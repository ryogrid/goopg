package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgStatioUserTablesEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_statio_user_tables resolves as a virtual view, the
// pg_statio_*_tables valuesOp branch fires, and the connecting database's own user
// table (items) is projected with real schemaname/relname and honest-0 block
// counters (goopg has no per-relation buffer-pool counters). M0122-0003.
func TestPgStatioUserTablesEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, relname, heap_blks_read, heap_blks_hit, idx_blks_read FROM pg_statio_user_tables WHERE relname = 'items'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items)", len(rows))
	}
	if got := rows[0][0].Format(); got != "public" {
		t.Errorf("schemaname = %q, want public", got)
	}
	if got := rows[0][1].Format(); got != "items" {
		t.Errorf("relname = %q, want items", got)
	}
	// heap/idx block counters are honest integer 0 (no per-relation buffer tracking).
	for i := 2; i <= 4; i++ {
		if rows[0][i].IsNull() {
			t.Errorf("block counter col %d is NULL, want integer 0", i)
		}
		if got := rows[0][i].Format(); got != "0" {
			t.Errorf("block counter col %d = %q, want 0", i, got)
		}
	}
}

// TestPgStatioUserIndexesEndToEnd confirms pg_statio_user_indexes resolves and an
// index on the connecting database's own user table projects real
// relname/indexrelname with honest-0 block counters. M0122-0003.
func TestPgStatioUserIndexesEndToEnd(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	im := cat.(*catalog.InMemory)
	if _, err := im.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT schemaname, relname, indexrelname, idx_blks_read, idx_blks_hit FROM pg_statio_user_indexes WHERE indexrelname = 'items_id_idx'")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (items_id_idx)", len(rows))
	}
	if got := rows[0][1].Format(); got != "items" {
		t.Errorf("relname = %q, want items", got)
	}
	if got := rows[0][2].Format(); got != "items_id_idx" {
		t.Errorf("indexrelname = %q, want items_id_idx", got)
	}
	for i := 3; i <= 4; i++ {
		if got := rows[0][i].Format(); got != "0" {
			t.Errorf("block counter col %d = %q, want 0", i, got)
		}
	}
}

// TestPgStatioSysTablesExcludesUserTable confirms the schemaname split: a
// public-schema user table appears in pg_statio_user_tables but not
// pg_statio_sys_tables. M0122-0003.
func TestPgStatioSysTablesExcludesUserTable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT relname FROM pg_statio_sys_tables WHERE relname = 'items'")
	if len(rows) != 0 {
		t.Fatalf("pg_statio_sys_tables returned %d rows for user table items, want 0", len(rows))
	}
}
