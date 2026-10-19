package executor

// M0146-0228 slice 3: a Parallel Hash join whose BUILD side is a partial
// index-only scan — PG's shape for TPC-DS Q23's best_ss_customer (`Parallel
// Hash -> Parallel Index Only Scan using customer_pkey`). The build side's
// leaf claim set (hashBuildBranch) carries its own pidx, so attachAll wires
// the index-only scan to it exactly as it wires a seq scan to pscan; this pins
// that by parallel-vs-serial identity plus the published-row count, the two
// instruments that see a build side every participant read whole.

import (
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// phIndexScanOf finds the index or index-only scan a Filter/Project spine
// reads and returns a setter for its Parallel stamp and the scanned table.
func phIndexScanOf(n optimizer.Node) (stamp func(), table string, indexOnly bool) {
	for {
		switch x := n.(type) {
		case *optimizer.IndexOnlyScan:
			return func() { x.Parallel = true }, x.Table.Name, true
		case *optimizer.IndexScan:
			return func() { x.Parallel = true }, x.Table.Name, false
		case *optimizer.Filter:
			n = x.Child
		case *optimizer.Project:
			n = x.Child
		default:
			return nil, "", false
		}
	}
}

func TestParallelHashIndexOnlyBuildIdentityWithSerial(t *testing.T) {
	ctx, cleanup := parallelHashFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE INDEX ph_build_k ON ph_build (k)")
	runSQL(t, ctx, "ANALYZE ph_build")
	runSQL(t, ctx, "ANALYZE ph_probe")

	plan := func(sql string) optimizer.Node {
		t.Helper()
		advanceStmtCounter(ctx)
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableNestLoop = false
		ps.EnableMergeJoin = false
		ps.EnableSeqScan = false
		ps.EnableBitmapScan = false
		ps.MaxParallelWorkersPerGather = 0
		node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatalf("plan %q: %v", sql, err)
		}
		return node
	}

	// enable_seqscan off leaves the probe a (disabled) seq scan and makes the
	// build an index-only scan of ph_build_k — one whose range covers every
	// leaf block (Q23's customer_pkey build reads the whole index), and a
	// ranged one.
	for _, tc := range []struct{ sql, buildRows string }{
		{"SELECT p.w, b.k FROM ph_probe p JOIN ph_build b ON p.k = b.k WHERE b.k >= 0",
			"SELECT count(*) FROM ph_build WHERE k IS NOT NULL"},
		{"SELECT p.w, b.k FROM ph_probe p JOIN ph_build b ON p.k = b.k WHERE b.k < 700",
			"SELECT count(*) FROM ph_build WHERE k < 700"},
		{"SELECT p.w FROM ph_probe p WHERE NOT EXISTS (SELECT 1 FROM ph_build b WHERE b.k = p.k AND b.k < 700)",
			"SELECT count(*) FROM ph_build WHERE k < 700"},
	} {
		sql := tc.sql
		wantBuildRows := renderRows(runSQL(t, ctx, tc.buildRows))[0]
		t.Run(sql, func(t *testing.T) {
			want := c19fRun(t, ctx, plan(sql))
			root := plan(sql)
			j := c19fFindJoin(root)
			if j == nil || j.Algo != optimizer.JoinAlgoHash {
				t.Fatalf("the serial plan has no hash join: %T", root)
			}
			probe, build := j.Left, j.Right
			if !probeSideIsLeft(j) {
				probe, build = j.Right, j.Left
			}
			stampBuild, buildTable, indexOnly := phIndexScanOf(build)
			if stampBuild == nil || buildTable != "ph_build" || !indexOnly {
				t.Fatalf("the build side is not an index-only scan of ph_build (%T over %q, indexOnly=%v)", build, buildTable, indexOnly)
			}
			stampProbe, _, _ := phIndexScanOf(probe)
			if stampProbe == nil {
				if s := c19fScanOf(probe); s != nil {
					stampProbe = func() { s.Parallel = true }
				} else {
					t.Fatalf("the probe side has no partial-capable scan: %T", probe)
				}
			}
			j.ParallelHash = true
			stampBuild()
			stampProbe()
			par := optimizer.NewGather(0, root, 4)

			var ph *parallelHashBuild
			var once sync.Once
			ctx.parallelHashObserver = func(got *parallelHashBuild) { once.Do(func() { ph = got }) }
			defer func() { ctx.parallelHashObserver = nil }()

			got := c19fRun(t, ctx, par)
			if len(got) != len(want) {
				t.Fatalf("parallel hash returned %d rows, serial %d — the index-only build was read whole by each participant, or partially", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("row %d differs: parallel %q, serial %q", i, got[i], want[i])
				}
			}
			if ph == nil {
				t.Fatal("no participant reached the Parallel Hash build state; the join ran as an ordinary hash join")
			}
			if got := renderRows([]Row{{NewIntDatum(int64(ph.rowsPublished))}})[0]; got != wantBuildRows {
				t.Fatalf("participants published %s build rows, the index holds %s non-NULL keys", got, wantBuildRows)
			}
			t.Logf("builders=%d rowsPublished=%d", ph.builders, ph.rowsPublished)
		})
	}
}
