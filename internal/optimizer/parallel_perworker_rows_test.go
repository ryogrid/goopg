package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPostPassGatherShowsPerWorkerScanRows pins M0141-S2b-16: when the
// post-pass stamps a serial scan `Parallel`, the scan's displayed rows become
// PG's per-worker estimate, clamp_row_est(rows / get_parallel_divisor)
// (cost_seqscan's parallel arm). 719876 rows over 3 workers with the leader
// participating (divisor 3.1) is 232218, TPC-DS SF0.25 store_sales' figure
// in PG 18.3's EXPLAIN. The serial original must stay untouched: the
// post-pass runs on a plan the process-wide cache may share.
func TestPostPassGatherShowsPerWorkerScanRows(t *testing.T) {
	scan := &SeqScan{Table: &catalog.Table{Name: "store_sales"}}
	scan.setPlanCost(PlanCost{StartupCost: 0, TotalCost: 20132.76, PlanRows: 719876, PlanWidth: 428})

	out := rebuildWithGather(scan, partialTarget{node: scan}, 3, true)
	g, ok := out.(*Gather)
	if !ok {
		t.Fatalf("rebuildWithGather returned %T, want *Gather", out)
	}
	ps, ok := g.Child.(*SeqScan)
	if !ok || !ps.Parallel {
		t.Fatalf("Gather child = %T (Parallel=%v), want a stamped *SeqScan", g.Child, ok && ps.Parallel)
	}
	if got := ps.PlanRows; got != 232218 {
		t.Errorf("stamped scan PlanRows = %v, want 232218 (719876 / 3.1)", got)
	}
	if !ps.PerWorker {
		t.Errorf("stamped scan PerWorker = false, want true")
	}
	if ps.TotalCost != 20132.76 {
		t.Errorf("stamped scan TotalCost = %v, want the serial 20132.76 (rows-only rescale)", ps.TotalCost)
	}
	if scan.PlanRows != 719876 || scan.Parallel {
		t.Errorf("original scan mutated: PlanRows=%v Parallel=%v, want 719876/false", scan.PlanRows, scan.Parallel)
	}

	// Without leader participation the divisor is the worker count.
	out2 := rebuildWithGather(scan, partialTarget{node: scan}, 3, false)
	if got := out2.(*Gather).Child.(*SeqScan).PlanRows; got != clampRowEst(719876.0/3) {
		t.Errorf("leader off: PlanRows = %v, want %v", got, clampRowEst(719876.0/3))
	}
}

// TestPerWorkerDisplayRowsSkipsPartialPathScan: a scan lowered from a partial
// scan path is stamped PerWorker and must not be divided again.
func TestPerWorkerDisplayRowsSkipsPartialPathScan(t *testing.T) {
	scan := &SeqScan{Table: &catalog.Table{Name: "store_sales"}}
	stampPlanCost(scan, &Path{Kind: PathSeqScan, Rows: 232218, Cost: Cost{Total: 15258.18}, ParallelWorkers: 3})
	if !scan.PerWorker {
		t.Fatal("stampPlanCost from a partial path did not set PerWorker")
	}
	perWorkerDisplayRows(scan, 3.1)
	if scan.PlanRows != 232218 {
		t.Errorf("PlanRows = %v, want 232218 unchanged (already per worker)", scan.PlanRows)
	}
}

// TestGatherPathDivisorRecoversLeaderSetting: the divisor is read back from
// the Gather path's rows, which computeGatherRows priced.
func TestGatherPathDivisorRecoversLeaderSetting(t *testing.T) {
	sub := &Path{Rows: 232218, ParallelWorkers: 3}
	on := &Path{Rows: clampRowEst(232218 * getParallelDivisor(3, true))}
	off := &Path{Rows: clampRowEst(232218 * getParallelDivisor(3, false))}
	if got := gatherPathDivisor(on, sub); got != getParallelDivisor(3, true) {
		t.Errorf("leader on: divisor %v, want %v", got, getParallelDivisor(3, true))
	}
	if got := gatherPathDivisor(off, sub); got != getParallelDivisor(3, false) {
		t.Errorf("leader off: divisor %v, want %v", got, getParallelDivisor(3, false))
	}
}
