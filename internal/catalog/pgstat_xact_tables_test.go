package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPGStatXactTablesRowsBasicShape pins the pg_stat_xact_all_tables/user/sys row
// set built by PGStatXactTablesRowsForDBOid: a user table is emitted with real
// relid/schemaname/relname and every per-transaction delta counter a faithful 0
// (goopg has no per-xact pgstat accumulator). The xact views carry only the 12
// columns — no n_live_tup / last_* / vacuum cells the cumulative views have.
// M0122-0003.
func TestPGStatXactTablesRowsBasicShape(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "widgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Even with a live-tuple estimate, the xact view has no n_live_tup cell.
	tbl.Stats = &TableStats{RowCount: 42, Pages: 3}

	rows := c.PGStatXactTablesRowsForDBOid(DefaultDBOid, StatScopeAll)
	var got []string
	for _, r := range rows {
		if r[2] == "widgets" {
			got = r
			break
		}
	}
	if got == nil {
		t.Fatalf("widgets not found in pg_stat_xact_all_tables rows (%d rows)", len(rows))
	}
	if len(got) != 12 {
		t.Fatalf("row width = %d, want 12", len(got))
	}
	if got[1] != "public" {
		t.Errorf("schemaname = %q, want public", got[1])
	}
	if got[2] != "widgets" {
		t.Errorf("relname = %q, want widgets", got[2])
	}
	// Every per-transaction delta counter (cols 3..11) is a faithful 0.
	for idx := 3; idx < 12; idx++ {
		if got[idx] != "0" {
			t.Errorf("column %d = %q, want 0 (untracked xact delta)", idx, got[idx])
		}
	}
}

// TestPGStatXactTablesScopeFilter confirms a user-schema table appears in
// pg_stat_xact_all_tables and pg_stat_xact_user_tables but not
// pg_stat_xact_sys_tables — the same schemaname split as the cumulative views.
// M0122-0003.
func TestPGStatXactTablesScopeFilter(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "gadgets"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	has := func(scope StatTableScope) bool {
		for _, r := range c.PGStatXactTablesRowsForDBOid(DefaultDBOid, scope) {
			if r[2] == "gadgets" {
				return true
			}
		}
		return false
	}
	if !has(StatScopeAll) {
		t.Error("gadgets missing from pg_stat_xact_all_tables")
	}
	if !has(StatScopeUser) {
		t.Error("gadgets missing from pg_stat_xact_user_tables")
	}
	if has(StatScopeSys) {
		t.Error("gadgets must not appear in pg_stat_xact_sys_tables (public is a user schema)")
	}
}

// TestPGStatXactTablesExcludesNonTableRelkinds confirms sequences and
// system-catalog virtual tables are not emitted — the identical relation filter as
// the cumulative pg_stat_*_tables views. M0122-0003.
func TestPGStatXactTablesExcludesNonTableRelkinds(t *testing.T) {
	c := NewInMemory()
	rows := c.PGStatXactTablesRowsForDBOid(DefaultDBOid, StatScopeAll)
	for _, r := range rows {
		switch r[2] {
		case "pg_class", "pg_attribute", "pg_stat_io", "pg_stat_xact_all_tables":
			t.Errorf("system-catalog virtual table %q leaked into pg_stat_xact_all_tables", r[2])
		}
	}
}

// TestPGStatXactTablesViewsRegistered confirms the three xact table views are
// registered as virtual system tables with the 12-column tupledesc. M0122-0003.
func TestPGStatXactTablesViewsRegistered(t *testing.T) {
	c := NewInMemory()
	for _, name := range []string{
		"pg_stat_xact_all_tables",
		"pg_stat_xact_sys_tables",
		"pg_stat_xact_user_tables",
	} {
		tbl := c.ns(DefaultDBOid).tables["pg_catalog."+name]
		if tbl == nil {
			t.Fatalf("%s not registered", name)
		}
		if len(tbl.Columns) != 12 {
			t.Errorf("%s: column count = %d, want 12", name, len(tbl.Columns))
		}
		if tbl.Columns[0].Name != "relid" || tbl.Columns[11].Name != "n_tup_newpage_upd" {
			t.Errorf("%s: unexpected tupledesc (first=%q last=%q)",
				name, tbl.Columns[0].Name, tbl.Columns[11].Name)
		}
	}
}
