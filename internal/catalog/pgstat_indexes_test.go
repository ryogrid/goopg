package catalog

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPGStatIndexesRowsBasicShape pins the pg_stat_all_indexes/user/sys row set
// built by PGStatIndexesRowsForDBOid: an index on a user table is emitted with
// real relid/indexrelid/schemaname/relname/indexrelname and every untracked
// pgstat scan counter a faithful 0 / NULL. M0122-0003.
func TestPGStatIndexesRowsBasicShape(t *testing.T) {
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

	rows := c.PGStatIndexesRowsForDBOid(DefaultDBOid, StatScopeAll)
	var got []string
	for _, r := range rows {
		if r[4] == "widgets_pkey" {
			got = r
			break
		}
	}
	if got == nil {
		t.Fatalf("widgets_pkey not found in pg_stat_all_indexes rows (%d rows)", len(rows))
	}
	if len(got) != 9 {
		t.Fatalf("row width = %d, want 9", len(got))
	}
	// relid(0) = parent table OID, indexrelid(1) = index OID.
	if got[1] == "0" || got[1] != strconv.Itoa(int(idx.OID)) {
		t.Errorf("indexrelid = %q, want index OID %d", got[1], idx.OID)
	}
	if got[0] != strconv.Itoa(int(tbl.OID)) {
		t.Errorf("relid = %q, want table OID %d", got[0], tbl.OID)
	}
	// schemaname(2) = parent table schema, relname(3) = table, indexrelname(4).
	if got[2] != "public" {
		t.Errorf("schemaname = %q, want public", got[2])
	}
	if got[3] != "widgets" {
		t.Errorf("relname = %q, want widgets", got[3])
	}
	// idx_scan(5) / idx_tup_read(7) / idx_tup_fetch(8) are faithful 0.
	for _, i := range []int{5, 7, 8} {
		if got[i] != "0" {
			t.Errorf("column %d = %q, want 0 (untracked)", i, got[i])
		}
	}
	// last_idx_scan(6) is NULL.
	if got[6] != VirtualNull {
		t.Errorf("last_idx_scan = %q, want NULL sentinel", got[6])
	}
}

// TestPGStatIndexesScopeFilter confirms a user-schema index appears in
// pg_stat_all_indexes and pg_stat_user_indexes but not pg_stat_sys_indexes,
// matching upstream's schemaname WHERE split (keyed on the parent table's
// schema). M0122-0003.
func TestPGStatIndexesScopeFilter(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "gadgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "gadgets_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	has := func(scope StatTableScope) bool {
		for _, r := range c.PGStatIndexesRowsForDBOid(DefaultDBOid, scope) {
			if r[4] == "gadgets_id_idx" {
				return true
			}
		}
		return false
	}
	if !has(StatScopeAll) {
		t.Error("gadgets_id_idx missing from pg_stat_all_indexes")
	}
	if !has(StatScopeUser) {
		t.Error("gadgets_id_idx missing from pg_stat_user_indexes")
	}
	if has(StatScopeSys) {
		t.Error("gadgets_id_idx must not appear in pg_stat_sys_indexes (public is a user schema)")
	}
}
