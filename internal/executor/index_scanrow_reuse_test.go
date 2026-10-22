package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestIndexScanKeepsScanRowAcrossReopen pins M0122-0012 slice 36: a nested
// loop closes and re-opens its inner index probe for every outer row, so
// indexScanOp.Close keeps its decode buffer and the next Open/Next reuses it
// instead of allocating a fresh full-width row per probe. The second probe's
// values must still be its own (the buffer is overwritten, not stale).
func TestIndexScanKeepsScanRowAcrossReopen(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE isr (k int NOT NULL, a text, b int)",
		"INSERT INTO isr SELECT g, 'v' || g, g * 10 FROM generate_series(1, 2000) g",
		"CREATE INDEX isr_k ON isr (k)",
		"ANALYZE isr",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableSeqScan, ps.EnableBitmapScan = false, false
	probe := func(k string) *indexScanOp {
		t.Helper()
		ctx.CommandCounterIncrement()
		ctx.CmdID = ctx.GetCurrentCommandId(true)
		stmts, err := parser.Parse("SELECT k, a, b FROM isr WHERE k = " + k)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatal(err)
		}
		var is *optimizer.IndexScan
		for n := plan; n != nil && is == nil; {
			switch x := n.(type) {
			case *optimizer.IndexScan:
				is = x
			case *optimizer.Project:
				n = x.Child
			case *optimizer.Filter:
				n = x.Child
			default:
				t.Fatalf("no IndexScan under %T", n)
			}
		}
		return newIndexScanOp(is)
	}
	op := probe("7")
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	slot, err := op.Next()
	if err != nil {
		t.Fatal(err)
	}
	first := slotRow(slot)
	if got := first[2].Int; got != 70 {
		t.Fatalf("first probe b = %d, want 70", got)
	}
	buf := &op.scanRow[0]
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
	if op.scanRow == nil || &op.scanRow[0] != buf {
		t.Fatal("Close dropped the decode buffer: every re-opened probe would allocate a new row")
	}
	// Re-open the same probe; its key is a constant, so it returns the same
	// row — decoded again into the kept buffer.
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	slot, err = op.Next()
	if err != nil {
		t.Fatal(err)
	}
	again := slotRow(slot)
	if &op.scanRow[0] != buf {
		t.Fatal("re-opened probe decoded into a new buffer")
	}
	if again[0].Int != 7 || again[1].StringValue() != "v7" || again[2].Int != 70 {
		t.Fatalf("re-opened probe row = %v, want (7, v7, 70)", again)
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
}
