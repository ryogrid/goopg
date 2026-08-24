package catalog

import "testing"

// TestPGInitPrivsRowsForDBOidNonEmpty pins the M0134-0132 fix: pg_init_privs
// must report at least one row so `SELECT count(*) > 0 FROM pg_init_privs`
// (postgres/src/test/regress/sql/init_privs.sql) returns true, matching every
// real PostgreSQL cluster (initdb's setup_privileges() always seeds relacl,
// and hence pg_init_privs, for the pg_catalog/information_schema relations
// that exist at bootstrap time — see the PGInitPrivsRowsForDBOid doc comment).
func TestPGInitPrivsRowsForDBOidNonEmpty(t *testing.T) {
	c := NewInMemory()
	rows := c.PGInitPrivsRowsForDBOid(DefaultDBOid)
	if len(rows) == 0 {
		t.Fatal("PGInitPrivsRowsForDBOid returned no rows; SELECT count(*) > 0 FROM pg_init_privs would fail (init_privs.sql)")
	}
	// pg_class (OID 1259) is a pg_catalog relation that exists at bootstrap and
	// must be represented.
	found := false
	for _, r := range rows {
		if len(r) != 5 {
			t.Fatalf("row has %d columns, want 5 (objoid, classoid, objsubid, privtype, initprivs): %v", len(r), r)
		}
		if r[0] == "1259" {
			found = true
			if r[1] != "1259" {
				t.Errorf("pg_class row classoid = %q, want \"1259\" (RelationRelationId)", r[1])
			}
			if r[2] != "0" {
				t.Errorf("pg_class row objsubid = %q, want \"0\"", r[2])
			}
			if r[3] != "i" {
				t.Errorf("pg_class row privtype = %q, want \"i\" (initdb-time)", r[3])
			}
			if r[4] == "" {
				t.Error("pg_class row initprivs is empty, want a non-NULL aclitem[] literal")
			}
		}
	}
	if !found {
		t.Error("no pg_init_privs row for pg_class (OID 1259) — every real PG cluster seeds one at initdb")
	}
}

// TestPGInitPrivsRowsForDBOidExcludesUserTables confirms user-created tables
// (which get NULL relacl by default, matching acldefault) do NOT get a
// pg_init_privs row — only the pg_catalog/information_schema objects that
// existed at initdb time do.
func TestPGInitPrivsRowsForDBOidExcludesUserTables(t *testing.T) {
	c := NewInMemory()
	userTbl := &Table{Schema: "public", Name: "widgets", OID: 99999}
	c.ns(DefaultDBOid).tables["public.widgets"] = userTbl
	rows := c.PGInitPrivsRowsForDBOid(DefaultDBOid)
	for _, r := range rows {
		if r[0] == "99999" {
			t.Errorf("user table widgets (OID 99999) got a pg_init_privs row; only initdb-time system relations should")
		}
	}
}
