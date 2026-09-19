package catalog

import "testing"

// TestExtensionRowsForDB verifies the per-database scoping of pg_extension
// (M0110-0003, AC-002 gap #7c). PostgreSQL's pg_extension is a per-database
// catalog; goopg shares one in-memory catalog across all databases, so an
// extension installed in one database must be invisible to a connection bound
// to another. ExtensionRowsForDB(db) filters the global registry to db.
func TestExtensionRowsForDB(t *testing.T) {
	c := NewInMemory()

	// amcheck created while connected to "postgres".
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err != nil {
		t.Fatalf("CreateExtension(postgres): %v", err)
	}

	// Visible from the database it was created in.
	if rows := c.ExtensionRowsForDB("postgres"); len(rows) != 1 {
		t.Fatalf("postgres: got %d rows, want 1: %v", len(rows), rows)
	} else if rows[0][1] != "amcheck" {
		t.Errorf("postgres: extname = %q, want amcheck", rows[0][1])
	}

	// Invisible from a different database — this is the gap #7c fix that lets
	// pg_amcheck warn `skipping database "template1": amcheck is not installed`.
	if rows := c.ExtensionRowsForDB("template1"); len(rows) != 0 {
		t.Fatalf("template1: got %d rows, want 0 (amcheck created in postgres): %v", len(rows), rows)
	}
	if rows := c.ExtensionRowsForDB("template0"); len(rows) != 0 {
		t.Fatalf("template0: got %d rows, want 0: %v", len(rows), rows)
	}

	// An empty filter (no connection context — embedded/test callers) sees
	// every row, matching the global VirtualRows fallback.
	if rows := c.ExtensionRowsForDB(""); len(rows) != 1 {
		t.Fatalf("empty filter: got %d rows, want 1 (all): %v", len(rows), rows)
	}
}

// TestExtensionRowsForDB_UnscopedVisibleEverywhere verifies that a row created
// with no database scope (legacy/direct-call insert, database == "") is visible
// from every database, so existing behaviour is preserved.
func TestExtensionRowsForDB_UnscopedVisibleEverywhere(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "", false); err != nil {
		t.Fatalf("CreateExtension(unscoped): %v", err)
	}
	for _, db := range []string{"postgres", "template1", "anything", ""} {
		if rows := c.ExtensionRowsForDB(db); len(rows) != 1 {
			t.Errorf("db %q: got %d rows, want 1 (unscoped row is global): %v", db, len(rows), rows)
		}
	}
}

// TestExtensionVirtualRowsGlobal verifies the table-level VirtualRows callback
// (used when no per-connection lister is wired) still returns every row.
func TestExtensionVirtualRowsGlobal(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err != nil {
		t.Fatalf("CreateExtension: %v", err)
	}
	tbl, ok := c.ns(DefaultDBOid).tables["pg_catalog.pg_extension"]
	if !ok || tbl.VirtualRows == nil {
		t.Fatalf("pg_extension table or VirtualRows missing")
	}
	if rows := tbl.VirtualRows(); len(rows) != 1 {
		t.Fatalf("global VirtualRows: got %d rows, want 1: %v", len(rows), rows)
	}
}

// TestCreateExtensionPerDatabase verifies the write side of per-database
// pg_extension (M0119-0006bs): the same extname may be installed
// independently in two databases — upstream's pg_extension_name_index
// constrains within one database only. Before the re-key, the global
// name-keyed registry answered "already exists" for a second database,
// which is what blocked `CREATE EXTENSION amcheck` inside a CREATE
// DATABASE'd database ahead of whole-database pg_amcheck.
func TestCreateExtensionPerDatabase(t *testing.T) {
	c := NewInMemory()

	created, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false)
	if err != nil || !created {
		t.Fatalf("CreateExtension(postgres): created=%v err=%v", created, err)
	}
	// Same name in a second database is legal and independent.
	created, err = c.CreateExtension("amcheck", "public", "1.4", "amcheckdb", false)
	if err != nil || !created {
		t.Fatalf("CreateExtension(amcheckdb): created=%v err=%v", created, err)
	}
	// Each database sees exactly its own row.
	if rows := c.ExtensionRowsForDB("postgres"); len(rows) != 1 {
		t.Fatalf("postgres: got %d rows, want 1: %v", len(rows), rows)
	}
	if rows := c.ExtensionRowsForDB("amcheckdb"); len(rows) != 1 {
		t.Fatalf("amcheckdb: got %d rows, want 1: %v", len(rows), rows)
	}
	// The two installs carry distinct OIDs.
	po, ao := c.ExtensionOID("amcheck", "postgres"), c.ExtensionOID("amcheck", "amcheckdb")
	if po == 0 || ao == 0 || po == ao {
		t.Fatalf("ExtensionOID: postgres=%d amcheckdb=%d — want two distinct nonzero oids", po, ao)
	}
	// Duplicate inside one database still errors; IF NOT EXISTS reports
	// created=false without erroring (the executor turns that into PG's
	// "already exists, skipping" NOTICE).
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err == nil {
		t.Fatal("duplicate CreateExtension(postgres): want error")
	}
	if created, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", true); err != nil || created {
		t.Fatalf("IF NOT EXISTS on installed: created=%v err=%v, want (false, nil)", created, err)
	}
}

// TestDropExtensionPerDatabase verifies DROP EXTENSION removes only the
// current database's row (M0119-0006bs) — under the old global key a DROP
// in one database uninstalled it everywhere.
func TestDropExtensionPerDatabase(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "amcheckdb", false); err != nil {
		t.Fatal(err)
	}

	// Dropping in amcheckdb leaves the postgres install intact.
	oid, scope, ok := c.DropExtension("amcheck", "amcheckdb")
	if !ok || scope != "amcheckdb" || oid == 0 {
		t.Fatalf("DropExtension(amcheckdb): oid=%d scope=%q ok=%v", oid, scope, ok)
	}
	if rows := c.ExtensionRowsForDB("amcheckdb"); len(rows) != 0 {
		t.Fatalf("amcheckdb after drop: got %d rows, want 0", len(rows))
	}
	if rows := c.ExtensionRowsForDB("postgres"); len(rows) != 1 {
		t.Fatalf("postgres after drop: got %d rows, want 1", len(rows))
	}
	if c.ExtensionOID("amcheck", "postgres") == 0 {
		t.Fatal("postgres ExtensionOID = 0 after dropping amcheckdb's row")
	}
	// Dropping a name absent from this database (installed only elsewhere)
	// is a no-op — the caller's existence check produces the 42704.
	if _, _, ok := c.DropExtension("plperl", "amcheckdb"); ok {
		t.Fatal("DropExtension of absent name: want ok=false")
	}
}

// TestExtensionScopeSurvivesDropDatabase verifies DropDatabase purges the
// dropped database's extension rows — a recreated same-name database must
// not inherit phantom installs (M0119-0006bs).
func TestExtensionScopeSurvivesDropDatabase(t *testing.T) {
	c := NewInMemory()
	// Register a user database, install there, drop the database.
	if _, err := c.CreateDatabase("amcheckdb", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateExtension("amcheck", "public", "1.4", "amcheckdb", false); err != nil {
		t.Fatal(err)
	}
	if err := c.DropDatabase("amcheckdb"); err != nil {
		t.Fatal(err)
	}
	if rows := c.ExtensionRowsForDB("amcheckdb"); len(rows) != 0 {
		t.Fatalf("dropped db: got %d rows, want 0", len(rows))
	}
	// A same-name recreate starts clean.
	if _, err := c.CreateDatabase("amcheckdb", 10); err != nil {
		t.Fatal(err)
	}
	if created, err := c.CreateExtension("amcheck", "public", "1.4", "amcheckdb", false); err != nil || !created {
		t.Fatalf("reinstall after recreate: created=%v err=%v, want (true, nil)", created, err)
	}
}

// TestCreateExtensionDuringRecoveryPerDatabase verifies restart replay
// registers same-named rows in distinct database scopes independently
// (M0119-0006bs) — previously the second row was silently deduped.
func TestCreateExtensionDuringRecoveryPerDatabase(t *testing.T) {
	c := NewInMemory()
	c.CreateExtensionDuringRecovery("amcheck", "public", "1.4", "postgres", 16400)
	c.CreateExtensionDuringRecovery("amcheck", "public", "1.4", "amcheckdb", 16401)
	if rows := c.ExtensionRowsForDB("postgres"); len(rows) != 1 {
		t.Fatalf("postgres: got %d rows, want 1", len(rows))
	}
	if rows := c.ExtensionRowsForDB("amcheckdb"); len(rows) != 1 {
		t.Fatalf("amcheckdb: got %d rows, want 1", len(rows))
	}
	// nextOID must be advanced past recovered OIDs or a later runtime
	// install can reallocate 16400/16401.
	if created, err := c.CreateExtension("plperl", "public", "1.0", "postgres", false); err != nil || !created {
		t.Fatalf("post-recovery CreateExtension: created=%v err=%v", created, err)
	}
	if oid := c.ExtensionOID("plperl", "postgres"); oid <= 16401 {
		t.Fatalf("post-recovery oid = %d, want > 16401 (no collision with recovered oids)", oid)
	}
}
