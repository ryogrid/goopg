package executor

// M0142-0005a: fused NestedLoopIndexJoin worker semantics — the shape PG's
// try_partial_nestloop_path builds when its inner is the cheapest
// parameterized path, INCLUDING the get_memoize_path variant
// (joinpath.c:2194-2199). goopg's memoized NLI is a fused
// *NestedLoopIndexJoin carrying the cache as the InnerMemo FIELD (never a
// *Join{Right: *Memoize} — createPlan panics on a free-standing
// PathMemoize), so the claim walks need their own sibling arms that
// descend the OUTER literally and never touch the re-probed inner.
//
// These tests pin the arm guard-for-guard with the planner's
// NestedLoopIndexJoinIsPartialCapable: INNER only, bare keyed equality
// probe (no SAOP, no range bounds, no bitmap), nil-plan/nil-state fail
// closed. The serial-vs-parallel identity gate lives with the plan-flip
// verification (TPC-DS Q34/Q73 on the SF0.25 sweep); what these tests
// protect is the claim topology: one partitioned outer, one re-probed
// inner per worker, no claim state crossing the boundary.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// nliProbePlan builds the bare parameterized equality probe
// lateralProbeIsPartialProbe admits — the same shape
// parallel_lateral_probe_test.go uses for the decomposed twin.
func nliProbePlan() *optimizer.IndexScan {
	return &optimizer.IndexScan{Index: &catalog.Index{},
		Key: &optimizer.OuterColumnRef{Level: 1, Index: 1}}
}

// nliBitmapProbePlan builds the bitmap-probe inner createNestLoopBitmapJoinPlan
// emits (M0146-0005 slice 27): a *BitmapHeapScan over exactly one
// *BitmapIndexScan carrying bound probe keys.
func nliBitmapProbePlan() *optimizer.BitmapHeapScan {
	return &optimizer.BitmapHeapScan{
		Outer: &optimizer.BitmapIndexScan{Index: &catalog.Index{},
			Key: &optimizer.OuterColumnRef{Level: 1, Index: 1}},
	}
}

// nliPlan builds a fused NLI plan: INNER over a SeqScan outer and a bare
// keyed probe inner. memo=true adds the InnerMemo wrapper — the field
// aliases the same *IndexScan, exactly as createNestLoopPlan's fused arm
// emits it.
func nliPlan(typ optimizer.JoinType, inner optimizer.Node, memo bool) *optimizer.NestedLoopIndexJoin {
	nli := &optimizer.NestedLoopIndexJoin{
		Type: typ, Outer: &optimizer.SeqScan{}, Inner: inner,
	}
	if memo {
		is, ok := inner.(*optimizer.IndexScan)
		if !ok {
			panic("nliPlan: InnerMemo aliases an *IndexScan only")
		}
		nli.InnerMemo = &optimizer.Memoize{Child: is}
	}
	return nli
}

// TestParallelNLIWalkerAdmission pins the approval: a capable fused NLI
// attaches on all three claim walks, the outer's scan takes the claim,
// and the probe inner carries none — memoized or not.
func TestParallelNLIWalkerAdmission(t *testing.T) {
	for _, memo := range []bool{false, true} {
		name := "bare"
		if memo {
			name = "memoized"
		}
		t.Run(name, func(t *testing.T) {
			op := &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &seqScanOp{},
			}
			if !attachParallelScan(op, newParallelScanState(0)) {
				t.Fatal("approved NLI must attach (sequential)")
			}
			if op.outer.(*seqScanOp).pscan == nil {
				t.Error("outer scan must take the claim")
			}

			op = &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &bitmapHeapScanOp{},
			}
			if !attachParallelBitmapScan(op, newParallelBitmapState()) {
				t.Fatal("approved NLI must attach (bitmap walk reaches outer)")
			}
			if op.outer.(*bitmapHeapScanOp).pbm == nil {
				t.Error("outer bitmap must take the claim")
			}

			op = &nestedLoopIndexJoinOp{
				plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), memo),
				outer: &indexOnlyScanOp{},
			}
			if !attachParallelIndexScan(op, newParallelIndexScanState()) {
				t.Fatal("approved NLI must attach (index walk reaches outer)")
			}
			if op.outer.(*indexOnlyScanOp).pidx == nil {
				t.Error("outer index scan must take the claim")
			}
		})
	}

	// M0146-0005 slice 27: the bitmap-probe inner attaches identically —
	// the claim lands on the OUTER; the inner is a per-worker serial
	// re-probe that takes no claim state of any kind.
	for _, jt := range []optimizer.JoinType{optimizer.JoinTypeInner, optimizer.JoinTypeSemi, optimizer.JoinTypeAnti} {
		op := &nestedLoopIndexJoinOp{
			plan:  nliPlan(jt, nliBitmapProbePlan(), false),
			outer: &seqScanOp{},
			inner: &bitmapHeapScanOp{},
		}
		if !attachParallelScan(op, newParallelScanState(0)) {
			t.Errorf("%v bitmap-inner NLI: sequential walk must attach", jt)
		}
		if op.outer.(*seqScanOp).pscan == nil {
			t.Errorf("%v bitmap-inner NLI: outer scan must take the claim", jt)
		}
	}
}

// TestParallelNLIWalkerRefusals pins the refusal matrix on all three
// walks: every non-INNER jointype outside the probe set, a bitmap inner
// missing its probe shape, an unkeyed inner, an SAOP probe, a
// range-bounded probe, nil plan, nil state.
func TestParallelNLIWalkerRefusals(t *testing.T) {
	bitmapInner := &optimizer.BitmapHeapScan{}
	saopInner := &optimizer.IndexScan{Index: &catalog.Index{},
		Key:      &optimizer.OuterColumnRef{Level: 1, Index: 1},
		SAOPKeys: []optimizer.Expr{&optimizer.OuterColumnRef{Level: 1, Index: 2}}}
	unkeyedInner := &optimizer.IndexScan{Index: &catalog.Index{}}
	rangedInner := &optimizer.IndexScan{Index: &catalog.Index{},
		Key:    &optimizer.OuterColumnRef{Level: 1, Index: 1},
		LowKey: &optimizer.NumericConst{Value: "1"}}

	refusals := map[string]*optimizer.NestedLoopIndexJoin{
		"right": nliPlan(optimizer.JoinTypeRight, nliProbePlan(), false),
		"full":  nliPlan(optimizer.JoinTypeFull, nliProbePlan(), false),
		// SEMI is ADMITTED since M0137-0019b (asserted below); ANTI and
		// LEFT joined it for M0145-0010 after their capability was verified
		// by TestParallelNLIJointypeIdentity. What remains refused is the
		// set with no worker-local per-outer-row verdict.
		"cross": nliPlan(optimizer.JoinTypeCross, nliProbePlan(), false),
		// A bare *BitmapHeapScan (no index child, no keys) is not a probe;
		// the shaped probe from nliBitmapProbePlan is admitted and pinned
		// in TestParallelNLIWalkerAdmission. The malformed forms — And/Or
		// outer tree, keyless index child — stay refused.
		"bitmap":           nliPlan(optimizer.JoinTypeInner, bitmapInner, false),
		"bitmap-and-outer": nliPlan(optimizer.JoinTypeInner, &optimizer.BitmapHeapScan{Outer: &optimizer.BitmapAnd{}}, false),
		"bitmap-keyless": nliPlan(optimizer.JoinTypeInner, &optimizer.BitmapHeapScan{
			Outer: &optimizer.BitmapIndexScan{Index: &catalog.Index{}},
		}, false),
		"saop":    nliPlan(optimizer.JoinTypeInner, saopInner, false),
		"unkeyed": nliPlan(optimizer.JoinTypeInner, unkeyedInner, false),
		"ranged":  nliPlan(optimizer.JoinTypeInner, rangedInner, false),
	}
	for name, plan := range refusals {
		op := &nestedLoopIndexJoinOp{plan: plan, outer: &seqScanOp{}}
		if attachParallelScan(op, newParallelScanState(0)) {
			t.Errorf("%s: sequential walk must refuse", name)
		}
		op = &nestedLoopIndexJoinOp{plan: plan, outer: &bitmapHeapScanOp{}}
		if attachParallelBitmapScan(op, newParallelBitmapState()) {
			t.Errorf("%s: bitmap walk must refuse", name)
		}
		op = &nestedLoopIndexJoinOp{plan: plan, outer: &indexOnlyScanOp{}}
		if attachParallelIndexScan(op, newParallelIndexScanState()) {
			t.Errorf("%s: index walk must refuse", name)
		}
	}
	// M0137-0019b: a SEMI NLI over an admitted probe must be ACCEPTED by
	// every walk, or the partial path its producer files would be costed
	// and then refused at the Gather. TPC-H Q4 is the consumer.
	semiPlan := nliPlan(optimizer.JoinTypeSemi, nliProbePlan(), false)
	if !attachParallelScan(&nestedLoopIndexJoinOp{plan: semiPlan, outer: &seqScanOp{}}, newParallelScanState(0)) {
		t.Error("semi: sequential walk must accept (M0137-0019b)")
	}
	if !attachParallelBitmapScan(&nestedLoopIndexJoinOp{plan: semiPlan, outer: &bitmapHeapScanOp{}}, newParallelBitmapState()) {
		t.Error("semi: bitmap walk must accept (M0137-0019b)")
	}
	if !attachParallelIndexScan(&nestedLoopIndexJoinOp{plan: semiPlan, outer: &indexOnlyScanOp{}}, newParallelIndexScanState()) {
		t.Error("semi: index walk must accept (M0137-0019b)")
	}

	// Nil plan fails closed on every walk.
	nilOp := &nestedLoopIndexJoinOp{outer: &seqScanOp{}}
	if attachParallelScan(nilOp, newParallelScanState(0)) {
		t.Error("nil plan: sequential walk must refuse")
	}
	if attachParallelBitmapScan(nilOp, newParallelBitmapState()) {
		t.Error("nil plan: bitmap walk must refuse")
	}
	if attachParallelIndexScan(nilOp, newParallelIndexScanState()) {
		t.Error("nil plan: index walk must refuse")
	}
	// Nil state fails closed on the approved shape.
	ok := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: &seqScanOp{},
	}
	if attachParallelScan(ok, nil) {
		t.Error("nil state must refuse")
	}
}

// TestCollectShareableJoinsDescendsNLI pins the prebuild agreement: a hash
// join below an approved NLI's outer is COLLECTED for leader prebuild;
// under a refused shape nothing is — matching HasShareableHashJoin's
// plan-side verdict so the promise never exceeds the collection.
func TestCollectShareableJoinsDescendsNLI(t *testing.T) {
	hash := &joinOp{plan: &optimizer.Join{Algo: optimizer.JoinAlgoHash, Type: optimizer.JoinTypeInner}}
	op := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: hash,
	}
	var got []*joinOp
	collectShareableJoins(op, &got)
	if len(got) != 1 || got[0] != hash {
		t.Fatalf("approved NLI: collected %d joins, want the outer hash", len(got))
	}

	refused := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeCross, nliProbePlan(), false),
		outer: hash,
	}
	got = got[:0]
	collectShareableJoins(refused, &got)
	if len(got) != 0 {
		t.Fatalf("refused NLI: collected %d joins, want none", len(got))
	}
}

// TestCollectBitmapScansDescendsNLI pins the bitmap-prebuild mirror: a
// driving bitmap under the approved outer is found; under a refused
// shape the collection is empty (the subtree never reaches workers).
func TestCollectBitmapScansDescendsNLI(t *testing.T) {
	bm := &bitmapHeapScanOp{}
	op := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliProbePlan(), false),
		outer: bm,
	}
	var got []*bitmapHeapScanOp
	collectBitmapScans(op, &got)
	if len(got) != 1 || got[0] != bm {
		t.Fatalf("approved NLI: collected %d bitmaps, want the outer one", len(got))
	}

	// ANTI, not SEMI: M0137-0019b admitted SEMI, so the refusal case has to
	// be a jointype that is still out of the set.
	refused := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeCross, nliProbePlan(), false),
		outer: bm,
	}
	got = got[:0]
	collectBitmapScans(refused, &got)
	if len(got) != 0 {
		t.Fatalf("refused NLI: collected %d bitmaps, want none", len(got))
	}

	// M0146-0005 slice 27 — the pin that makes the bitmap inner safe: the
	// collection walk descends the OUTER only, so an approved bitmap-probe
	// inner is never collected and can never be mistaken for the driving
	// bitmap prebuildBitmap would publish. With a bitmap inner AND a
	// bitmap outer, exactly the outer is collected.
	innerBM := &bitmapHeapScanOp{}
	outerBM := &bitmapHeapScanOp{}
	bitmapProbe := &nestedLoopIndexJoinOp{
		plan:  nliPlan(optimizer.JoinTypeInner, nliBitmapProbePlan(), false),
		outer: outerBM,
		inner: innerBM,
	}
	got = got[:0]
	collectBitmapScans(bitmapProbe, &got)
	if len(got) != 1 || got[0] != outerBM {
		t.Fatalf("bitmap-inner NLI: collected %v, want exactly the OUTER driving bitmap", got)
	}
	if innerBM.pbm != nil {
		t.Fatal("the inner re-probe must never take bitmap claim state")
	}
}

// M0146-0004 — the serial-vs-parallel identity pin for the memoized fused
// NLI under a Gather. The claim-topology pins above prove WHICH scan takes
// the claim; this pin proves the whole shape returns identical rows: each
// worker partitions the outer, re-opens the probe per its own outer rows,
// and caches in a PRIVATE memoizeOp/kvcache — PG's per-worker MemoizeState
// (nodeMemoize.c:1190-1260, the DSM shuttles only instrumentation). The
// fixture's key stream has 4 rows per key, so repeats for one key can
// split across workers — each worker's first sight of that key is a miss,
// and correctness must hold anyway.
func TestParallelNLIMemoizeIdentity(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	const sql = "SELECT mo.pad, mi.v FROM mo JOIN mi ON mi.id = mo.k"

	want := sortedRowStrings(t, ctx, sql)
	if len(want) == 0 {
		t.Fatal("fixture produced no rows; the comparison would be vacuous")
	}

	// The plan must be the fused memoized shape — a bare fused NLI or a
	// decomposed join means the fixture no longer exercises this family
	// (and a hand-wrapped Gather over it would measure the wrong thing).
	stmts0, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan0, err := optimizer.Plan(stmts0[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	nli := findNLI(plan0)
	if nli == nil || nli.InnerMemo == nil {
		t.Fatalf("fixture no longer plans a memoized fused NLI (InnerMemo=%v)",
			nli != nil && nli.InnerMemo != nil)
	}
	if !optimizer.NestedLoopIndexJoinIsPartialCapable(nli) {
		t.Fatal("fixture's fused NLI is not partial-capable; a hand-wrapped " +
			"Gather would run the whole plan per participant")
	}

	for _, workers := range []int{1, 2, 4} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		gathered := optimizer.NewGather(0, plan, workers)
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		op, err := Build(gathered)
		if err != nil {
			t.Fatalf("workers=%d build: %v", workers, err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatalf("workers=%d open: %v", workers, err)
		}
		var got []string
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				t.Fatalf("workers=%d next: %v", workers, err)
			}
			r := slot.Row()
			cells := make([]string, len(r))
			for i, d := range r {
				if d.IsNull() {
					cells[i] = "NULL"
				} else {
					cells[i] = fmt.Sprint(d.Int)
				}
			}
			got = append(got, strings.Join(cells, ","))
			slot.Release()
		}
		op.Close()
		sort.Strings(got)
		if diff := multisetDiff(got, want); diff != "" {
			t.Errorf("workers=%d: %s", workers, diff)
		}
	}
}

// findNLI locates the fused *NestedLoopIndexJoin under the transparent
// wrappers (Project/Filter) a plain join select may carry.
func findNLI(n optimizer.Node) *optimizer.NestedLoopIndexJoin {
	switch x := n.(type) {
	case *optimizer.NestedLoopIndexJoin:
		return x
	case *optimizer.Project:
		return findNLI(x.Child)
	case *optimizer.Filter:
		return findNLI(x.Child)
	}
	return nil
}

// multisetDiff reports the first count mismatch between two sorted
// multisets, "" when identical.
func multisetDiff(got, want []string) string {
	if len(got) != len(want) {
		return fmt.Sprintf("row count %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Sprintf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
	return ""
}

// M0146-0005 slice 27 — the serial-vs-parallel identity pin for the
// bitmap-probe fused NLI under a Gather, TPC-DS Q55's shape (`Parallel Seq
// Scan -> Nested Loop -> Bitmap Heap Scan` per worker). The walker pins
// above prove WHICH scan takes the claim; this pin proves the whole shape
// returns identical rows: each worker partitions the outer, re-opens the
// bitmap probe per its own outer rows, and builds a PRIVATE TIDBitmap per
// rescan (bitmapHeapScanOp.Rescan -> tbm=nil -> lazy rebuild). The fixture
// gives the inner ~2000 rows per key, which is what makes the planner elect
// a bitmap probe over an index probe at all — the same election PG makes
// for Q55's ss_item_sk probe (~480 rows/key).
func newBitmapNLIFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE nlib_outer (id int, k int)",
		"CREATE TABLE nlib_inner (k int, v int)",
		"CREATE INDEX nlib_inner_k ON nlib_inner (k)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	// 24 outer rows over 40 keys — a small outer is what lets a nested
	// loop beat hashing the whole inner.
	for i := 0; i < 24; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO nlib_outer VALUES (%d, %d)", i, i%40)); err != nil {
			cleanup()
			t.Fatalf("insert outer: %v", err)
		}
	}
	// 40 keys x 500 rows: each parameterized probe returns ~500 rows —
	// the fan-out where a bitmap scan beats the index probe per rescan
	// (Q55's ss probe returns ~480 rows/key).
	if err := runDDL(t, ctx, "INSERT INTO nlib_inner SELECT g % 40, g FROM generate_series(1, 20000) g"); err != nil {
		cleanup()
		t.Fatalf("insert inner: %v", err)
	}
	// The in-process ANALYZE is a no-op; seed the stats it would record so
	// the probe election sees the real fan-out.
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "nlib_outer"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 24, Columns: []catalog.ColumnStats{
			{NDistinct: 24}, {NDistinct: 40},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "nlib_inner"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 20000, Columns: []catalog.ColumnStats{
			{NDistinct: 40}, {NDistinct: 20000},
		}}
	}
	return ctx, cleanup
}

func TestParallelNLIBitmapInnerIdentity(t *testing.T) {
	ctx, cleanup := newBitmapNLIFixture(t)
	defer cleanup()

	const sql = "SELECT o.id, i.v FROM nlib_outer o JOIN nlib_inner i ON i.k = o.k"

	want := sortedRowStrings(t, ctx, sql)
	if len(want) == 0 {
		t.Fatal("fixture produced no rows; the comparison would be vacuous")
	}

	// The plan must be the fused NLI over a BITMAP inner — an index probe
	// or a decomposed join means the fixture no longer exercises this
	// family (and a hand-wrapped Gather over it would measure the wrong
	// thing). Hash and merge join are disabled for the plan under test so
	// the election is between the probe shapes, not between algorithms —
	// the same knob measurement PG's own force_parallel_max regress setup
	// uses. The serial baseline runs with defaults, so it may legitimately
	// take a hash plan; only the row SET must agree.
	nloOnly := optimizer.DefaultPlannerSettings()
	nloOnly.EnableHashJoin = false
	nloOnly.EnableMergeJoin = false
	stmts0, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan0, err := optimizer.PlanWithSettings(stmts0[0], ctx.Catalog, nloOnly)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	nli := findNLI(plan0)
	if nli == nil {
		t.Fatal("fixture no longer plans a fused NLI")
	}
	if _, ok := nli.Inner.(*optimizer.BitmapHeapScan); !ok {
		t.Fatalf("fixture's NLI inner is %T, want *BitmapHeapScan — the probe election changed", nli.Inner)
	}
	if !optimizer.NestedLoopIndexJoinIsPartialCapable(nli) {
		t.Fatal("fixture's fused NLI is not partial-capable; a hand-wrapped " +
			"Gather would run the whole plan per participant")
	}

	for _, workers := range []int{1, 2, 4} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, nloOnly)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		gathered := optimizer.NewGather(0, plan, workers)
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		op, err := Build(gathered)
		if err != nil {
			t.Fatalf("workers=%d build: %v", workers, err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatalf("workers=%d open: %v", workers, err)
		}
		var got []string
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				t.Fatalf("workers=%d next: %v", workers, err)
			}
			r := slot.Row()
			cells := make([]string, len(r))
			for i, d := range r {
				if d.IsNull() {
					cells[i] = "NULL"
				} else {
					cells[i] = fmt.Sprint(d.Int)
				}
			}
			got = append(got, strings.Join(cells, ","))
			slot.Release()
		}
		op.Close()
		sort.Strings(got)
		if diff := multisetDiff(got, want); diff != "" {
			t.Errorf("workers=%d: %s", workers, diff)
		}
	}
}
