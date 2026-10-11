package catalog

import "testing"

// TestSchemasArePerDatabase pins the registry side of M0119-0006: a user
// schema belongs to the database that created it, the bootstrap schemas are
// visible everywhere, and DROP / RENAME / OWNER in one database leave another
// database's same-named schema alone. Calls without a dbOid keep the old
// process-wide answer (any database, the default one first).
func TestSchemasArePerDatabase(t *testing.T) {
	c := NewInMemory()
	const other = uint32(16500)
	c.RegisterSchema("s", other)
	if !c.SchemaExists("s", other) || c.SchemaExists("s", DefaultDBOid) {
		t.Fatalf("schema s: exists in other=%v default=%v; want true, false",
			c.SchemaExists("s", other), c.SchemaExists("s", DefaultDBOid))
	}
	if !c.SchemaExists("public", other) || !c.SchemaExists("pg_catalog", DefaultDBOid) {
		t.Fatalf("bootstrap schemas must be visible in every database")
	}
	// A connection's real postgres oid normalises to the default namespace.
	if c.SchemaExists("s", PostgresDBOid) {
		t.Fatalf("schema s visible through PostgresDBOid")
	}
	if !c.SchemaExists("s") || c.SchemaOID("s") != c.SchemaOID("s", other) {
		t.Fatalf("unscoped lookup must still find a schema of any database")
	}

	c.RegisterSchema("s", DefaultDBOid)
	a, b := c.SchemaOID("s", other), c.SchemaOID("s", DefaultDBOid)
	if a == 0 || b == 0 || a == b {
		t.Fatalf("same-named schemas in two databases: oids %d / %d, want two distinct", a, b)
	}
	if c.SchemaOID("s") != b {
		t.Fatalf("unscoped lookup prefers the default database: got %d, want %d", c.SchemaOID("s"), b)
	}
	if c.SchemaNameForOID(a) != "s" || c.SchemaNameForOID(b) != "s" {
		t.Fatalf("SchemaNameForOID must resolve both databases' schemas")
	}

	if !c.SetSchemaOwner("s", 4242, other) || c.SchemaOwnerOID("s", other) != 4242 || c.SchemaOwnerOID("s", DefaultDBOid) != 10 {
		t.Fatalf("owner change leaked across databases: other=%d default=%d",
			c.SchemaOwnerOID("s", other), c.SchemaOwnerOID("s", DefaultDBOid))
	}

	c.UnregisterSchema("s", DefaultDBOid)
	if !c.SchemaExists("s", other) || c.SchemaExists("s", DefaultDBOid) {
		t.Fatalf("DROP SCHEMA in the default database removed the other database's schema")
	}
	if _, err := c.RenameSchema("s", "r", other); err != nil {
		t.Fatalf("rename in other: %v", err)
	}
	if !c.SchemaExists("r", other) || c.SchemaExists("r", DefaultDBOid) || c.SchemaOwnerOID("r", other) != 4242 {
		t.Fatalf("rename: r in other=%v default=%v owner=%d", c.SchemaExists("r", other), c.SchemaExists("r", DefaultDBOid), c.SchemaOwnerOID("r", other))
	}

	rows := c.PGNamespaceRowsForDBOid(other)
	var sawR, sawPublic bool
	for _, r := range rows {
		sawR = sawR || r[1] == "r"
		sawPublic = sawPublic || r[1] == "public"
	}
	if !sawR || !sawPublic {
		t.Fatalf("pg_namespace rows of other: r=%v public=%v", sawR, sawPublic)
	}
	for _, r := range c.PGNamespaceRowsForDBOid(DefaultDBOid) {
		if r[1] == "r" {
			t.Fatalf("other's schema in the default database's pg_namespace")
		}
	}

	// CREATE DATABASE ... TEMPLATE copies the template's user schemas.
	const fresh = uint32(16600)
	if names := c.CopyUserSchemas(other, fresh); len(names) != 1 || names[0] != "r" ||
		c.SchemaOID("r", fresh) != c.SchemaOID("r", other) || c.SchemaOwnerOID("r", fresh) != 4242 {
		t.Fatalf("CopyUserSchemas: %v", names)
	}
}
