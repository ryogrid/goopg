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

// TestPGInitPrivsRowsMatchInitdb pins M0149-0001g slice 22: goopg's
// pg_init_privs view serves PG 18.3 initdb's rows (the same table the on-disk
// 3394 heap is written from), with the bootstrap superuser under its name.
func TestPGInitPrivsRowsMatchInitdb(t *testing.T) {
	c := NewInMemory()
	rows := c.PGInitPrivsRowsForDBOid(DefaultDBOid)
	if len(rows) != len(PGInitdbInitPrivs) {
		t.Fatalf("got %d rows, want %d", len(rows), len(PGInitdbInitPrivs))
	}
	byKey := map[string]string{}
	for _, r := range rows {
		byKey[r[0]+"/"+r[1]+"/"+r[2]] = r[4]
	}
	for key, want := range map[string]string{
		"11/2615/0":   "{postgres=UC/postgres,=U/postgres}",
		"2200/2615/0": "{pg_database_owner=UC/pg_database_owner,=U/pg_database_owner}",
		"1260/1259/0": "{postgres=arwdDxtm/postgres}",
	} {
		if got := byKey[key]; got != want {
			t.Errorf("initprivs %s = %q, want %q", key, got, want)
		}
	}
}

// TestBootstrapSchemaNspaclMatchesInitPrivs: pg_catalog's and
// information_schema's nspacl must equal their initprivs, else pg_dump emits
// REVOKE USAGE ON SCHEMA … FROM PUBLIC (M0149-0001g slice 22).
func TestBootstrapSchemaNspaclMatchesInitPrivs(t *testing.T) {
	c := NewInMemory()
	for _, oid := range []uint32{11, 13273} {
		if got, want := c.NamespaceACLText(oid), "{postgres=UC/postgres,=U/postgres}"; got != want {
			t.Errorf("nspacl(%d) = %q, want %q", oid, got, want)
		}
	}
}

// TestPublicSchemaOwnerAndACLMatchInitdb pins M0149-0001g slice 23: schema
// public is owned by pg_database_owner and carries PG's initdb nspacl, so a
// fresh goopg database pg_dumps exactly like a fresh PG one (no ALTER SCHEMA
// public OWNER, no REVOKE USAGE). A superuser GRANT renders the owner as
// grantor (select_best_grantor), and ALTER SCHEMA public OWNER drops the
// pg_database_owner rendering.
func TestPublicSchemaOwnerAndACLMatchInitdb(t *testing.T) {
	c := NewInMemory()
	if got := c.SchemaOwnerOID("public"); got != 6171 {
		t.Errorf("public owner = %d, want 6171 (pg_database_owner)", got)
	}
	want := "{pg_database_owner=UC/pg_database_owner,=U/pg_database_owner}"
	if got := c.NamespaceACLText(2200); got != want {
		t.Errorf("public nspacl = %q, want %q", got, want)
	}
	c.GrantTablePrivilege(2200, "bob", "USAGE")
	want = "{pg_database_owner=UC/pg_database_owner,=U/pg_database_owner,bob=U/pg_database_owner}"
	if got := c.NamespaceACLText(2200); got != want {
		t.Errorf("public nspacl after GRANT = %q, want %q", got, want)
	}
}

// TestInitdbDescriptionsSeedComments: goopg's comment store starts with PG
// initdb's built-in descriptions (M0149-0001g slice 23), e.g. schema public's.
func TestInitdbDescriptionsSeedComments(t *testing.T) {
	c := NewInMemory()
	for _, k := range []struct {
		class, obj uint32
		want       string
	}{
		{2615, 2200, "standard public schema"},
		{2615, 11, "system catalog schema"},
		{1262, 5, "default administrative connection database"},
	} {
		if got, ok := c.GetComment(k.class, k.obj, 0); !ok || got != k.want {
			t.Errorf("comment (%d,%d) = %q,%v, want %q", k.class, k.obj, got, ok, k.want)
		}
	}
	if oid, ok := c.SystemCatalogOID("pg_namespace"); !ok || oid != 2615 {
		t.Errorf("SystemCatalogOID(pg_namespace) = %d,%v, want 2615", oid, ok)
	}
}

// TestPGNamespaceRowsListPgToast pins M0149-0001g slice 24: goopg's
// pg_namespace lists pg_toast (OID 99, owner 10, NULL nspacl) like PG's
// pg_namespace.dat; it was skipped as an "internal alias" while its OID still
// collided with public's.
func TestPGNamespaceRowsListPgToast(t *testing.T) {
	c := NewInMemory()
	for _, r := range c.PGNamespaceRowsForDBOid(DefaultDBOid) {
		if r[1] == "pg_toast" {
			if r[0] != "99" || r[2] != "10" || r[3] != "" {
				t.Errorf("pg_toast row = %v, want [99 pg_toast 10 <NULL>]", r)
			}
			return
		}
	}
	t.Fatal("pg_namespace has no pg_toast row")
}
