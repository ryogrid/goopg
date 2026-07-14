package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPGStatTablesRowsBasicShape pins the pg_stat_all_tables/user/sys row set
// built by PGStatTablesRowsForDBOid: a user table is emitted with real
// relid/schemaname/relname, n_live_tup backed by the ANALYZE reltuples estimate,
// and every untracked pgstat counter a faithful 0 / NULL. M0122-0003.
func TestPGStatTablesRowsBasicShape(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "widgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &TableStats{RowCount: 42, Pages: 3}

	rows := c.PGStatTablesRowsForDBOid(DefaultDBOid, StatScopeAll)
	var got []string
	for _, r := range rows {
		if r[2] == "widgets" {
			got = r
			break
		}
	}
	if got == nil {
		t.Fatalf("widgets not found in pg_stat_all_tables rows (%d rows)", len(rows))
	}
	if len(got) != 26 {
		t.Fatalf("row width = %d, want 26", len(got))
	}
	// relid(0) is the table OID, schemaname(1) is public, relname(2) is widgets.
	if got[1] != "public" {
		t.Errorf("schemaname = %q, want public", got[1])
	}
	// n_live_tup(14) is the reltuples estimate.
	if got[14] != "42" {
		t.Errorf("n_live_tup = %q, want 42", got[14])
	}
	// Untracked integer counters are faithful 0.
	for _, idx := range []int{3, 5, 6, 8, 9, 10, 11, 12, 13, 15, 16, 17, 22, 23, 24, 25} {
		if got[idx] != "0" {
			t.Errorf("column %d = %q, want 0 (untracked)", idx, got[idx])
		}
	}
	// last_* timestamps are NULL.
	for _, idx := range []int{4, 7, 18, 19, 20, 21} {
		if got[idx] != VirtualNull {
			t.Errorf("column %d = %q, want NULL sentinel", idx, got[idx])
		}
	}
}

// TestPGStatTablesScopeFilter confirms a user-schema table appears in
// pg_stat_all_tables and pg_stat_user_tables but not pg_stat_sys_tables, matching
// upstream's schemaname WHERE split. M0122-0003.
func TestPGStatTablesScopeFilter(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "gadgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	has := func(scope StatTableScope) bool {
		for _, r := range c.PGStatTablesRowsForDBOid(DefaultDBOid, scope) {
			if r[2] == "gadgets" {
				return true
			}
		}
		return false
	}
	if !has(StatScopeAll) {
		t.Error("gadgets missing from pg_stat_all_tables")
	}
	if !has(StatScopeUser) {
		t.Error("gadgets missing from pg_stat_user_tables")
	}
	if has(StatScopeSys) {
		t.Error("gadgets must not appear in pg_stat_sys_tables (public is a user schema)")
	}
}

// TestPGStatTablesExcludesNonTableRelkinds confirms sequences and system-catalog
// virtual tables are not emitted (pg_stat_all_tables filters relkind IN
// ('r','t','m','p') and goopg's system catalogs are storage-less). M0122-0003.
func TestPGStatTablesExcludesNonTableRelkinds(t *testing.T) {
	c := NewInMemory()
	rows := c.PGStatTablesRowsForDBOid(DefaultDBOid, StatScopeAll)
	for _, r := range rows {
		switch r[2] {
		case "pg_class", "pg_attribute", "pg_stat_io", "pg_stat_all_tables":
			t.Errorf("system-catalog virtual table %q leaked into pg_stat_all_tables", r[2])
		}
	}
}
