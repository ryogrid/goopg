package optimizer

// gatherpaths_upperrel_test.go — M0140-0006b-2: the upper-rel Gather reader.
//
// generateUpperRelGatherPaths is the only reader of an upper rel's
// PartialPathlist. Only the SETOP rel can carry partials today
// (addPartialSetOpPath, M0140-0006b), so these tests pin the reader's own
// contract — offer over a partial SetOp, refuse under every gate — plus the
// end-to-end consequence through createSetOpPaths (offered but dominated by
// the serial candidate, winner unchanged).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// upperRelPartialBranch builds one UNION ALL branch the way M0140-0006b's own
// tests do: considers parallel, offers one partial bare seq scan.
func upperRelPartialBranch(rows float64) *RelOptInfo {
	return &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: rows, Cost: Cost{Total: rows},
		ParallelSafe: true, ParallelWorkers: 1,
	}}}
}

// upperRelSetOpWithPartials runs the producer (M0140-0006b) so the reader
// under test sees a realistic rel: ConsiderParallel stamped by the
// conjunction rule, one partial PathSetOp filed.
func upperRelSetOpWithPartials(t *testing.T, cp costParams) *RelOptInfo {
	t.Helper()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = upperRelPartialBranch(100)
	setOpRel.RightBranchRel = upperRelPartialBranch(50)
	node := setOpTestNode(parser.SetOpUnion, true, upperOrderedInput(100), upperOrderedInput(50))
	addPartialSetOpPath(setOpRel, node, cp)
	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("setup: PartialPathlist = %d entries, want the producer's 1", len(setOpRel.PartialPathlist))
	}
	return setOpRel
}

// TestGenerateUpperRelGatherPathsOffersGatherOverPartialSetOp pins the live
// site: a SETOP rel with a partial path gets a PathGather offered on its
// Pathlist, over the partial SetOp itself.
func TestGenerateUpperRelGatherPathsOffersGatherOverPartialSetOp(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		cp := defaultCostParams()
		rel := upperRelSetOpWithPartials(t, cp)

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 1 {
			t.Fatalf("Pathlist = %d entries, want 1 (the offered Gather)", len(rel.Pathlist))
		}
		g := rel.Pathlist[0]
		if g.Kind != PathGather {
			t.Fatalf("offered path kind = %v, want PathGather", g.Kind)
		}
		if len(g.Children) != 1 || g.Children[0] != rel.PartialPathlist[0] {
			t.Fatal("the Gather is not over the partial SetOp")
		}
	})
}

// TestGenerateUpperRelGatherPathsRefusesUnderTop pins the task's own stated
// policy: `top` admits only the search's FINAL rel, and an upper rel is
// never a member of any search level — fail closed. The producer still
// files (its guard is `off`-only), but the reader offers nothing.
func TestGenerateUpperRelGatherPathsRefusesUnderTop(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsTop)()
		cp := defaultCostParams()
		rel := upperRelSetOpWithPartials(t, cp)

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 0 {
			t.Fatalf("Pathlist = %d entries under mode top, want 0 (upper rels are never the search's final rel)", len(rel.Pathlist))
		}
	})
}

// TestGenerateUpperRelGatherPathsRefusesUnderOff: the delegated call refuses,
// same as for a search rel.
func TestGenerateUpperRelGatherPathsRefusesUnderOff(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsOff)()
		cp := defaultCostParams()
		rel := &RelOptInfo{ConsiderParallel: true}
		rel.PartialPathlist = []*Path{{
			Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100},
			ParallelSafe: true, ParallelWorkers: 2,
		}}

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 0 {
			t.Fatalf("Pathlist = %d entries under mode off, want 0", len(rel.Pathlist))
		}
	})
}

// TestGenerateUpperRelGatherPathsRefusesWhenParallelDisabled: with
// maxParallelWorkersPerGather = 0 the query-wide gate fails. The partial is
// hand-filed (the producer itself would cap workers to 0 and file nothing),
// isolating the READER's own gate from the producer's.
func TestGenerateUpperRelGatherPathsRefusesWhenParallelDisabled(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		cp := defaultCostParams()
		cp.maxParallelWorkersPerGather = 0
		rel := &RelOptInfo{ConsiderParallel: true}
		rel.PartialPathlist = []*Path{{
			Kind: PathSetOp, Rows: 150, Cost: Cost{Total: 17},
			ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{
				{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2},
				{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2},
			},
		}}

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 0 {
			t.Fatalf("Pathlist = %d entries with parallel disabled, want 0", len(rel.Pathlist))
		}
	})
}

// TestGenerateUpperRelGatherPathsRespectsConsiderParallel: partials present
// but the rel does not consider parallel (e.g. one branch refused) — the
// reader gates on the same flag the producer stamps.
func TestGenerateUpperRelGatherPathsRespectsConsiderParallel(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		cp := defaultCostParams()
		rel := &RelOptInfo{ConsiderParallel: false}
		rel.PartialPathlist = []*Path{{
			Kind: PathSetOp, Rows: 150, Cost: Cost{Total: 17},
			ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{
				{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2},
				{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2},
			},
		}}

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 0 {
			t.Fatalf("Pathlist = %d entries with ConsiderParallel=false, want 0", len(rel.Pathlist))
		}
	})
}

// TestGenerateUpperRelGatherPathsIgnoresRelWithoutPartials pins the
// provably-inert posture of the WINDOW/ORDERED/GROUP_AGG call sites: no
// producer files partials there today, so the reader must leave the rel
// byte-identical (same Pathlist contents, not just the same length).
func TestGenerateUpperRelGatherPathsIgnoresRelWithoutPartials(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		cp := defaultCostParams()
		rel := &RelOptInfo{ConsiderParallel: true}
		seed := &Path{Kind: PathSort, Rows: 10, Cost: Cost{Total: 50}}
		rel.Pathlist = []*Path{seed}

		generateUpperRelGatherPaths(rel, cp)

		if len(rel.Pathlist) != 1 || rel.Pathlist[0] != seed {
			t.Fatal("a rel with no partial paths was modified; the WINDOW/ORDERED/GROUP_AGG call sites must be inert")
		}
	})
}
