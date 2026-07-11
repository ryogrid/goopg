package catalog

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPGStatioTablesRowsBasicShape pins the pg_statio_all_tables/user/sys row set
// built by PGStatioTablesRowsForDBOid: a user table is emitted with real
// relid/schemaname/relname and every untracked buffer-pool block counter a faithful
// 0 (goopg has no per-relation buffer counters). M0122-0003.
func TestPGStatioTablesRowsBasicShape(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "widgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := c.PGStatioTablesRowsForDBOid(DefaultDBOid, StatScopeAll)
	var got []string
	for _, r := range rows {
		if r[2] == "widgets" {
			got = r
			break
		}
	}
	if got == nil {
		t.Fatalf("widgets not found in pg_statio_all_tables rows (%d rows)", len(rows))
	}
	if len(got) != 11 {
		t.Fatalf("row width = %d, want 11", len(got))
	}
	if got[0] != strconv.Itoa(int(tbl.OID)) {
		t.Errorf("relid = %q, want table OID %d", got[0], tbl.OID)
	}
	if got[1] != "public" {
		t.Errorf("schemaname = %q, want public", got[1])
	}
	// All 8 block counters (cols 3..10) are a faithful 0.
	for i := 3; i <= 10; i++ {
		if got[i] != "0" {
			t.Errorf("column %d = %q, want 0 (untracked)", i, got[i])
		}
	}
}

// TestPGStatioTablesScopeFilter confirms a user-schema table appears in
// pg_statio_all_tables and pg_statio_user_tables but not pg_statio_sys_tables,
// matching upstream's schemaname WHERE split. M0122-0003.
func TestPGStatioTablesScopeFilter(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "gadgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	has := func(scope StatTableScope) bool {
		for _, r := range c.PGStatioTablesRowsForDBOid(DefaultDBOid, scope) {
			if r[2] == "gadgets" {
				return true
			}
		}
		return false
	}
	if !has(StatScopeAll) {
		t.Error("gadgets missing from pg_statio_all_tables")
	}
	if !has(StatScopeUser) {
		t.Error("gadgets missing from pg_statio_user_tables")
	}
	if has(StatScopeSys) {
		t.Error("gadgets must not appear in pg_statio_sys_tables (public is a user schema)")
	}
}

// TestPGStatioIndexesRowsBasicShape pins the pg_statio_all_indexes row set: an index
// on a user table is emitted with real relid/indexrelid/schemaname/relname/
// indexrelname and both block counters a faithful 0. M0122-0003.
func TestPGStatioIndexesRowsBasicShape(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "widgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "widgets_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}

	rows := c.PGStatioIndexesRowsForDBOid(DefaultDBOid, StatScopeAll)
	var got []string
	for _, r := range rows {
		if r[4] == "widgets_pkey" {
			got = r
			break
		}
	}
	if got == nil {
		t.Fatalf("widgets_pkey not found in pg_statio_all_indexes rows (%d rows)", len(rows))
	}
	if len(got) != 7 {
		t.Fatalf("row width = %d, want 7", len(got))
	}
	if got[0] != strconv.Itoa(int(tbl.OID)) {
		t.Errorf("relid = %q, want table OID %d", got[0], tbl.OID)
	}
	if got[1] != strconv.Itoa(int(idx.OID)) {
		t.Errorf("indexrelid = %q, want index OID %d", got[1], idx.OID)
	}
	if got[2] != "public" {
		t.Errorf("schemaname = %q, want public", got[2])
	}
	if got[3] != "widgets" {
		t.Errorf("relname = %q, want widgets", got[3])
	}
	// idx_blks_read(5) / idx_blks_hit(6) are a faithful 0.
	for _, i := range []int{5, 6} {
		if got[i] != "0" {
			t.Errorf("column %d = %q, want 0 (untracked)", i, got[i])
		}
	}
}
