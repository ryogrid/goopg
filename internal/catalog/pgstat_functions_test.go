package catalog

import "testing"

// TestPGStatUserFunctionsRowsAlwaysEmpty pins the always-empty contract of the
// pg_stat_user_functions / pg_stat_xact_user_functions row builder: goopg has no
// per-function call/time tracking, so — exactly like a stock PostgreSQL cluster
// with the default track_functions = none — both views report zero rows. M0122-0003.
func TestPGStatUserFunctionsRowsAlwaysEmpty(t *testing.T) {
	c := NewInMemory()
	if rows := c.PGStatUserFunctionsRows(); len(rows) != 0 {
		t.Fatalf("PGStatUserFunctionsRows() = %d rows, want 0 (no function-call tracking)", len(rows))
	}
}

// TestPGStatUserFunctionsViewsRegistered confirms both function-statistics views
// resolve as virtual pg_catalog relations with the upstream 6-column tupledesc,
// so a client can introspect them (and query them for 0 rows) instead of hitting
// an unknown-relation error. M0122-0003.
func TestPGStatUserFunctionsViewsRegistered(t *testing.T) {
	c := NewInMemory()
	wantCols := []string{"funcid", "schemaname", "funcname", "calls", "total_time", "self_time"}
	for _, name := range []string{"pg_stat_user_functions", "pg_stat_xact_user_functions"} {
		tbl := c.ns(DefaultDBOid).tables["pg_catalog."+name]
		if tbl == nil {
			t.Fatalf("%s not registered as a virtual pg_catalog table", name)
		}
		if !tbl.Virtual {
			t.Errorf("%s: Virtual = false, want true", name)
		}
		if len(tbl.Columns) != len(wantCols) {
			t.Fatalf("%s: %d columns, want %d", name, len(tbl.Columns), len(wantCols))
		}
		for i, want := range wantCols {
			if tbl.Columns[i].Name != want {
				t.Errorf("%s: column %d = %q, want %q", name, i, tbl.Columns[i].Name, want)
			}
		}
		if tbl.VirtualRows == nil {
			t.Fatalf("%s: VirtualRows is nil", name)
		}
		if rows := tbl.VirtualRows(); len(rows) != 0 {
			t.Errorf("%s: VirtualRows() = %d rows, want 0", name, len(rows))
		}
	}
}
