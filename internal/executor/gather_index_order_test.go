package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestParallelPostPassKeepsIndexOrder pins M0146-0034: when a serial plan
// takes its ORDER BY from an index scan (no Sort), the parallel post-pass
// used to wrap the scan in a plain Gather, whose interleaved worker streams
// returned rows in scheduling order — TPC-DS SF0.25's `... ORDER BY
// d_date_sk LIMIT 1` gave a different row on half the runs. PG never claims
// pathkeys for a Gather and orders such a path with Gather Merge. The pass
// now merges on the index key columns, stays serial when it cannot (Gather
// Merge disabled), and still uses a plain Gather where nothing above relies
// on the index order.
func TestParallelPostPassKeepsIndexOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE gt (a int PRIMARY KEY, y int)")
	runSQL(t, ctx, "INSERT INTO gt SELECT g, g % 7 FROM generate_series(1, 2000) g")
	serial := optimizer.DefaultPlannerSettings()
	serial.MaxParallelWorkersPerGather = 0
	ps := optimizer.ParallelSettings{MaxWorkersPerGather: 2, DebugParallelQuery: "on", LeaderParticipates: true}

	var kinds func(n optimizer.Node, out map[string]int) map[string]int
	kinds = func(n optimizer.Node, out map[string]int) map[string]int {
		switch x := n.(type) {
		case nil:
			return out
		case *optimizer.Gather:
			out["Gather"]++
			return kinds(x.Child, out)
		case *optimizer.GatherMerge:
			out["GatherMerge"]++
			if len(x.Keys) > 0 {
				if cr, ok := x.Keys[0].Expr.(*optimizer.ColumnRef); ok {
					out["key:"+cr.Name]++
				}
			}
			return kinds(x.Child, out)
		case *optimizer.Limit:
			return kinds(x.Child, out)
		case *optimizer.Project:
			return kinds(x.Child, out)
		case *optimizer.Aggregate:
			return kinds(x.Child, out)
		}
		return out
	}

	ordered := planWithSettings(t, ctx, "SELECT a FROM gt WHERE y = 3 ORDER BY a LIMIT 1", serial)
	got := kinds(optimizer.MaybeAddGather(ordered, ps), map[string]int{})
	if got["Gather"] != 0 || got["GatherMerge"] != 1 || got["key:a"] != 1 {
		t.Errorf("ordered index scan: want one Gather Merge on a and no plain Gather, got %v", got)
	}

	noGM := ps
	noGM.DisableGatherMerge = true
	got = kinds(optimizer.MaybeAddGather(ordered, noGM), map[string]int{})
	if got["Gather"] != 0 || got["GatherMerge"] != 0 {
		t.Errorf("enable_gathermerge=off: the plan must stay serial, got %v", got)
	}

	hashed := planWithSettings(t, ctx, "SELECT y, count(*) FROM gt WHERE a > 10 GROUP BY y", serial)
	got = kinds(optimizer.MaybeAddGather(hashed, ps), map[string]int{})
	if got["GatherMerge"] != 0 {
		t.Errorf("order not relied on: no Gather Merge expected, got %v", got)
	}
}
