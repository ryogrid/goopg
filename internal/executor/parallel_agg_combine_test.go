package executor

// P5 of docs/design/parallel-query/ — the aggregate decomposition matrix
// (chapter 09 §4).
//
// The property under test throughout: splitting the input across N partial
// states and combining them must equal aggregating the whole input serially.
// Every test therefore runs the REAL transition function (applyAgg) over a
// partition of the same data, so a combine rule that disagrees with the
// transition function's own state representation is caught — which is exactly
// the bug the design's first draft contained for the variance lane.

import (
	"math"
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// aggTestOp gives the tests access to the REAL transition and finalize
// functions, which live on aggregateOp. Driving them (rather than
// reimplementing the state) is the whole point: a combine rule that disagrees
// with the transition function's own state representation is exactly the bug
// this file exists to catch.
func aggTestOp(ctx *Context) *aggregateOp {
	return &aggregateOp{ctx: ctx, plan: &planner.Aggregate{}}
}

// applyAggValue feeds one value through the real transition function.
func applyAggValue(ctx *Context, call planner.AggregateCall, st *aggRuntime, v Datum) error {
	op := aggTestOp(ctx)
	slot := SlotFromRow(planner.Schema{{Name: "v"}}, Row{v})
	c := call
	c.Arg = &planner.ColumnRef{Index: 0}
	return op.applyAgg(st, c, slot)
}

// finishAggValue runs the real finalize function.
func finishAggValue(ctx *Context, call planner.AggregateCall, st *aggRuntime) (Datum, error) {
	op := aggTestOp(ctx)
	c := call
	c.Arg = &planner.ColumnRef{Index: 0}
	return op.finishAgg(*st, c)
}

// datumFloatForTest coerces a finished aggregate result to float64.
func datumFloatForTest(t *testing.T, d Datum) float64 {
	t.Helper()
	f, ok := datumToFloat64(d)
	if !ok {
		t.Fatalf("cannot read %v as float", d)
	}
	return f
}

// ── decomposability whitelist ───────────────────────────────────────────────

func TestAggregateIsDecomposableWhitelist(t *testing.T) {
	for _, name := range []string{
		"count", "sum", "avg", "min", "max", "bool_and", "bool_or", "every",
		"bit_and", "bit_or", "bit_xor", "any_value",
		"var_pop", "var_samp", "variance", "stddev", "stddev_pop", "stddev_samp",
		"regr_count", "regr_sxx", "covar_pop", "corr",
	} {
		if !aggregateIsDecomposable(planner.AggregateCall{Name: name}) {
			t.Errorf("%s should be decomposable", name)
		}
	}

	// The refusals. These matter as much as the positives: a refusal that
	// silently stops refusing is how wrong results ship.
	for _, tc := range []struct {
		what string
		call planner.AggregateCall
	}{
		{"array_agg (order-dependent)", planner.AggregateCall{Name: "array_agg"}},
		{"string_agg (order-dependent)", planner.AggregateCall{Name: "string_agg"}},
		{"DISTINCT (needs global dedup)", planner.AggregateCall{Name: "count", Distinct: true}},
		{"ORDER BY inside the aggregate", planner.AggregateCall{Name: "sum", OrderBy: []planner.SortKey{{}}}},
		{"percentile_cont (WITHIN GROUP)", planner.AggregateCall{Name: "percentile_cont"}},
		{"user aggregate without COMBINEFUNC", planner.AggregateCall{
			Name: "myagg", UserAgg: &catalog.UserAggregate{SFunc: "f"}}},
		{"user aggregate declaring COMBINEFUNC (no combine rule for user aggs)",
			planner.AggregateCall{Name: "myagg", UserAgg: &catalog.UserAggregate{SFunc: "f", CombineFunc: "c"}}},
	} {
		if aggregateIsDecomposable(tc.call) {
			t.Errorf("%s must NOT be decomposable", tc.what)
		}
	}

	// The whitelist must refuse anything it does not know. applyAgg's default
	// arm silently does `count++; sum += arg.Int` for unrecognised names, so a
	// blacklist would let a future aggregate split through it and return
	// garbage.
	if aggregateIsDecomposable(planner.AggregateCall{Name: "some_future_aggregate"}) {
		t.Error("an unknown aggregate must be refused, not guessed at")
	}
}

// ── partial + combine == serial ─────────────────────────────────────────────

// aggHarness runs one aggregate over `values`, both serially and split across
// `parts` partial states, and returns the two finished results for comparison.
type aggHarness struct {
	t     *testing.T
	name  string
	input catalog.Type
}

func (h *aggHarness) run(values []Datum, parts int) (serial, combined Datum) {
	h.t.Helper()
	call := planner.AggregateCall{Name: h.name, InputType: h.input}
	ctx := NewContext()

	apply := func(st *aggRuntime, vals []Datum) {
		for _, v := range vals {
			if err := applyAggValue(ctx, call, st, v); err != nil {
				h.t.Fatalf("%s transition: %v", h.name, err)
			}
		}
	}

	var whole aggRuntime
	apply(&whole, values)

	states := make([]aggRuntime, parts)
	for i, v := range values {
		apply(&states[i%parts], []Datum{v})
	}
	acc := states[0]
	for i := 1; i < parts; i++ {
		if err := combineAggRuntime(h.name, &acc, &states[i]); err != nil {
			h.t.Fatalf("%s combine: %v", h.name, err)
		}
	}

	return h.finish(&whole, call, ctx), h.finish(&acc, call, ctx)
}

func (h *aggHarness) finish(st *aggRuntime, call planner.AggregateCall, ctx *Context) Datum {
	h.t.Helper()
	d, err := finishAggValue(ctx, call, st)
	if err != nil {
		h.t.Fatalf("%s finalize: %v", h.name, err)
	}
	return d
}

func ints(vs ...int64) []Datum {
	out := make([]Datum, len(vs))
	for i, v := range vs {
		out[i] = NewIntDatum(v)
	}
	return out
}

// TestCombineExactAggregatesMatchSerial covers the aggregates whose result
// must be BIT-IDENTICAL to serial execution: they accumulate exactly.
func TestCombineExactAggregatesMatchSerial(t *testing.T) {
	vals := ints(5, 3, 9, 1, 7, 2, 8, 4, 6, 10)
	for _, name := range []string{"count", "sum", "avg", "min", "max"} {
		for _, parts := range []int{2, 3, 4} {
			h := &aggHarness{t: t, name: name, input: catalog.Type{Name: "int8"}}
			serial, combined := h.run(vals, parts)
			if serial.Format() != combined.Format() {
				t.Errorf("%s with %d partials: serial %s, combined %s — must be identical",
					name, parts, serial.Format(), combined.Format())
			}
		}
	}
}

// TestCombineVarianceMatchesSerial is the test that would have caught the
// design's original error. floatSx is Sigma-x, not the mean, so Sx adds
// plainly and only Sxx needs a correction term; a Chan-Golub-LeVeque combine
// over means produces silently wrong numbers here.
//
// Float-lane aggregates are NOT bit-identical to serial — they accumulate with
// float64 += — so the comparison is within a tolerance, as chapter 09 §1
// requires. That carve-out is stated rather than discovered.
func TestCombineVarianceMatchesSerial(t *testing.T) {
	vals := ints(2, 4, 4, 4, 5, 5, 7, 9)
	for _, name := range []string{"var_pop", "var_samp", "stddev_pop", "stddev_samp"} {
		for _, parts := range []int{2, 3, 4} {
			h := &aggHarness{t: t, name: name, input: catalog.Type{Name: "float8"}}
			serial, combined := h.run(vals, parts)
			s, c := datumFloatForTest(t, serial), datumFloatForTest(t, combined)
			if math.Abs(s-c) > 1e-9*math.Max(1, math.Abs(s)) {
				t.Errorf("%s with %d partials: serial %g, combined %g", name, parts, s, c)
			}
		}
	}
}

// TestCombineVarianceEmptyPartial pins the N == 0 case, which must be handled
// BEFORE the general formula because that formula divides by both counts. A
// worker producing an empty partial for a group is routine, not exotic — it
// happens whenever a group's rows all land in other workers' blocks.
func TestCombineVarianceEmptyPartial(t *testing.T) {
	ctx := NewContext()
	call := planner.AggregateCall{Name: "var_pop", InputType: catalog.Type{Name: "float8"}}

	var withData, empty aggRuntime
	for _, v := range ints(2, 4, 6) {
		if err := applyAggValue(ctx, call, &withData, v); err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	before := withData

	// empty into populated
	if err := combineAggRuntime("var_pop", &withData, &empty); err != nil {
		t.Fatalf("combine empty into populated: %v", err)
	}
	if withData.count != before.count || math.IsNaN(withData.floatM2) {
		t.Errorf("combining an empty partial corrupted the state: %+v", withData)
	}

	// populated into empty
	var acc aggRuntime
	if err := combineAggRuntime("var_pop", &acc, &before); err != nil {
		t.Fatalf("combine populated into empty: %v", err)
	}
	if acc.count != before.count {
		t.Errorf("count = %d, want %d", acc.count, before.count)
	}
}

// TestCombineFloatSpecialPrecedence pins the NaN/Inf rules. A wrong precedence
// differs from serial output ONLY on data containing infinities, which no
// ordinary test data would reach.
func TestCombineFloatSpecialPrecedence(t *testing.T) {
	for _, tc := range []struct{ a, b, want floatSpecialKind }{
		{floatSpecialNone, floatSpecialNone, floatSpecialNone},
		{floatSpecialNaN, floatSpecialNone, floatSpecialNaN},
		{floatSpecialNone, floatSpecialNaN, floatSpecialNaN},
		{floatSpecialNaN, floatSpecialPosInf, floatSpecialNaN},
		{floatSpecialPosInf, floatSpecialNone, floatSpecialPosInf},
		{floatSpecialNegInf, floatSpecialNone, floatSpecialNegInf},
		{floatSpecialPosInf, floatSpecialPosInf, floatSpecialPosInf},
		// +Inf combined with -Inf is NaN, per IEEE addition.
		{floatSpecialPosInf, floatSpecialNegInf, floatSpecialNaN},
		{floatSpecialNegInf, floatSpecialPosInf, floatSpecialNaN},
	} {
		if got := combineFloatSpecial(tc.a, tc.b); got != tc.want {
			t.Errorf("combineFloatSpecial(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestCombineVarianceNaNConvention pins the goopg-specific signal: the
// variance lane marks NaN/Inf by setting floatM2 to NaN, distinct from the
// floatSpecial field that sum/avg use.
func TestCombineVarianceNaNConvention(t *testing.T) {
	var dst, src aggRuntime
	dst.count, dst.floatSx, dst.floatM2 = 3, 12, 8
	src.count, src.floatSx, src.floatM2 = 2, 6, math.NaN()
	combineVariance(&dst, &src)
	if !math.IsNaN(dst.floatM2) {
		t.Errorf("NaN in either partial must yield NaN, got %g", dst.floatM2)
	}
}

// TestCombineExactIntegerLane pins that the exact big.Int lane adds rather
// than being silently dropped, and that a nil (never-touched) partial is
// treated as zero rather than panicking.
func TestCombineExactIntegerLane(t *testing.T) {
	var dst, src aggRuntime
	dst.intExact, dst.intSx, dst.intSxx = true, big.NewInt(10), big.NewInt(100)
	src.intExact, src.intSx, src.intSxx = true, big.NewInt(5), big.NewInt(25)
	dst.count, src.count = 2, 1
	combineVariance(&dst, &src)
	if dst.intSx.Int64() != 15 || dst.intSxx.Int64() != 125 {
		t.Errorf("exact integer lane = (%v,%v), want (15,125)", dst.intSx, dst.intSxx)
	}

	// nil partial must not panic and must not corrupt.
	var empty aggRuntime
	combineVariance(&dst, &empty)
	if dst.intSx.Int64() != 15 {
		t.Errorf("a nil exact lane corrupted the accumulator: %v", dst.intSx)
	}
}

// TestCombineRejectsUnknownAggregate pins the loud failure: an aggregate with
// no combine rule must error rather than return a plausible wrong answer.
func TestCombineRejectsUnknownAggregate(t *testing.T) {
	var a, b aggRuntime
	if err := combineAggRuntime("array_agg", &a, &b); err == nil {
		t.Error("an aggregate with no combine rule must error, not guess")
	}
}
