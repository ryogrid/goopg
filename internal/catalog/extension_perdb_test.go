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
	if err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err != nil {
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
	if err := c.CreateExtension("amcheck", "public", "1.4", "", false); err != nil {
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
	if err := c.CreateExtension("amcheck", "public", "1.4", "postgres", false); err != nil {
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
