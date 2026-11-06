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
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// aggTestOp gives the tests access to the REAL transition and finalize
// functions, which live on aggregateOp. Driving them (rather than
// reimplementing the state) is the whole point: a combine rule that disagrees
// with the transition function's own state representation is exactly the bug
// this file exists to catch.
func aggTestOp(ctx *Context) *aggregateOp {
	return &aggregateOp{ctx: ctx, plan: &optimizer.Aggregate{}}
}

// applyAggValue feeds one value through the real transition function.
// idx<0: the bare helper op is never Opened/compiled — interpreted path.
func applyAggValue(ctx *Context, call optimizer.AggregateCall, st *aggRuntime, v Datum) error {
	op := aggTestOp(ctx)
	slot := SlotFromRow(optimizer.Schema{{Name: "v"}}, Row{v})
	c := call
	c.Arg = &optimizer.ColumnRef{Index: 0}
	return op.applyAgg(st, &c, slot, -1)
}

// finishAggValue runs the real finalize function.
func finishAggValue(ctx *Context, call optimizer.AggregateCall, st *aggRuntime) (Datum, error) {
	op := aggTestOp(ctx)
	c := call
	c.Arg = &optimizer.ColumnRef{Index: 0}
	return op.finishAgg(st, &c)
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
		if !aggregateIsDecomposable(optimizer.AggregateCall{Name: name}) {
			t.Errorf("%s should be decomposable", name)
		}
	}

	// The refusals. These matter as much as the positives: a refusal that
	// silently stops refusing is how wrong results ship.
	for _, tc := range []struct {
		what string
		call optimizer.AggregateCall
	}{
		{"array_agg with its own ORDER BY", optimizer.AggregateCall{Name: "array_agg", OrderBy: []optimizer.SortKey{{}}}},
		{"string_agg DISTINCT", optimizer.AggregateCall{Name: "string_agg", Distinct: true}},
		{"DISTINCT (needs global dedup)", optimizer.AggregateCall{Name: "count", Distinct: true}},
		{"ORDER BY inside the aggregate", optimizer.AggregateCall{Name: "sum", OrderBy: []optimizer.SortKey{{}}}},
		{"percentile_cont (WITHIN GROUP)", optimizer.AggregateCall{Name: "percentile_cont"}},
		{"user aggregate without COMBINEFUNC", optimizer.AggregateCall{
			Name: "myagg", UserAgg: &catalog.UserAggregate{SFunc: "f"}}},
		{"user aggregate with COMBINEFUNC but not PARALLEL SAFE (default unsafe)",
			optimizer.AggregateCall{Name: "myagg", UserAgg: &catalog.UserAggregate{SFunc: "f", CombineFunc: "c"}}},
		{"user aggregate with an internal state type and no SERIALFUNC",
			optimizer.AggregateCall{Name: "myagg", UserAgg: &catalog.UserAggregate{
				SFunc: "f", CombineFunc: "c", Parallel: "safe", SType: "internal"}}},
	} {
		if aggregateIsDecomposable(tc.call) {
			t.Errorf("%s must NOT be decomposable", tc.what)
		}
	}

	// M0148-0001i: unordered array_agg / string_agg split as in PG 18
	// (array_agg_combine, string_agg_combine).
	for _, name := range []string{"array_agg", "string_agg"} {
		if !aggregateIsDecomposable(optimizer.AggregateCall{Name: name}) {
			t.Errorf("unordered %s should be decomposable (PG 18 has a combine function)", name)
		}
	}

	// M0148-0001i: a PARALLEL SAFE user aggregate with a COMBINEFUNC splits;
	// combineUserAggState runs the COMBINEFUNC in the combine phase.
	if !aggregateIsDecomposable(optimizer.AggregateCall{Name: "myagg", UserAgg: &catalog.UserAggregate{
		SFunc: "f", CombineFunc: "c", Parallel: "safe", SType: "int8"}}) {
		t.Error("a PARALLEL SAFE user aggregate with a COMBINEFUNC should be decomposable")
	}

	// The whitelist must refuse anything it does not know. applyAgg's default
	// arm silently does `count++; sum += arg.Int` for unrecognised names, so a
	// blacklist would let a future aggregate split through it and return
	// garbage.
	if aggregateIsDecomposable(optimizer.AggregateCall{Name: "some_future_aggregate"}) {
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
	call := optimizer.AggregateCall{Name: h.name, InputType: h.input}
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
		if err := combineAggRuntime(optimizer.AggregateCall{Name: h.name}, &acc, &states[i], nil); err != nil {
			h.t.Fatalf("%s combine: %v", h.name, err)
		}
	}

	return h.finish(&whole, call, ctx), h.finish(&acc, call, ctx)
}

func (h *aggHarness) finish(st *aggRuntime, call optimizer.AggregateCall, ctx *Context) Datum {
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
	call := optimizer.AggregateCall{Name: "var_pop", InputType: catalog.Type{Name: "float8"}}

	var withData, empty aggRuntime
	for _, v := range ints(2, 4, 6) {
		if err := applyAggValue(ctx, call, &withData, v); err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	before := withData

	// empty into populated
	if err := combineAggRuntime(optimizer.AggregateCall{Name: "var_pop"}, &withData, &empty, nil); err != nil {
		t.Fatalf("combine empty into populated: %v", err)
	}
	if withData.count != before.count || math.IsNaN(withData.floatM2) {
		t.Errorf("combining an empty partial corrupted the state: %+v", withData)
	}

	// populated into empty
	var acc aggRuntime
	if err := combineAggRuntime(optimizer.AggregateCall{Name: "var_pop"}, &acc, &before, nil); err != nil {
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
	if err := combineAggRuntime(optimizer.AggregateCall{Name: "percentile_cont"}, &a, &b, nil); err == nil {
		t.Error("an aggregate with no combine rule must error, not guess")
	}
}

// string_agg_combine (varlena.c) appends the second partial state WITH its
// leading delimiter — the first piece's own — and array_agg_combine
// concatenates the elements and their NULL flags; both survive the row
// transport unchanged (M0148-0001i).
func TestStringArrayAggCombineAndTransport(t *testing.T) {
	sa := optimizer.AggregateCall{Name: "string_agg"}
	dst := aggRuntime{hasValue: true, strAccum: []byte("a,b"), strFirstDelim: ","}
	src := aggRuntime{hasValue: true, strAccum: []byte("c|d"), strFirstDelim: "|"}
	if err := combineAggRuntime(sa, &dst, &src, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(dst.strAccum); got != "a,b|c|d" {
		t.Errorf("string_agg combine = %q, want %q (second state keeps its first delimiter)", got, "a,b|c|d")
	}
	var empty aggRuntime
	if err := combineAggRuntime(sa, &empty, &src, nil); err != nil || string(empty.strAccum) != "c|d" || empty.strFirstDelim != "|" {
		t.Errorf("string_agg into empty: %q/%q err %v", empty.strAccum, empty.strFirstDelim, err)
	}
	d, err := serializeAggRuntime("string_agg", &dst)
	if err != nil {
		t.Fatal(err)
	}
	back, err := deserializeAggRuntime("string_agg", d.BytesValue())
	if err != nil || string(back.strAccum) != "a,b|c|d" || back.strFirstDelim != "," || !back.hasValue {
		t.Errorf("string_agg round trip: %+v err %v", back, err)
	}

	aa := optimizer.AggregateCall{Name: "array_agg"}
	adst := aggRuntime{hasValue: true, arrayElems: []string{"1", ""}, arrayElemNull: []bool{false, true}}
	asrc := aggRuntime{hasValue: true, arrayElems: []string{"3"}, arrayElemNull: []bool{false}}
	if err := combineAggRuntime(aa, &adst, &asrc, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(adst.arrayElems, ",") != "1,,3" || len(adst.arrayElemNull) != 3 || !adst.arrayElemNull[1] {
		t.Errorf("array_agg combine = %v / %v", adst.arrayElems, adst.arrayElemNull)
	}
	ad, err := serializeAggRuntime("array_agg", &adst)
	if err != nil {
		t.Fatal(err)
	}
	aback, err := deserializeAggRuntime("array_agg", ad.BytesValue())
	if err != nil || strings.Join(aback.arrayElems, ",") != "1,,3" || !aback.arrayElemNull[1] || !aback.hasValue {
		t.Errorf("array_agg round trip: %+v err %v", aback, err)
	}
	// array_agg over arrays: mismatched dimensions are PG's 2202E.
	x := aggRuntime{hasValue: true, arrayOfArrays: true, arrayShape: []int{2}, arrayElems: []string{"{1,2}"}}
	y := aggRuntime{hasValue: true, arrayOfArrays: true, arrayShape: []int{3}, arrayElems: []string{"{1,2,3}"}}
	if err := combineAggRuntime(aa, &x, &y, nil); err == nil || !strings.Contains(err.Error(), "different dimensionality") {
		t.Errorf("mismatched array_agg dims combined: %v", err)
	}
}
