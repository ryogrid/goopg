package optimizer

// E-21 Cut 1 — the one-relation path search
// (docs/design/planner-e20-e21-parallel-path-search/DESIGN.md §4 Cut 1).
//
// Two things have to hold and neither is observable from a values gate, which
// is why they are pinned here rather than measured on a corpus:
//
//  1. with `GOOPG_ONEREL_SEARCH` off the seam is INERT for a one-relation
//     statement — node and predicate come back by identity, the same rollback
//     guarantee `TestPGShapedSeamIsInertWithTheFlagOff` makes for the search
//     as a whole;
//  2. with it on, the statement reaches the path search and its base rel
//     acquires the `PartialPathlist` entry `create_plain_partial_paths`
//     (allpaths.c:806) files for every plain relation — the thing E-21 says
//     does not exist today, and the input `generateUsefulGatherPaths` reads.
//
// The second is asserted through the PRODUCTION producers, not by constructing
// a path by hand: a test that files its own partial path would pass over a
// planner that files none.

import "testing"

// oneRelSeamFixture is `SELECT … FROM t WHERE t1 > 5` as `planSelect` hands it
// to the seam: a single `*SeqScan` leaf, one binding, a one-item joinlist.
//
// `rows` is large on purpose. `computeParallelWorkerForRel` is
// `compute_parallel_worker` with `index_pages = -1` (allpaths.c:4274), and its
// `RELOPT_BASEREL` arm returns 0 outright below `min_parallel_table_scan_size`
// — so a small fixture would produce no partial path for a reason that has
// nothing to do with this cut.
func oneRelSeamFixture(rows int64) (Node, *resolveContext) {
	return seamFixture([]string{"a"}, []int64{rows})
}

// TestOneRelSearchIsInertWithTheKnobOff: the rollback arm. A one-relation
// statement must take exactly the path it took before this cut — the seam
// declines at `minSearchRels()` and returns its inputs untouched.
func TestOneRelSearchIsInertWithTheKnobOff(t *testing.T) {
	withPGShapedDP(t)
	t.Cleanup(setOneRelSearchForTest(false))

	node, ctx := oneRelSeamFixture(10_000_000)
	pred := seamLocal([]string{"a"}, 0)

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam searched a one-relation statement with GOOPG_ONEREL_SEARCH off")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
	if minSearchRels() != 2 {
		t.Fatalf("minSearchRels() = %d with the knob off, want 2", minSearchRels())
	}
}

// TestOneRelSearchAdmitsASingleTableStatement: with the knob on, the same
// statement is planned by the search. This is the whole of Cut 1's mechanism —
// everything downstream (`relfromjoinlist.go`'s `jl.nrels() == 1` carve-out and
// the one-relation protocol it runs) already existed and was simply unreachable.
func TestOneRelSearchAdmitsASingleTableStatement(t *testing.T) {
	withPGShapedDP(t)
	t.Cleanup(setOneRelSearchForTest(true))

	node, ctx := oneRelSeamFixture(10_000_000)
	pred := seamLocal([]string{"a"}, 0)

	out, _, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined a one-relation statement with GOOPG_ONEREL_SEARCH on")
	}
	if !isSearchedTree(out) {
		t.Fatalf("the seam returned %T untagged — the statement was not searched", out)
	}
	if minSearchRels() != 1 {
		t.Fatalf("minSearchRels() = %d with the knob on, want 1", minSearchRels())
	}
}

// TestOneRelSearchFilesAPartialPath is E-21's actual subject. The seam test
// above shows the statement enters the search; this one shows that entering it
// produces the partial path whose absence the row is about.
//
// It drives the SAME producers `searchOneProblem` drives, in the same order
// (`setBaseRelConsiderParallel` then `addBaseRelPartialPaths` —
// relfromjoinlist.go, PG's allpaths.c:794-795 ordering), over a one-rel
// `searchCtx`.
//
// HONEST SCOPE, so a later reader does not over-read it: this test builds the
// `searchCtx` directly and therefore passes at HEAD too. It is NOT the
// reachability pin — `TestOneRelSearchAdmitsASingleTableStatement` is, and its
// knob-off twin above is that pin's mutation check. What this one adds is the
// other half of the claim: that once a one-rel `searchCtx` exists, the
// production producers do file a partial path on it, so admitting the
// statement is sufficient and no second change is needed. Both halves are
// required and neither implies the other.
func TestOneRelSearchFilesAPartialPath(t *testing.T) {
	t.Cleanup(setOneRelSearchForTest(true))

	const rows = 10_000_000
	_, ctx := oneRelSeamFixture(rows)
	tbl := ctx.bindings[0].table
	leaf := &SeqScan{Table: tbl, Alias: "a", schema: cpjSchema("a", rfjWidth)}

	cp := defaultCostParams()
	if cp.maxParallelWorkersPerGather <= 0 {
		t.Skip("max_parallel_workers_per_gather is 0 in the default cost params")
	}
	relInfos := []baseRelInfo{{table: tbl, baseRows: rows, filteredRows: rows / 100, bindingIdx: 0}}
	s, err := buildInitialRels(ctx.bindings, []Node{leaf}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}
	if len(s.joinrels) < 2 || len(s.joinrels[1]) != 1 {
		t.Fatalf("one-rel search has joinrels %d levels, level-1 %d rels; want 2 / 1",
			len(s.joinrels), len(s.joinrels[1]))
	}
	rel := s.joinrels[1][0]
	if len(rel.PartialPathlist) != 0 {
		t.Fatalf("base rel already carries %d partial paths before the producer ran",
			len(rel.PartialPathlist))
	}

	s.setBaseRelConsiderParallel(nil)
	if !rel.ConsiderParallel {
		t.Fatal("set_rel_consider_parallel refused a plain heap relation with no quals")
	}
	s.addBaseRelPartialPaths()

	if len(rel.PartialPathlist) == 0 {
		t.Fatal("create_plain_partial_paths filed no partial path for a one-relation search — " +
			"E-21's defect, still present")
	}
	p := rel.PartialPathlist[0]
	if p.Kind != PathSeqScan {
		t.Fatalf("partial path kind %v, want a partial seq scan", p.Kind)
	}
	if p.ParallelWorkers <= 0 {
		t.Fatalf("partial path has %d workers; compute_parallel_worker must give at least one "+
			"for a relation above min_parallel_table_scan_size", p.ParallelWorkers)
	}
	if !p.ParallelSafe {
		t.Fatal("partial path is not parallel-safe; add_partial_path would refuse it")
	}
}
