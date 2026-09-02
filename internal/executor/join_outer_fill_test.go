package executor

// M0127-P4.2 — hash-join outer fill (design leftdeep-joins/07 §3).
//
// The tests below are written against an ORACLE rather than against hand-listed
// expectations wherever they can be: goopg already had a correct RIGHT/FULL
// implementation in runNestedLoop, and it is the reference the hash path has to
// reproduce. A hand-written expectation only proves the author and the code
// agree; the nested-loop comparison proves the new path agrees with the
// semantics goopg already shipped.
//
// The cases that cannot use the oracle are the ones about HOW the fill is
// produced rather than what it produces — the per-batch sweep, and the NULL-key
// build rows that never enter a bucket at all.

import (
	"fmt"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// outerFillPlan builds a two-column-per-side equi-join of the given type on
// (l0 = r0). Predicate carries the equality as well as the keys, because the
// nested-loop oracle has only the Predicate to work from and the hash path must
// reach the identical answer whether or not the residual is redundant.
func outerFillPlan(jt optimizer.JoinType, algo optimizer.JoinAlgo, leftWidth, estLeft, estRight int) *optimizer.Join {
	col := func(idx int) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	return &optimizer.Join{
		Type:      jt,
		Algo:      algo,
		LeftKey:   col(0),
		RightKey:  col(leftWidth),
		Predicate: &optimizer.BinaryOp{Op: parser.OpEq, Left: col(0), Right: col(leftWidth)},
		Left:      valuesNode(estLeft),
		Right:     valuesNode(estRight),
	}
}

// runOuterFillJoin is runBatchJoin without the batch-state return: the same
// open/drain/close, so both arms of every comparison below render rows
// identically.
func runOuterFillJoin(t *testing.T, plan *optimizer.Join, leftRows, rightRows []Row, lw, rw int, workMem int64) []string {
	t.Helper()
	out, _ := runBatchJoin(t, plan, leftRows, rightRows, lw, rw, workMem)
	return out
}

// keyedRows builds [key, payload] rows for the given keys.
func keyedRows(tag string, keys ...int64) []Row {
	rows := make([]Row, len(keys))
	for i, k := range keys {
		rows[i] = Row{NewIntDatum(k), NewStringDatum(fmt.Sprintf("%s%d", tag, i))}
	}
	return rows
}

// assertSameMultiset compares two already-sorted renderings.
func assertSameMultiset(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, oracle produced %d\n got: %v\nwant: %v",
			what, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: row %d = %q, oracle produced %q\n got: %v\nwant: %v",
				what, i, got[i], want[i], got, want)
		}
	}
}

// The core semantic claim: for every outer join type and both build
// orientations, the hash path emits exactly what the nested-loop path emits.
// The fixture deliberately contains left-only keys, right-only keys, a
// duplicated key on each side (so the fill decision is per ROW, not per key) and
// a key that matches many-to-many.
func TestHashOuterFillMatchesNestedLoopOracle(t *testing.T) {
	const lw, rw = 2, 2
	left := keyedRows("l", 1, 2, 2, 3, 7)
	right := keyedRows("r", 2, 3, 3, 4, 9)

	cases := []struct {
		name      string
		jt        optimizer.JoinType
		buildLeft bool
	}{
		{"left/build-right", optimizer.JoinTypeLeft, false},
		{"left/build-left", optimizer.JoinTypeLeft, true},
		{"right/build-left", optimizer.JoinTypeRight, true},
		{"right/build-right", optimizer.JoinTypeRight, false},
		{"full/build-right", optimizer.JoinTypeFull, false},
		{"full/build-left", optimizer.JoinTypeFull, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oracle := outerFillPlan(tc.jt, optimizer.JoinAlgoNestedLoop, lw, len(left), len(right))
			want := runOuterFillJoin(t, oracle, left, right, lw, rw, 0)
			if len(want) == 0 {
				t.Fatalf("precondition: the nested-loop oracle emitted nothing")
			}
			hash := outerFillPlan(tc.jt, optimizer.JoinAlgoHash, lw, len(left), len(right))
			hash.BuildLeft = tc.buildLeft
			got := runOuterFillJoin(t, hash, left, right, lw, rw, 0)
			assertSameMultiset(t, "hash vs nested-loop", got, want)
		})
	}
}

// A hash-key hit that the residual predicate rejects is NOT a match. Both fill
// halves have to agree with that: the probe row must still be null-padded, and
// the build row must still be swept. This is why the bitmap is written after the
// residual check rather than at bucket-lookup time — writing it earlier passes
// every test above and silently drops these rows.
func TestHashOuterFillResidualRejectionStillFills(t *testing.T) {
	const lw, rw = 2, 2
	col := func(idx int) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	// Key 5 hash-matches, but the residual `l1 = r1` (the payload columns,
	// which never agree here) rejects every candidate pair.
	left := []Row{{NewIntDatum(5), NewIntDatum(100)}}
	right := []Row{{NewIntDatum(5), NewIntDatum(200)}}
	residual := &optimizer.BinaryOp{
		Op: parser.OpAnd,
		Left: &optimizer.BinaryOp{
			Op: parser.OpEq, Left: col(0), Right: col(lw),
		},
		Right: &optimizer.BinaryOp{
			Op: parser.OpEq, Left: col(1), Right: col(lw + 1),
		},
	}
	for _, tc := range []struct {
		name string
		jt   optimizer.JoinType
		want []string
	}{
		{"left", optimizer.JoinTypeLeft, []string{"5|100||"}},
		{"right", optimizer.JoinTypeRight, []string{"||5|200"}},
		{"full", optimizer.JoinTypeFull, []string{"5|100||", "||5|200"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := outerFillPlan(tc.jt, optimizer.JoinAlgoHash, lw, 1, 1)
			plan.Predicate = residual
			if tc.jt == optimizer.JoinTypeRight {
				plan.BuildLeft = true
			}
			got := runOuterFillJoin(t, plan, left, right, lw, rw, 0)
			sort.Strings(tc.want)
			assertSameMultiset(t, "residual-rejected fill", got, tc.want)
		})
	}
}

// A build row whose join key is NULL matches nothing by definition, which is
// exactly why RIGHT/FULL must emit it. goopg's build loops drop such rows before
// they reach a bucket (a NULL key has no canonical form to key on), so they are
// retained separately and swept after the last batch — PG reaches the same place
// by inserting them into the table itself under `hashtable->keepNulls`.
func TestHashOuterFillEmitsNullKeyedBuildRows(t *testing.T) {
	const lw, rw = 2, 2
	left := keyedRows("l", 1, 2)
	right := []Row{
		{NewIntDatum(2), NewStringDatum("r-match")},
		{NullDatum, NewStringDatum("r-nullkey")},
	}
	plan := outerFillPlan(optimizer.JoinTypeFull, optimizer.JoinAlgoHash, lw, len(left), len(right))
	got := runOuterFillJoin(t, plan, left, right, lw, rw, 0)
	// datumToString renders a NULL as an empty field, so a null-padded side
	// shows up as consecutive separators.
	want := []string{
		"1|l0||",         // unmatched probe row
		"2|l1|2|r-match", // the one real match
		"|||r-nullkey",   // the NULL-keyed build row, swept
	}
	sort.Strings(want)
	assertSameMultiset(t, "null-keyed build fill", got, want)
	// And the nested-loop oracle agrees, which is the point: dropping the row
	// would have been a silent divergence from goopg's own semantics.
	oracle := outerFillPlan(optimizer.JoinTypeFull, optimizer.JoinAlgoNestedLoop, lw, len(left), len(right))
	assertSameMultiset(t, "null-keyed build fill vs oracle", got,
		runOuterFillJoin(t, oracle, left, right, lw, rw, 0))
}

// The sweep is per BATCH, and that is the part a single-batch test cannot see:
// an implementation that swept only the last resident table would pass every
// case above and lose every unmatched build row of every earlier batch. Same
// identity assertion the P3.2 spill tests use — spilled answer == memory answer
// — applied to the two join types that have a sweep to lose.
func TestHashOuterFillSweepsEveryBatch(t *testing.T) {
	const lw, rw = 2, 2
	// Disjoint key spaces on purpose: with no key in common EVERY build row is
	// unmatched, so the whole output is sweep output and a lost batch is a
	// missing row rather than a missing duplicate.
	build := intKeyRows(4000, 700, "b")
	probe := make([]Row, 0, 3000)
	for _, r := range intKeyRows(3000, 700, "p") {
		k, _ := datumToInt64Key(r[0])
		probe = append(probe, Row{NewIntDatum(k + 10_000), r[1]})
	}
	for _, jt := range []optimizer.JoinType{optimizer.JoinTypeRight, optimizer.JoinTypeFull} {
		name := "right"
		if jt == optimizer.JoinTypeFull {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			plan := outerFillPlan(jt, optimizer.JoinAlgoHash, lw, len(probe), len(build))
			// Build on the right for both, so RIGHT exercises the sweep too
			// (the planner's own RIGHT orientation probes the preserved side
			// and is covered by the oracle test above).
			want, memBS := runBatchJoin(t, plan, probe, build, lw, rw, unboundedWorkMem)
			if memBS != nil && memBS.innerSpilled != 0 {
				t.Fatalf("precondition: the unbounded arm spilled %d rows", memBS.innerSpilled)
			}
			if len(want) == 0 {
				t.Fatalf("precondition: the in-memory join emitted nothing")
			}
			got, bs := runBatchJoin(t, plan, probe, build, lw, rw, 256*1024)
			if bs == nil || bs.nbatch <= 1 {
				t.Fatalf("precondition: the join did not batch (nbatch=%v)", bs)
			}
			assertSameMultiset(t, fmt.Sprintf("%s spilled vs memory", name), got, want)
		})
	}
}

// A batch holding ONLY build rows must not be skipped when the build side
// fills: every row in it is unmatched, and skipping it drops exactly those rows.
// This is the inner arm of PG's rule 1 (nodeHashjoin.c ExecHashJoinNewBatch),
// and the mirror of the outer arm P3.2 added for LEFT/ANTI.
func TestBuildOnlyBatchIsNotSkippedWhenBuildFills(t *testing.T) {
	for _, tc := range []struct {
		name string
		jt   optimizer.JoinType
		bl   bool
		want bool // skippable
	}{
		{"inner", optimizer.JoinTypeInner, false, true},
		{"left/build-right", optimizer.JoinTypeLeft, false, true},
		{"left/build-left", optimizer.JoinTypeLeft, true, false},
		{"right/build-right", optimizer.JoinTypeRight, false, false},
		{"right/build-left", optimizer.JoinTypeRight, true, true},
		{"full", optimizer.JoinTypeFull, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := outerFillPlan(tc.jt, optimizer.JoinAlgoHash, 2, 1, 1)
			plan.BuildLeft = tc.bl
			o := &joinOp{plan: plan}
			bs := &hashBatchState{
				nbatch: 2, origNBatch: 2, nbatchOutstart: 2,
				inner: []*joinBatchFile{nil, {}},
				outer: []*joinBatchFile{nil, nil},
			}
			if got := bs.batchSkippable(o, 1); got != tc.want {
				t.Fatalf("batchSkippable = %v, want %v", got, tc.want)
			}
		})
	}
}

// The planner's half of 07 §3: RIGHT and FULL are no longer pinned to merge.
// Asserted on the executor's own view of the plan (which side fills) rather than
// on the algorithm alone, because "hash join with the fill on the wrong side" is
// the failure this pin was protecting against.
func TestOuterFillSidesFollowTypeAndOrientation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		jt               optimizer.JoinType
		bl               bool
		probe, buildFill bool
	}{
		{"inner", optimizer.JoinTypeInner, false, false, false},
		{"left/build-right", optimizer.JoinTypeLeft, false, true, false},
		{"left/build-left", optimizer.JoinTypeLeft, true, false, true},
		{"right/build-left", optimizer.JoinTypeRight, true, true, false},
		{"right/build-right", optimizer.JoinTypeRight, false, false, true},
		{"full", optimizer.JoinTypeFull, false, true, true},
		// Semi/Anti are build-right by contract whatever the flag says, so a
		// stray BuildLeft must not turn them into filling joins.
		{"semi/stray-build-left", optimizer.JoinTypeSemi, true, false, false},
		{"anti/stray-build-left", optimizer.JoinTypeAnti, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := outerFillPlan(tc.jt, optimizer.JoinAlgoHash, 2, 1, 1)
			plan.BuildLeft = tc.bl
			o := &joinOp{plan: plan}
			if got := o.fillProbeSide(); got != tc.probe {
				t.Fatalf("fillProbeSide = %v, want %v", got, tc.probe)
			}
			if got := o.fillBuildSide(); got != tc.buildFill {
				t.Fatalf("fillBuildSide = %v, want %v", got, tc.buildFill)
			}
		})
	}
}
