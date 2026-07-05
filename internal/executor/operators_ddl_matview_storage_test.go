package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDropMaterializedViewReleasesStorage pins the physical-storage-leak fix
// deferred by the M0119-0004 view-restart-persistence loop (deferral_ledger.md
// row for "DROP MATERIALIZED VIEW still leaks the matview's physical heap file
// on disk"): unlike DROP TABLE (dropTableByRefImmediate), execDropOneMatView
// only removed the catalog entry and the on-disk pg_class/pg_attribute rows —
// it never called Pool.Manager().DropRelation/InvalidateRel or FSM/VM
// DropRelation for the matview's own main-fork file, so every DROP MATERIALIZED
// VIEW leaked one heap file forever.
func TestDropMaterializedViewReleasesStorage(t *testing.T) {
	ctx, cat, cleanup := newDDLFixtureWithFSMVM(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE src (id int, val int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO src VALUES (1, 10), (2, 20)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE MATERIALIZED VIEW mv_src AS SELECT id, val FROM src"); err != nil {
		t.Fatalf("CREATE MATERIALIZED VIEW: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "mv_src"})
	if !ok {
		t.Fatal("mv_src not found in catalog")
	}
	rel := cat.RelFileNode(tbl)

	if !ctx.Pool.Manager().Exists(rel) {
		t.Fatal("pre-drop: mv_src's heap file does not exist on disk (test setup broken)")
	}

	if err := runDDL(t, ctx, "DROP MATERIALIZED VIEW mv_src"); err != nil {
		t.Fatalf("DROP MATERIALIZED VIEW: %v", err)
	}

	if ctx.Pool.Manager().Exists(rel) {
		t.Error("mv_src's heap file still exists on disk after DROP MATERIALIZED VIEW — physical-storage leak")
	}
	if blk, ok := ctx.FSM.GetPageWithFreeSpace(rel, 1); ok {
		t.Errorf("FSM still answers GetPageWithFreeSpace post-drop (blk=%d) — DropRelation not called", blk)
	}
	if ctx.VM.AllVisible(rel, 0) {
		t.Error("VM.AllVisible reports true post-drop — VM.DropRelation not called")
	}
}
