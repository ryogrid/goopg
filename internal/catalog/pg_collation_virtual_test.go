package catalog

import "testing"

// TestPgCollationVirtualRows pins DU-002 slice 187: the pg_collation virtual
// view (OID 3456) returns the 7 BKI-pinned built-in collations from PG18's
// pg_collation.dat instead of an empty relation. Catalog queries (psql \dO,
// collation-OID joins) depend on these resolving by OID and name.
func TestPgCollationVirtualRows(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.tables["pg_catalog.pg_collation"]
	if !ok {
		t.Fatal("pg_catalog.pg_collation not registered")
	}
	if tbl.OID != 3456 {
		t.Errorf("pg_collation OID=%d, want 3456", tbl.OID)
	}
	if tbl.VirtualRows == nil {
		t.Fatal("pg_collation.VirtualRows is nil")
	}
	rows := tbl.VirtualRows()
	if len(rows) != 7 {
		t.Fatalf("pg_collation row count=%d, want 7", len(rows))
	}
	// Every row must have exactly the 12 declared columns.
	for i, r := range rows {
		if len(r) != len(tbl.Columns) {
			t.Errorf("row %d has %d values, want %d (matches column count)", i, len(r), len(tbl.Columns))
		}
	}

	// Index rows by OID (column 0) for targeted assertions.
	byOID := make(map[string][]string, len(rows))
	for _, r := range rows {
		byOID[r[0]] = r
	}

	// Expected values mirror pg_collation.dat. Columns:
	// 0 oid, 1 collname, 2 collnamespace, 3 collowner, 4 collprovider,
	// 5 collisdeterministic, 6 collencoding, 7 collcollate, 8 collctype,
	// 9 colllocale, 10 collicurules, 11 collversion.
	want := map[string][]string{
		"100":  {"100", "default", "11", "10", "d", "t", "-1", "", "", "", "", ""},
		"950":  {"950", "C", "11", "10", "c", "t", "-1", "C", "C", "", "", ""},
		"951":  {"951", "POSIX", "11", "10", "c", "t", "-1", "POSIX", "POSIX", "", "", ""},
		"962":  {"962", "ucs_basic", "11", "10", "b", "t", "6", "", "", "C", "", "1"},
		"963":  {"963", "unicode", "11", "10", "i", "t", "-1", "", "", "und", "", ""},
		"811":  {"811", "pg_c_utf8", "11", "10", "b", "t", "6", "", "", "C.UTF-8", "", "1"},
		"6411": {"6411", "pg_unicode_fast", "11", "10", "b", "t", "6", "", "", "PG_UNICODE_FAST", "", "1"},
	}
	for oid, exp := range want {
		got, ok := byOID[oid]
		if !ok {
			t.Errorf("collation OID %s missing", oid)
			continue
		}
		for col := range exp {
			if got[col] != exp[col] {
				t.Errorf("OID %s col %d = %q, want %q", oid, col, got[col], exp[col])
			}
		}
	}
}
