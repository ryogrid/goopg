package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgStatUserIndexesEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_user_indexes resolves as a virtual view, the
// pg_stat_*_indexes valuesOp branch fires, and an index on the connecting
// database's own user table (items) is projected with real
// relname/indexrelname/schemaname. M0122-0003.
func TestPgStatUserIndexesEndToEnd(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	im := cat.(*catalog.InMemory)
	if _, err := im.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, relname, indexrelname, idx_scan FROM pg_stat_user_indexes WHERE indexrelname = 'items_id_idx'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items_id_idx)", len(rows))
	}
	if got := rows[0][0].Format(); got != "public" {
		t.Errorf("schemaname = %q, want public", got)
	}
	if got := rows[0][1].Format(); got != "items" {
		t.Errorf("relname = %q, want items", got)
	}
	if got := rows[0][2].Format(); got != "items_id_idx" {
		t.Errorf("indexrelname = %q, want items_id_idx", got)
	}
	// idx_scan is an honest integer 0 (goopg has no live index-scan counter).
	if rows[0][3].IsNull() {
		t.Errorf("idx_scan is NULL, want an integer")
	}
}

// TestPgStatSysIndexesExcludesUserIndex confirms the schemaname split: an index
// on a public-schema user table appears in pg_stat_user_indexes but not
// pg_stat_sys_indexes. M0122-0003.
func TestPgStatSysIndexesExcludesUserIndex(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	im := cat.(*catalog.InMemory)
	if _, err := im.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT indexrelname FROM pg_stat_sys_indexes WHERE indexrelname = 'items_id_idx'")
	if len(rows) != 0 {
		t.Fatalf("pg_stat_sys_indexes returned %d rows for user index items_id_idx, want 0", len(rows))
	}
}
