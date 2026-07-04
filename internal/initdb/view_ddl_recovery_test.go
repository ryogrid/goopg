package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestViewSurvivesRestartViaWAL pins the M0119-0004 pg_dump-parity follow-up
// (sibling of TestMatViewSurvivesRestartViaWAL): before this fix, a plain
// CREATE VIEW had zero on-disk persistence at all — execCreateView never
// called syncTableToCatalogHeap — so it simply ceased to exist after a
// restart (LookupTable returned not-found), unlike a matview which at least
// downgraded to a plain table.
//
// Flow:
//  1. Init + Open + CREATE TABLE + CREATE VIEW + Close (no SaveCatalog,
//     simulating a crash).
//  2. Re-Open: loadUserTablesFromHeap recognizes relkind='v' → replayViewRecords
//     re-parses the defining query from WAL.
//  3. Assert the reloaded relation is still a view with its query intact.
func TestViewSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE v_src (id int4 NOT NULL, val int4)")
	runDDL(t, rt1, "CREATE VIEW v_src_view AS SELECT id, val FROM v_src WHERE val > 5")
	// Note: NO SaveCatalog call here — simulating a crash.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "v_src_view"})
	if !ok {
		t.Fatal("v_src_view not found after restart — view heap recovery failed")
	}
	if tbl.IsMatView {
		t.Error("v_src_view reloaded as a matview (IsMatView=true)")
	}
	if !tbl.Virtual {
		t.Error("v_src_view.Virtual=false after restart — a plain view has no physical storage")
	}
	if tbl.View == nil {
		t.Fatal("v_src_view.View is nil after restart — WAL query replay failed")
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0].Name != "id" || tbl.Columns[1].Name != "val" {
		t.Errorf("v_src_view.Columns = %+v, want [id val]", tbl.Columns)
	}
}

// TestViewOrReplaceSurvivesRestartWithoutDuplicate pins the CREATE OR REPLACE
// VIEW OID-churn cleanup: catalog.InMemory.CreateView always assigns a fresh
// OID, even on replace, so the old view's pg_class/pg_attribute rows must be
// stamped deleted or a restart would register the view twice (once under the
// stale OID, once under the new one).
func TestViewOrReplaceSurvivesRestartWithoutDuplicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE v_or_src (id int4 NOT NULL, val int4)")
	runDDL(t, rt1, "CREATE VIEW v_or_view AS SELECT id FROM v_or_src")
	runDDL(t, rt1, "CREATE OR REPLACE VIEW v_or_view AS SELECT id, val FROM v_or_src WHERE val > 1")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "v_or_view"})
	if !ok {
		t.Fatal("v_or_view not found after restart")
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0].Name != "id" || tbl.Columns[1].Name != "val" {
		t.Errorf("v_or_view.Columns = %+v, want [id val] (should reflect the REPLACE body, not the original)", tbl.Columns)
	}
}

// TestDropViewNotResurrectedAfterRestart pins the drop-side cleanup: DROP
// VIEW must stamp xmax on the pg_class/pg_attribute rows CREATE VIEW wrote,
// or a restart resurrects the dropped view (loadUserTablesFromHeap would
// still see its never-deleted heap rows).
func TestDropViewNotResurrectedAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE v_drop_src (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE VIEW v_dropped AS SELECT id FROM v_drop_src")
	runDDL(t, rt1, "DROP VIEW v_dropped")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "v_dropped"}); ok {
		t.Fatal("v_dropped resurrected after restart — DROP VIEW heap cleanup failed")
	}
}

// TestDropMatViewNotResurrectedAfterRestart is the sibling of
// TestDropViewNotResurrectedAfterRestart for materialized views: the earlier
// M0119-0004 matview-persistence loop landed CREATE's heap sync but never
// wired DROP's cleanup (replayMatViewRecords's own "matview dropped since the
// snapshot" comment anticipated this but nothing ever stamped the xmax).
func TestDropMatViewNotResurrectedAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE mv_drop_src (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE MATERIALIZED VIEW mv_dropped AS SELECT id FROM mv_drop_src")
	runDDL(t, rt1, "DROP MATERIALIZED VIEW mv_dropped")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "mv_dropped"}); ok {
		t.Fatal("mv_dropped resurrected after restart — DROP MATERIALIZED VIEW heap cleanup failed")
	}
}
