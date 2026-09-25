package executor

// M0141-S7-exec-b: end-to-end structural + semantic reachability for
// *optimizer.IncrementalSort — the correctness gate fix_plan.md's own
// entry names before GOOPG_INCREMENTAL_SORT can ever be pointed at the
// corpus. exec-a's own tests (operators_incremental_sort_test.go) construct
// incrementalSortOp directly with zero createPlanNode/Plan() callers; this
// file is the sibling that proves the node reaches BOTH executor.go builder
// sites (classic `buildNode` and `BuildFast`'s slab tree, which falls
// through to the `opAdapter` default arm — the same non-migrated-operator
// path *optimizer.Distinct/Window/SetOp already use, so no dedicated
// OpIncrementalSort slab kind is needed) and the four mechanical tree-
// walkers (scan_deform.go's deformBoundBelow/deformSideWidth, subplan.go's
// classifySubPlan, operators_cte_dml.go's planContainsWorkTableScan — the
// last three of which have their own direct-call unit tests alongside their
// Sort siblings; see subplan_handle_test.go and scan_deform_bound_test.go).
//
// The plan is built BY HAND (`optimizer.IncrementalSort{...}`) rather than
// through `planSelect`'s tournament: `GOOPG_INCREMENTAL_SORT` stays off by
// default (incrementalsortpaths.go), and getting a real query to WIN the
// tournament is the broader M0141-S7 measurement goal, not exec-b's own
// "does the wiring work" scope — the same staging TestBuildFastNodeKinds
// (phase_c_test.go) already uses for its own hand-built plan nodes.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// firstSort finds the first *optimizer.Sort in a plan tree, descending
// through the small set of single-child wrappers a simple
// "SELECT cols FROM t ORDER BY cols" plan can produce above it.
func firstSort(n optimizer.Node) *optimizer.Sort {
	switch x := n.(type) {
	case *optimizer.Sort:
		return x
	case *optimizer.Project:
		return firstSort(x.Child)
	case *optimizer.Filter:
		return firstSort(x.Child)
	case *optimizer.Limit:
		return firstSort(x.Child)
	default:
		return nil
	}
}

// replaceSort rebuilds n with its first *optimizer.Sort replaced by with,
// mutating the wrapper chain's Child fields in place (mirrors firstSort's
// own descent).
func replaceSort(n optimizer.Node, with optimizer.Node) optimizer.Node {
	switch x := n.(type) {
	case *optimizer.Sort:
		return with
	case *optimizer.Project:
		x.Child = replaceSort(x.Child, with)
		return x
	case *optimizer.Filter:
		x.Child = replaceSort(x.Child, with)
		return x
	case *optimizer.Limit:
		x.Child = replaceSort(x.Child, with)
		return x
	default:
		return n
	}
}

// TestIncrementalSortReachesBothBuilders is the correctness gate: a plan
// carrying a hand-built *optimizer.IncrementalSort (standing in for a
// PathIncrementalSort tournament win) must (1) execute identically to the
// PLANNER's own real Sort over the same query — the failure mode of a
// wrongly-grouped Incremental Sort is a plausible, error-free reordering,
// exactly the class "An unwinnable path is an untested path" warns about —
// and (2) agree between the classic `Build`/`Run` path and `BuildFast`/
// `RunFast`'s slab tree, so neither builder site was missed.
func TestIncrementalSortReachesBothBuilders(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE inc_items (grp int4, v int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// Inserted in ascending-grp BLOCKS, so the table's natural (insertion)
	// SeqScan order already satisfies the presorted prefix a real
	// PathIncrementalSort candidate would have proven via an ordered index
	// scan — this hand-built test asserts that invariant directly instead of
	// re-deriving it through a tournament. Each block's `v` values are
	// deliberately NOT already sorted, so a correct answer requires the
	// per-group full sort the operator performs (a naive "trust the input
	// order" bug would leave v unsorted within a group).
	for g := 0; g < 4; g++ {
		for k := 0; k < 20; k++ {
			v := (19 - k) * 7
			if err := runDDL(t, ctx, fmt.Sprintf(
				"INSERT INTO inc_items VALUES (%d, %d)", g, v)); err != nil {
				t.Fatalf("INSERT: %v", err)
			}
		}
	}

	const sql = "SELECT grp, v FROM inc_items ORDER BY grp, v"

	// Baseline: the planner's own real Sort, executed via Build+Run.
	basePlan := planOne(t, sql, cat)
	baseOp, err := Build(basePlan)
	if err != nil {
		t.Fatalf("Build(baseline): %v", err)
	}
	baseRows, err := Run(baseOp, ctx)
	if err != nil {
		t.Fatalf("Run(baseline): %v", err)
	}
	want := renderRows(baseRows)
	if len(want) != 80 {
		t.Fatalf("baseline produced %d rows, want 80", len(want))
	}

	// Swap the real Sort for a hand-built IncrementalSort over the SAME
	// child and keys, PresortedCount=1 (the leading "grp" key).
	incPlan := planOne(t, sql, cat)
	sortNode := firstSort(incPlan)
	if sortNode == nil {
		t.Fatalf("plan %T has no *optimizer.Sort node to replace — the query shape assumption behind this test no longer holds", incPlan)
	}
	if len(sortNode.Keys) != 2 {
		t.Fatalf("planner Sort has %d keys, want 2 (grp, v)", len(sortNode.Keys))
	}
	incNode := &optimizer.IncrementalSort{
		Child:          sortNode.Child,
		Keys:           sortNode.Keys,
		PresortedCount: 1,
	}
	incPlan = replaceSort(incPlan, incNode)

	// (1) Value equivalence against the real-Sort baseline, through the
	// classic builder.
	incOp, err := Build(incPlan)
	if err != nil {
		t.Fatalf("Build(incremental): %v", err)
	}
	incRows, err := Run(incOp, ctx)
	if err != nil {
		t.Fatalf("Run(incremental): %v", err)
	}
	got := renderRows(incRows)
	if len(got) != len(want) {
		t.Fatalf("incremental sort: got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("incremental sort: row %d differs:\n got %q\nwant %q", i, got[i], want[i])
		}
	}

	// (2) Build vs BuildFast agreement — neither executor.go builder site
	// was missed.
	runBothAndCompare(t, incPlan, ctx)
}

// TestExplainIncrementalSortPresortedKey is M0141-S7-exec-c's own gate: a
// hand-built *optimizer.IncrementalSort must render PG's two-line
// `Sort Key:` / `Presorted Key:` pair (nodeIncrementalSort.c's
// show_incremental_sort_keys, explain.c:2583-2823), not just the plain
// Sort's single `Sort Key:` line. Reuses the same hand-built-plan technique
// as TestIncrementalSortReachesBothBuilders above (GOOPG_INCREMENTAL_SORT
// stays off, so a real query can never plan this node itself yet).
func TestExplainIncrementalSortPresortedKey(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE inc_explain (grp int4, v int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	basePlan := planOne(t, "SELECT grp, v FROM inc_explain ORDER BY grp, v DESC", cat)
	sortNode := firstSort(basePlan)
	if sortNode == nil {
		t.Fatalf("plan %T has no *optimizer.Sort node to replace", basePlan)
	}
	if len(sortNode.Keys) != 2 {
		t.Fatalf("planner Sort has %d keys, want 2 (grp, v)", len(sortNode.Keys))
	}
	incNode := &optimizer.IncrementalSort{
		Child:          sortNode.Child,
		Keys:           sortNode.Keys,
		PresortedCount: 1,
	}
	incPlan := replaceSort(basePlan, incNode)

	ex := &optimizer.Explain{Child: incPlan, Options: parser.ExplainOptions{
		Costs: false,
		Set:   parser.ExplainOptionsSet{Costs: true},
	}}
	op, err := Build(ex)
	if err != nil {
		t.Fatalf("Build(explain): %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(explain): %v", err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain(explain): %v", err)
	}
	_ = op.Close()
	lines := renderRows(rows)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "Sort Key: grp, v DESC") {
		t.Errorf("expected `Sort Key: grp, v DESC` detail line; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Presorted Key: grp") {
		t.Errorf("expected `Presorted Key: grp` detail line (no DESC/NULLS suffix, only the first PresortedCount=1 key); got:\n%s", joined)
	}
	if strings.Contains(joined, "Presorted Key: grp DESC") || strings.Contains(joined, "Presorted Key: grp, v") {
		t.Errorf("Presorted Key must be the bare undecorated leading-prefix expression only; got:\n%s", joined)
	}
}
