package executor

// parallel_agg_combine.go — P5 of docs/design/parallel-query/, chapter 06.
//
// Combining two partial aggregate states into one. This is what a Finalize
// node does with the per-group states its workers produced.
//
// goopg needs no serialisation to get here. PG's aggserialfn/aggdeserialfn
// exist solely because an `internal`-typed transition state cannot cross a
// process boundary: the worker flattens it to bytea and the leader rebuilds
// it. Workers here hand the aggRuntime across a channel directly, which
// removes an entire feature surface — and with it a classic round-trip
// mismatch bug class.
//
// What it costs instead: aggRuntime is a fat struct with pointer fields
// (big.Int, big.Rat, maps, slices), so combining is a DEEP MERGE with an
// explicit rule per field, not a struct add. Anything without a correct rule
// must be refused by the planner rather than approximated here.

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// normalizeAggName matches how applyAgg and finishAgg dispatch on the name
// (strings.ToLower(call.Name)), so the whitelist and the combine rules cannot
// disagree with the transition function about what an aggregate is called.
func normalizeAggName(n string) string { return strings.ToLower(strings.TrimSpace(n)) }

// aggregateIsDecomposable reports whether an aggregate call can be split into
// partial + finalize.
//
// This is a WHITELIST, deliberately. applyAgg ends in a `default:` catch-all
// that does `st.count++; st.sum += arg.Int` for any unrecognised name, so a
// blacklist would let an aggregate added later split through that arm and
// return garbage. A name absent from this function is refused, not guessed at.
func aggregateIsDecomposable(call planner.AggregateCall) bool {
	// Order-dependence and global-state requirements defeat the split
	// regardless of the function.
	if call.Distinct {
		// Each worker's `distinct` map sees only its own share, so dedup
		// would be per-worker rather than global.
		return false
	}
	if len(call.OrderBy) > 0 {
		// An ORDER BY inside the aggregate makes the result depend on input
		// order, which parallel scans do not preserve.
		return false
	}
	if call.UserAgg != nil {
		// A user aggregate is decomposable exactly when it declares a
		// combine function — which is what COMBINEFUNC is for, and which
		// goopg already parses and stores.
		return call.UserAgg.CombineFunc != ""
	}

	switch normalizeAggName(call.Name) {
	case "count", "sum", "avg", "min", "max",
		"bool_and", "every", "bool_or",
		"bit_and", "bit_or", "bit_xor",
		"any_value",
		"var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev",
		"regr_count", "regr_avgx", "regr_avgy", "regr_sxx", "regr_syy", "regr_sxy",
		"covar_pop", "covar_samp", "regr_r2", "regr_slope", "regr_intercept", "corr":
		return true
	default:
		// Includes array_agg and string_agg (order-dependent), the
		// WITHIN GROUP ordered-set aggregates, and anything new.
		return false
	}
}

// combineAggRuntime merges src into dst for the named aggregate.
//
// dst is the accumulating state; src is a worker's partial. Both must come
// from the same aggregate call. Returns an error for a name this file has no
// rule for — which should be unreachable, because the planner consults
// aggregateIsDecomposable first, but an unreachable wrong answer is worse than
// an unreachable error.
func combineAggRuntime(name string, dst, src *aggRuntime) error {
	switch normalizeAggName(name) {
	case "count":
		dst.count += src.count

	case "sum", "avg":
		// avg needs no special handling: sum and avg share one transition arm
		// accumulating (sum, count), and diverge only in finishAgg. The
		// composite transition state PG has to synthesise for a parallel avg
		// already exists here.
		if err := combineNumericSum(dst, src); err != nil {
			return err
		}
		dst.count += src.count
		dst.floatSpecial = combineFloatSpecial(dst.floatSpecial, src.floatSpecial)
		if src.hasValue {
			dst.hasValue = true
		}

	case "min":
		if err := combineExtremum(dst, src, true); err != nil {
			return err
		}
	case "max":
		if err := combineExtremum(dst, src, false); err != nil {
			return err
		}

	case "bool_and", "every":
		if src.hasValue {
			if dst.hasValue {
				dst.boolResult = dst.boolResult && src.boolResult
			} else {
				dst.boolResult, dst.hasValue = src.boolResult, true
			}
		}
	case "bool_or":
		if src.hasValue {
			if dst.hasValue {
				dst.boolResult = dst.boolResult || src.boolResult
			} else {
				dst.boolResult, dst.hasValue = src.boolResult, true
			}
		}

	case "bit_and":
		if src.hasValue {
			if dst.hasValue {
				dst.intResult &= src.intResult
			} else {
				dst.intResult, dst.hasValue = src.intResult, true
			}
		}
	case "bit_or":
		if src.hasValue {
			if dst.hasValue {
				dst.intResult |= src.intResult
			} else {
				dst.intResult, dst.hasValue = src.intResult, true
			}
		}
	case "bit_xor":
		if src.hasValue {
			if dst.hasValue {
				dst.intResult ^= src.intResult
			} else {
				dst.intResult, dst.hasValue = src.intResult, true
			}
		}

	case "any_value":
		if !dst.hasValue && src.hasValue {
			dst.value, dst.hasValue = src.value, true
		}

	case "var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev":
		combineVariance(dst, src)

	case "regr_count", "regr_avgx", "regr_avgy", "regr_sxx", "regr_syy", "regr_sxy",
		"covar_pop", "covar_samp", "regr_r2", "regr_slope", "regr_intercept", "corr":
		// goopg stores UNCENTERED raw sums here (regrSumXX += x*x), unlike
		// PG's centered Youngs-Cramer regression state — so plain addition
		// really is the correct combine, with no correction term. Worth
		// stating because it is the opposite of the variance case two arms up.
		dst.regrN += src.regrN
		dst.regrSumX += src.regrSumX
		dst.regrSumY += src.regrSumY
		dst.regrSumXX += src.regrSumXX
		dst.regrSumXY += src.regrSumXY
		dst.regrSumYY += src.regrSumYY

	default:
		return fmt.Errorf("internal error: no combine rule for aggregate %q; "+
			"aggregateIsDecomposable should have refused the parallel split", name)
	}
	return nil
}

// combineNumericSum adds src's running sum into dst, honouring the int and
// numeric lanes the transition function picks between.
func combineNumericSum(dst, src *aggRuntime) error {
	dst.sum += src.sum
	if src.numericSum.Kind == KindNumeric {
		if dst.numericSum.Kind != KindNumeric {
			dst.numericSum = src.numericSum
			return nil
		}
		s, err := numericAdd(dst.numericSum, src.numericSum)
		if err != nil {
			return &ExecError{Code: "22003", Message: err.Error()}
		}
		dst.numericSum = s
	}
	return nil
}

// combineFloatSpecial reproduces the NaN/Inf precedence the transition
// function applies, exactly.
//
// Getting this wrong produces a result that differs from serial execution ONLY
// on data containing infinities — which no ordinary test would notice.
func combineFloatSpecial(a, b floatSpecialKind) floatSpecialKind {
	if a == floatSpecialNaN || b == floatSpecialNaN {
		return floatSpecialNaN
	}
	if a == floatSpecialNone {
		return b
	}
	if b == floatSpecialNone {
		return a
	}
	if a != b {
		// +Inf combined with -Inf is NaN, per IEEE addition.
		return floatSpecialNaN
	}
	return a
}

// combineExtremum merges min/max state.
func combineExtremum(dst, src *aggRuntime, wantMin bool) error {
	if !src.hasValue {
		return nil
	}
	if !dst.hasValue {
		dst.value, dst.hasValue = src.value, true
		return nil
	}
	cmp, err := compareDatum(src.value, dst.value, 0)
	if err != nil {
		return err
	}
	if (wantMin && cmp < 0) || (!wantMin && cmp > 0) {
		dst.value = src.value
	}
	return nil
}

// combineVariance merges the three representations the variance family keeps.
//
// The float lane is the trap in this whole file. `floatSx` is the running SUM
// of values (Sigma-x), NOT the mean — read the field comment, not the
// algorithm's name. So Sx adds plainly and only Sxx needs a correction term.
// An earlier draft of the design prescribed a Chan-Golub-LeVeque combine over
// MEANS, which for this state representation silently produces wrong var_* and
// stddev_* results.
//
// The rule below is PG's float8_combine
// (postgres/src/backend/utils/adt/float.c), including its N == 0 special cases
// — which must be handled BEFORE the general formula, since it divides by both
// N1 and N2, and a worker producing an empty partial for a group is routine.
func combineVariance(dst, src *aggRuntime) {
	// NaN convention: the variance lane signals NaN/Inf by setting floatM2 to
	// NaN (a goopg-specific convention, distinct from floatSpecial, which
	// covers sum/avg). NaN in either input yields NaN.
	dstNaN := math.IsNaN(dst.floatM2)
	srcNaN := math.IsNaN(src.floatM2)

	// Exact lanes: plain sums, and nil means "this worker saw nothing".
	if src.intSx != nil {
		if dst.intSx == nil {
			dst.intSx, dst.intSxx = new(big.Int), new(big.Int)
			dst.intExact = true
		}
		dst.intSx.Add(dst.intSx, src.intSx)
		if src.intSxx != nil {
			if dst.intSxx == nil {
				dst.intSxx = new(big.Int)
			}
			dst.intSxx.Add(dst.intSxx, src.intSxx)
		}
	}
	if src.numericSx != nil {
		if dst.numericSx == nil {
			dst.numericSx, dst.numericSxx = new(big.Rat), new(big.Rat)
			dst.numericExact = true
		}
		dst.numericSx.Add(dst.numericSx, src.numericSx)
		if src.numericSxx != nil {
			if dst.numericSxx == nil {
				dst.numericSxx = new(big.Rat)
			}
			dst.numericSxx.Add(dst.numericSxx, src.numericSxx)
		}
	}

	n1, n2 := dst.count, src.count
	switch {
	case n2 == 0:
		// Nothing to merge; dst keeps its state.
	case n1 == 0:
		dst.count = n2
		dst.floatSx = src.floatSx
		dst.floatM2 = src.floatM2
	default:
		n := n1 + n2
		sx := dst.floatSx + src.floatSx
		// Sxx = Sxx1 + Sxx2 + N1*N2*(Sx1/N1 - Sx2/N2)^2 / N
		d := dst.floatSx/float64(n1) - src.floatSx/float64(n2)
		sxx := dst.floatM2 + src.floatM2 + float64(n1)*float64(n2)*d*d/float64(n)
		dst.count = n
		dst.floatSx = sx
		dst.floatM2 = sxx
	}

	if dstNaN || srcNaN {
		dst.floatM2 = math.NaN()
	}
	if src.hasValue {
		dst.hasValue = true
	}
}
