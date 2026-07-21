package planner

// P6 of docs/design/parallel-query/ — Gather insertion.
//
// This is the first stage whose behaviour every user sees, so the tests are
// weighted toward the two properties that would be silently wrong: the pass
// must never mutate a shared plan, and the safety refusals must actually
// refuse.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func parallelTestSettings() ParallelSettings {
	return ParallelSettings{
		MaxWorkersPerGather: 2,
		MinTableScanBlocks:  1024,
		DebugParallelQuery:  "off",
	}
}

// bigTable returns a table with enough statistics to clear the size gate.
func bigTable(t *testing.T, name string) *catalog.Table {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1024 blocks at the assumed 60 rows/block ⇒ well past the threshold.
	tbl.Stats = &catalog.TableStats{RowCount: 1_000_000}
	return tbl
}

func seqScanOver(tbl *catalog.Table) *SeqScan {
	return &SeqScan{Table: tbl, schema: Schema{{Name: "a"}}}
}

// ── the non-mutating property ───────────────────────────────────────────────

// TestMaybeAddGatherIsNonMutating is the most important test in this file.
//
// The plan the post-pass receives comes from a process-wide, cross-session
// cache: the same node is referenced by every session running that SQL,
// concurrently. Editing any node in place would be a data race that race-gate
// catches only under load — and a wrong plan for whoever else holds it.
func TestMaybeAddGatherIsNonMutating(t *testing.T) {
	tbl := bigTable(t, "big")
	scan := seqScanOver(tbl)
	filter := &Filter{Child: scan}
	root := &Project{Child: filter, schema: filter.Output()}

	// Capture the original wiring.
	origRootChild := root.Child
	origFilterChild := filter.Child

	out := MaybeAddGather(root, parallelTestSettings())

	if out == root {
		t.Fatal("expected a Gather to be inserted for a large relation")
	}
	// The ORIGINAL tree must be untouched: same nodes, same children.
	if root.Child != origRootChild {
		t.Error("post-pass mutated the original root's child — the cached plan is shared")
	}
	if filter.Child != origFilterChild {
		t.Error("post-pass mutated a node below the root")
	}
	// And the original must still be a runnable serial plan.
	if _, isGather := root.Child.(*Gather); isGather {
		t.Error("a Gather was spliced into the shared original")
	}
}

// TestMaybeAddGatherSharesUntouchedSubtrees pins the other half: only the
// spine is copied. Copying the whole tree would work but would defeat the
// point of caching a plan at all.
func TestMaybeAddGatherSharesUntouchedSubtrees(t *testing.T) {
	tbl := bigTable(t, "big")
	scan := seqScanOver(tbl)
	root := &Project{Child: scan, schema: scan.Output()}

	out := MaybeAddGather(root, parallelTestSettings())

	// The Gather lands ABOVE the Project, not below it: the partial subtree is
	// kept as large as the terminating nodes allow, so projection happens in
	// the workers. That is also what PG does — its Parallel Seq Scan carries
	// the targetlist, with Gather above.
	g, ok := out.(*Gather)
	if !ok {
		t.Fatalf("expected the Gather at the root, got %T", out)
	}
	// And the whole partial subtree is SHARED by pointer, not deep-copied —
	// copying it would work but would defeat the point of caching a plan.
	if g.Child != Node(root) {
		t.Error("the partial subtree should be shared by pointer")
	}
	if root.Child != Node(scan) {
		t.Error("the shared subtree was rewritten")
	}
}

// ── the safety refusals ─────────────────────────────────────────────────────

func TestMaybeAddGatherRefusals(t *testing.T) {
	tbl := bigTable(t, "big")

	tempTbl := bigTable(t, "tmp")
	tempTbl.Temp = true

	virtualTbl := bigTable(t, "virt")
	virtualTbl.Virtual = true

	for _, tc := range []struct {
		name string
		root Node
		set  ParallelSettings
	}{
		{
			name: "DML never parallelises",
			root: &Update{Table: tbl, Child: seqScanOver(tbl)},
			set:  parallelTestSettings(),
		},
		{
			name: "DELETE never parallelises",
			root: &Delete{Table: tbl, Child: seqScanOver(tbl)},
			set:  parallelTestSettings(),
		},
		{
			name: "SELECT FOR UPDATE (row marks) never parallelises",
			root: &LockRows{Child: seqScanOver(tbl)},
			set:  parallelTestSettings(),
		},
		{
			name: "temp tables are refused",
			root: &Project{Child: seqScanOver(tempTbl), schema: Schema{{Name: "a"}}},
			set:  parallelTestSettings(),
		},
		{
			name: "virtual catalog relations are refused",
			root: &Project{Child: seqScanOver(virtualTbl), schema: Schema{{Name: "a"}}},
			set:  parallelTestSettings(),
		},
		{
			name: "SERIALIZABLE suppresses parallelism",
			root: &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}},
			set: func() ParallelSettings {
				s := parallelTestSettings()
				s.IsSerializable = true
				return s
			}(),
		},
		{
			name: "max_parallel_workers_per_gather = 0 disables",
			root: &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}},
			set: func() ParallelSettings {
				s := parallelTestSettings()
				s.MaxWorkersPerGather = 0
				return s
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := MaybeAddGather(tc.root, tc.set)
			if planHasGather(out) {
				t.Errorf("%s: a Gather was inserted anyway", tc.name)
			}
		})
	}
}

// TestMaybeAddGatherNoStatsRefuses pins the gate's default direction, which is
// the OPPOSITE of the semi/anti NLI gate's optimistic accept. Accepting
// without evidence risks spawning workers for a tiny relation; declining keeps
// today's behaviour. goopg's ANALYZE stats are in-memory and restart-lost, so
// "no stats" is a common production state, not an edge case — which is exactly
// why the choice of default has to be deliberate.
func TestMaybeAddGatherNoStatsRefuses(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "nostats"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}}
	if planHasGather(MaybeAddGather(root, parallelTestSettings())) {
		t.Error("a relation with no statistics must not get a parallel path")
	}
}

// TestMaybeAddGatherSmallRelationRefuses pins the size rule.
func TestMaybeAddGatherSmallRelationRefuses(t *testing.T) {
	tbl := bigTable(t, "small")
	tbl.Stats = &catalog.TableStats{RowCount: 100} // ≈1 block
	root := &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}}
	if planHasGather(MaybeAddGather(root, parallelTestSettings())) {
		t.Error("a relation below min_parallel_table_scan_size must stay serial")
	}
}

// TestDebugParallelQueryForcesPastSizeGate pins upstream's testing lever: it
// bypasses the SIZE gate but must NOT bypass the safety refusals.
func TestDebugParallelQueryForcesPastSizeGate(t *testing.T) {
	tbl := bigTable(t, "small")
	tbl.Stats = &catalog.TableStats{RowCount: 10}
	s := parallelTestSettings()
	s.DebugParallelQuery = "on"

	root := &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}}
	if !planHasGather(MaybeAddGather(root, s)) {
		t.Error("debug_parallel_query = on must force a Gather past the size gate")
	}

	// ...but not past safety.
	tempTbl := bigTable(t, "tmp")
	tempTbl.Temp = true
	tempRoot := &Project{Child: seqScanOver(tempTbl), schema: Schema{{Name: "a"}}}
	if planHasGather(MaybeAddGather(tempRoot, s)) {
		t.Error("debug_parallel_query must NOT override the temp-table refusal")
	}
}

// ── the worker-count rule ───────────────────────────────────────────────────

// TestComputeParallelWorkersLadder pins PG's ×3 ladder
// (compute_parallel_worker, allpaths.c): 1 worker at the threshold, +1 every
// time the size passes another factor of three.
func TestComputeParallelWorkersLadder(t *testing.T) {
	for _, tc := range []struct {
		blocks int64
		cap    int
		want   int
	}{
		{blocks: 1023, cap: 8, want: 0},  // below threshold
		{blocks: 1024, cap: 8, want: 1},  // at threshold
		{blocks: 3071, cap: 8, want: 1},  // just below 3×
		{blocks: 3072, cap: 8, want: 2},  // 3×
		{blocks: 9216, cap: 8, want: 3},  // 9×
		{blocks: 27648, cap: 8, want: 4}, // 27×
		{blocks: 27648, cap: 2, want: 2}, // clamped by the GUC
	} {
		tbl := bigTable(t, "t")
		s := parallelTestSettings()
		s.MaxWorkersPerGather = tc.cap
		s.BlocksForTable = func(*catalog.Table) (int64, bool) { return tc.blocks, true }
		got := computeParallelWorkers(seqScanOver(tbl), s)
		if got != tc.want {
			t.Errorf("%d blocks (cap %d): %d workers, want %d", tc.blocks, tc.cap, got, tc.want)
		}
	}
}

// TestParallelWorkersReloptionWins pins PG's precedence: a table's
// parallel_workers reloption overrides the size ladder entirely. goopg has
// parsed and stored this option since M0110-0001 and never read it.
func TestParallelWorkersReloptionWins(t *testing.T) {
	tbl := bigTable(t, "t")
	tbl.ParallelWorkers = 3
	tbl.ParallelWorkersSet = true
	s := parallelTestSettings()
	s.MaxWorkersPerGather = 8
	s.BlocksForTable = func(*catalog.Table) (int64, bool) { return 1, true } // tiny
	if got := computeParallelWorkers(seqScanOver(tbl), s); got != 3 {
		t.Errorf("reloption should win outright, got %d workers", got)
	}
}

// TestKillSwitchSuppressesGather pins the house-style escape hatch.
func TestKillSwitchSuppressesGather(t *testing.T) {
	SetParallelEnabled(false)
	defer SetParallelEnabled(true)
	tbl := bigTable(t, "big")
	root := &Project{Child: seqScanOver(tbl), schema: Schema{{Name: "a"}}}
	if planHasGather(MaybeAddGather(root, parallelTestSettings())) {
		t.Error("GOOPG_PARALLEL=off must suppress insertion")
	}
}

// planHasGather reports whether any node in the tree is a Gather.
func planHasGather(n Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*Gather); ok {
		return true
	}
	for _, c := range parallelChildren(n) {
		if planHasGather(c) {
			return true
		}
	}
	return false
}
