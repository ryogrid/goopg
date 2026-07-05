package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestMatViewSurvivesRestartViaWAL pins the M0119-0004 pg_dump-parity follow-up:
// before this fix, a CREATE MATERIALIZED VIEW reverted to a plain table after a
// restart — buildUserPGClassRow always wrote pg_class.relkind='r', and
// loadUserTablesFromHeap only recognized relkind='r', so the reloaded relation
// had IsMatView=false and View=nil (the defining query was lost entirely).
//
// Flow:
//  1. Init + Open + CREATE TABLE + CREATE MATERIALIZED VIEW + Close (no
//     SaveCatalog, simulating a crash).
//  2. Re-Open: loadUserTablesFromHeap recognizes relkind='m' →
//     replayMatViewRecords re-parses the defining query from WAL.
//  3. Assert the reloaded relation is still a matview with its query intact.
func TestMatViewSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE src (id int4 NOT NULL, val int4)")
	runDDL(t, rt1, "CREATE MATERIALIZED VIEW mv_src AS SELECT id, val FROM src WHERE val > 5")
	// Note: NO SaveCatalog call here — simulating a crash.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "mv_src"})
	if !ok {
		t.Fatal("mv_src not found after restart — matview heap recovery failed")
	}
	if !tbl.IsMatView {
		t.Error("mv_src reloaded as a plain table (IsMatView=false) — relkind/loadUserTablesFromHeap regression")
	}
	if !tbl.IsPopulated {
		t.Error("mv_src lost its IsPopulated flag across restart")
	}
	if tbl.View == nil {
		t.Fatal("mv_src.View is nil after restart — WAL query replay failed")
	}
	if len(tbl.Columns) != 2 || tbl.Columns[0].Name != "id" || tbl.Columns[1].Name != "val" {
		t.Errorf("mv_src.Columns = %+v, want [id val]", tbl.Columns)
	}
}

// TestMatViewWithNoDataSurvivesRestartViaWAL pins WITH NO DATA's IsPopulated=false
// across a restart.
func TestMatViewWithNoDataSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE src2 (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE MATERIALIZED VIEW mv_empty AS SELECT id FROM src2 WITH NO DATA")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "mv_empty"})
	if !ok {
		t.Fatal("mv_empty not found after restart")
	}
	if !tbl.IsMatView {
		t.Error("mv_empty reloaded as a plain table")
	}
	if tbl.IsPopulated {
		t.Error("mv_empty.IsPopulated=true after restart, want false (WITH NO DATA)")
	}
	if tbl.View == nil {
		t.Fatal("mv_empty.View is nil after restart")
	}
}
