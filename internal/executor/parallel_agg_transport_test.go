package executor

// M0146-0003b (M0141-S3/S4, adopted by M0146-0003): row-transport partial
// aggregates. The Partial node emits [group key values | passthrough
// values | serialized transition states] as ordinary rows — PG's
// aggserialfn-shaped transport — and the Finalize node deserialises and
// combineAggRuntime's them per group. Unlike the zero-row accumulator
// model (parallel_agg_split.go) the pair survives any row-moving node
// between them: a Sort under Gather Merge (PG's
// `Finalize GroupAggregate -> Gather Merge -> Sort -> Partial
// HashAggregate`) or a plain Gather.
//
// The failure this file exists to catch is the same one
// TestPartialFinalizeIdentity names: an N-times overcount when every
// worker aggregates the whole relation because the parallel scan never
// claimed it — plausible output, nothing flags it. Comparing against
// serial execution finds it.

import (
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// runTransportSplit plans sql, clones its aggregate into a
// PartialEmit partial/finalize pair, and runs it under a Gather (or a
// Sort->GatherMerge composition when merged is true). Returns the
// rendered rows.
func runTransportSplit(t *testing.T, ctx *Context, sql string, workers int, merged bool, sortedFinal bool) []string {
	t.Helper()
	node := planForTest(t, ctx, sql)
	spec := findGroupAgg(node)
	if spec == nil {
		t.Fatalf("plan for %q has no aggregate to clone", sql)
	}
	if spec.Mode != optimizer.AggModeSimple || spec.GroupingSets != nil || len(spec.Aggs) == 0 {
		t.Fatalf("plan for %q: aggregate not a plain splittable spec", sql)
	}

	partial := *spec
	partial.Mode = optimizer.AggModePartial
	partial.PartialEmit = true

	var transport optimizer.Node = &partial
	if merged {
		// PG's `Gather Merge -> Sort -> Partial HashAggregate`: the sort
		// orders the partial's OUTPUT (positions 0..nGroupCols-1 are the
		// group key values), the merge interleaves the worker streams.
		var keys []optimizer.SortKey
		for i, ge := range spec.GroupExprs {
			typ := catalog.Type{Name: "int4"}
			if cr, ok := ge.(*optimizer.ColumnRef); ok && cr.Type.Name != "" {
				typ = cr.Type
			}
			keys = append(keys, optimizer.SortKey{
				Expr: &optimizer.ColumnRef{Index: i, Name: "gk", Type: typ},
			})
		}
		sorted := &optimizer.Sort{Child: transport, Keys: keys}
		transport = optimizer.NewGatherMerge(0, sorted, workers, keys)
	} else {
		transport = optimizer.NewGather(0, &partial, workers)
	}

	final := *spec
	final.Mode = optimizer.AggModeFinal
	final.PartialEmit = true
	final.PartialSource = nil
	if sortedFinal {
		// M0146-0003 S5: `Finalize GroupAggregate` — the finalize
		// consumes the merge-ordered state stream one live group at a
		// time rather than absorbing every row into a group map.
		final.Strategy = optimizer.AggStrategySorted
	}
	final.Child = transport

	advanceStmtCounter(ctx)
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(&final)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out
}

func checkTransportIdentity(t *testing.T, ctx *Context, sql string, merged bool, sortedFinal bool) {
	t.Helper()
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)
	sortStrings := func(s []string) {
		for i := 1; i < len(s); i++ {
			for j := i; j > 0 && s[j] < s[j-1]; j-- {
				s[j], s[j-1] = s[j-1], s[j]
			}
		}
	}
	for _, workers := range []int{1, 2, 4} {
		got := runTransportSplit(t, ctx, sql, workers, merged, sortedFinal)
		if len(got) != len(want) {
			t.Fatalf("merged=%v workers=%d: got %d rows, want %d\n got=%v\nwant=%v",
				merged, workers, len(got), len(want), got, want)
		}
		if !merged {
			// A plain Gather delivers rows in worker-completion order;
			// compare as a multiset. The GatherMerge case is positionally
			// ordered by construction.
			sortStrings(got)
			sortStrings(want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("merged=%v workers=%d: row %d differs:\n got %q\nwant %q",
					merged, workers, i, got[i], want[i])
			}
		}
	}
}

// TestPartialEmitIdentity is the gate for the transport pair, over both
// Gather and Sort->GatherMerge shapes, at 1/2/4 workers.
func TestPartialEmitIdentity(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name   string
		sql    string
		merged bool
	}{
		// Ungrouped — every worker emits exactly one state row; the
		// finalize folds N partial states into the pre-seeded group.
		{"ungrouped", "SELECT count(*), sum(v), avg(v), min(v), max(v) FROM pq_agg", false},
		// Grouped over plain Gather.
		{"grouped-gather", "SELECT grp, count(*), sum(v) FROM pq_agg GROUP BY grp ORDER BY grp", false},
		{"grouped-gather-aggmix", "SELECT grp, avg(v), min(v), max(v) FROM pq_agg GROUP BY grp ORDER BY grp", false},
		// Float lane — floatSpecial and hasValue must round-trip.
		{"grouped-float", "SELECT grp, sum(f), avg(f) FROM pq_agg GROUP BY grp ORDER BY grp", false},
		// Groups absent from some workers (the filter takes whole grp
		// values below most workers' share) — a worker's empty partial
		// must not poison the combine.
		{"grouped-sparse", "SELECT grp, count(*) FROM pq_agg WHERE id < 20 GROUP BY grp ORDER BY grp", false},
		// The PG stack: Sort under Gather Merge.
		{"grouped-gathermerge", "SELECT grp, count(*), sum(v) FROM pq_agg GROUP BY grp ORDER BY grp", true},
		{"grouped-gathermerge-aggmix", "SELECT grp, avg(v), min(v), max(v) FROM pq_agg GROUP BY grp ORDER BY grp", true},
		{"grouped-gathermerge-float", "SELECT grp, sum(f), avg(f) FROM pq_agg GROUP BY grp ORDER BY grp", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkTransportIdentity(t, ctx, tc.sql, tc.merged, false)
		})
	}
}

// TestAggRuntimeSerialization round-trips every family the decomposable
// whitelist admits, including the pointer fields the flat scalars can't
// express — int/numeric exact-variance sums, numericSum, Datum values,
// NaN/Inf float specials. A field silently dropped here is a wrong final
// answer on real data.
func TestAggRuntimeSerialization(t *testing.T) {
	mkDatum := func() Datum { return NewIntDatum(42) }
	cases := []struct {
		label string
		name  string
		st    aggRuntime
	}{
		{"count", "count", aggRuntime{count: 991}},
		{"sum", "sum", aggRuntime{hasValue: true, sum: -7, count: 3, floatSpecial: floatSpecialPosInf}},
		{"sum-numeric", "sum", aggRuntime{hasValue: true, count: 2,
			numericSum: Datum{Kind: KindNumeric, Int: 12345, Scale: 2}}},
		{"avg-floatnan", "avg", aggRuntime{hasValue: true, count: 5, floatSpecial: floatSpecialNaN}},
		{"min", "min", aggRuntime{hasValue: true, value: mkDatum()}},
		{"max-empty", "max", aggRuntime{}},
		{"bool_and", "bool_and", aggRuntime{hasValue: true, boolResult: true}},
		{"bool_or", "bool_or", aggRuntime{hasValue: true}},
		{"bit_and", "bit_and", aggRuntime{hasValue: true, intResult: -255, strResult: "32"}},
		{"any_value", "any_value", aggRuntime{hasValue: true, value: NewStringDatum("picked")}},
		{"var_samp-int", "var_samp", func() aggRuntime {
			st := aggRuntime{hasValue: true, intExact: true, count: 4}
			st.intSx = big.NewInt(300)
			st.intSxx = big.NewInt(25000)
			return st
		}()},
		{"var_pop-numeric", "var_pop", func() aggRuntime {
			st := aggRuntime{hasValue: true, numericExact: true, count: 2}
			st.numericSx = big.NewRat(7, 2)
			st.numericSxx = big.NewRat(99, 8)
			return st
		}()},
		{"stddev-float", "stddev", aggRuntime{hasValue: true, count: 9,
			floatSx: 12.5, floatM2: math.Float64frombits(math.Float64bits(3.25))}},
		{"stddev-nan", "stddev", aggRuntime{hasValue: true, count: 1, floatSx: 1, floatM2: math.NaN()}},
		{"regr_slope", "regr_slope", aggRuntime{regrN: 7, regrSumX: 2.5, regrSumY: -3.5,
			regrSumXX: 9.25, regrSumXY: -8.125, regrSumYY: 17.75}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			d, err := serializeAggRuntime(tc.name, &tc.st)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			if d.Kind != KindBytes {
				t.Fatalf("state column kind %v, want KindBytes", d.Kind)
			}
			got, err := deserializeAggRuntime(tc.name, d.BytesValue())
			if err != nil {
				t.Fatalf("deserialize: %v", err)
			}
			if !aggRuntimeStateEqual(tc.name, tc.st, got) {
				t.Fatalf("round trip dropped a field:\n in=%+v\nout=%+v", tc.st, got)
			}
		})
	}
}

// aggRuntimeStateEqual compares the serialisable surface field-by-field
// for the named family.
func aggRuntimeStateEqual(name string, a, b aggRuntime) bool {
	switch name {
	case "count":
		return a.count == b.count
	case "sum", "avg":
		return a.hasValue == b.hasValue && a.sum == b.sum && a.count == b.count &&
			a.floatSpecial == b.floatSpecial &&
			a.numericSum.Kind == b.numericSum.Kind && a.numericSum.Int == b.numericSum.Int &&
			a.numericSum.Scale == b.numericSum.Scale
	case "min", "max", "any_value":
		return a.hasValue == b.hasValue &&
			a.value.Kind == b.value.Kind && a.value.Int == b.value.Int &&
			string(a.value.Buf) == string(b.value.Buf)
	case "bool_and", "every", "bool_or":
		return a.hasValue == b.hasValue && a.boolResult == b.boolResult
	case "bit_and", "bit_or", "bit_xor":
		return a.hasValue == b.hasValue && a.intResult == b.intResult && a.strResult == b.strResult
	case "var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev":
		if a.hasValue != b.hasValue || a.intExact != b.intExact || a.numericExact != b.numericExact ||
			a.count != b.count || a.floatSx != b.floatSx {
			return false
		}
		if !(math.IsNaN(a.floatM2) && math.IsNaN(b.floatM2)) && a.floatM2 != b.floatM2 {
			return false
		}
		bigEq := func(x, y *big.Int) bool {
			if x == nil || y == nil {
				return x == y
			}
			return x.Cmp(y) == 0
		}
		ratEq := func(x, y *big.Rat) bool {
			if x == nil || y == nil {
				return x == y
			}
			return x.Cmp(y) == 0
		}
		return bigEq(a.intSx, b.intSx) && bigEq(a.intSxx, b.intSxx) &&
			ratEq(a.numericSx, b.numericSx) && ratEq(a.numericSxx, b.numericSxx)
	default: // regr family
		return a.regrN == b.regrN && a.regrSumX == b.regrSumX && a.regrSumY == b.regrSumY &&
			a.regrSumXX == b.regrSumXX && a.regrSumXY == b.regrSumXY && a.regrSumYY == b.regrSumYY
	}
}

// TestAggRuntimeSerializationRefusals pins the fail-closed edges: a
// state outside the whitelist's surface errors rather than silently
// dropping fields, a non-whitelist name has no rule, and a truncated or
// padded frame is a corruption error, not a partial decode.
func TestAggRuntimeSerializationRefusals(t *testing.T) {
	if _, err := serializeAggRuntime("array_agg", &aggRuntime{}); err == nil {
		t.Error("array_agg serialised — the whitelist and the codec disagree")
	}
	if _, err := serializeAggRuntime("count", &aggRuntime{distinct: map[string]struct{}{"x": {}}}); err == nil {
		t.Error("a DISTINCT-carrying state serialised — the field belt is open")
	}
	if _, err := serializeAggRuntime("sum", &aggRuntime{strAccum: []byte("ab")}); err == nil {
		t.Error("a string_agg accumulator serialised through the sum arm")
	}
	if _, err := deserializeAggRuntime("array_agg", []byte{1}); err == nil {
		t.Error("array_agg deserialised")
	}
	if _, err := deserializeAggRuntime("count", []byte{1, 2}); err == nil {
		t.Error("a truncated count frame decoded")
	}
	full, err := serializeAggRuntime("count", &aggRuntime{count: 1})
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	padded := append(full.BytesValue(), 0xAA)
	if _, err := deserializeAggRuntime("count", padded); err == nil {
		t.Error("a padded count frame decoded — trailing bytes must error")
	}
}

// TestPartialEmitPairingErrors: a transport pair is only honest when
// BOTH ends carry the flag. A non-emitting partial under a transport
// finalize delivers zero state rows, and a transport partial under an
// accumulator finalize leaves the accumulator unregistered — both must
// error rather than answer with what arrived.
func TestPartialEmitPairingErrors(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	// Transport finalize over a NON-emitting partial: the partial
	// refuses first (no accumulator registered), which is the loud
	// failure — pinned so a quiet empty result can never stand in.
	node := planForTest(t, ctx, "SELECT grp, count(*) FROM pq_agg GROUP BY grp")
	spec := findGroupAgg(node)
	partial := *spec
	partial.Mode = optimizer.AggModePartial // no PartialEmit
	final := *spec
	final.Mode = optimizer.AggModeFinal
	final.PartialEmit = true
	final.PartialSource = nil
	final.Child = optimizer.NewGather(0, &partial, 1)

	advanceStmtCounter(ctx)
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(&final)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	err = op.Open(ctx)
	if err == nil {
		// Open may succeed while the error arrives on first Next — drain.
		_, err = op.Next()
	}
	if err == nil {
		t.Fatal("mismatched pair produced no error")
	}
	if !strings.Contains(err.Error(), "partial aggregate") &&
		!strings.Contains(err.Error(), "partial-state") {
		t.Fatalf("mismatched pair errored, but not the pairing failure: %v", err)
	}
	_ = op.Close()
}

// TestPartialEmitSortedIdentity pins the GatherMerge-fed Finalize-Sorted
// arm (M0146-0003 S5): Strategy=AggStrategySorted on the transport
// finalize, which folds same-key state rows into ONE live group instead
// of absorbing every row into a group map — PG's `Finalize
// GroupAggregate -> Gather Merge -> Sort -> Partial HashAggregate`.
// The merged splice supplies the order contract; the comparison is
// positional (sorted output, not a multiset) because ORDER BY grp's
// serial result defines the merge order.
func TestPartialEmitSortedIdentity(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name   string
		sql    string
		merged bool
	}{
		// Ungrouped — no merge needed: every transported row carries the
		// same (empty) key, so the fold is a single group over a plain
		// Gather.
		{"ungrouped", "SELECT count(*), sum(v), avg(v), min(v), max(v) FROM pq_agg", false},
		// The PG stack: Finalize GroupAggregate over Gather Merge.
		{"sorted-gathermerge", "SELECT grp, count(*), sum(v) FROM pq_agg GROUP BY grp ORDER BY grp", true},
		{"sorted-gathermerge-aggmix", "SELECT grp, avg(v), min(v), max(v) FROM pq_agg GROUP BY grp ORDER BY grp", true},
		{"sorted-gathermerge-float", "SELECT grp, sum(f), avg(f) FROM pq_agg GROUP BY grp ORDER BY grp", true},
		// Groups absent from some workers: an empty worker contributes no
		// state row, so its key's run is simply shorter.
		{"sorted-gathermerge-sparse", "SELECT grp, count(*) FROM pq_agg WHERE id < 20 GROUP BY grp ORDER BY grp", true},
		// Two-column key — the merge order and the boundary test both
		// walk every key column.
		{"sorted-gathermerge-twokey", "SELECT grp, s, count(*) FROM pq_agg GROUP BY grp, s ORDER BY grp, s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkTransportIdentity(t, ctx, tc.sql, tc.merged, true)
		})
	}
}

// TestPartialEmitSortedRejectsUnsorted pins the order belt: a sorted
// row-transport finalize fed a stream whose keys descend must error,
// never emit a group twice. Without the belt a key recurring after a
// higher key produces two output rows for one group — a silently wrong
// result.
func TestPartialEmitSortedRejectsUnsorted(t *testing.T) {
	ctx, cleanup := pqAggFixture(t)
	defer cleanup()

	node := planForTest(t, ctx, "SELECT grp, count(*) FROM pq_agg GROUP BY grp")
	spec := findGroupAgg(node)
	if spec == nil {
		t.Fatal("no aggregate in plan")
	}
	final := *spec
	final.Mode = optimizer.AggModeFinal
	final.PartialEmit = true
	final.Strategy = optimizer.AggStrategySorted
	final.PartialSource = nil

	mk := func(grp, cnt int64) Row {
		d, err := serializeAggRuntime("count", &aggRuntime{count: cnt})
		if err != nil {
			t.Fatalf("serialize: %v", err)
		}
		return Row{NewIntDatum(grp), d}
	}

	// Ascending then descending: the 2->1 boundary must trip the belt.
	op := &aggregateOp{plan: &final, child: &rowsOp{rows: []Row{mk(1, 3), mk(2, 5), mk(1, 7)}}, schema: final.Output()}
	err := op.Open(ctx)
	if err == nil {
		t.Fatal("unsorted partial-state stream produced no error")
	}
	if !strings.Contains(err.Error(), "not ordered by group key") {
		t.Fatalf("unsorted stream errored, but not the order belt: %v", err)
	}
	_ = op.Close()

	// And the sanity arm: the same stream WITHOUT the out-of-order tail
	// is accepted and produces one row per key run.
	op2 := &aggregateOp{plan: &final, child: &rowsOp{rows: []Row{mk(1, 3), mk(2, 5)}}, schema: final.Output()}
	if err := op2.Open(ctx); err != nil {
		t.Fatalf("sorted stream rejected: %v", err)
	}
	var n int
	for {
		_, err := op2.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("sorted stream emitted %d rows, want 2", n)
	}
	_ = op2.Close()
}
