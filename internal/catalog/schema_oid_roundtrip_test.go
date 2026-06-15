package catalog

import "testing"

// TestSchemaOIDNameRoundTrip pins the SchemaOID <-> SchemaNameForOID inverse
// pair that the M0110-0003 user-schema table durability fix relies on:
// syncTableToCatalogHeap stamps a user table's pg_class.relnamespace with
// SchemaOID(schema), and the restart-recovery path (loadUserTablesFromHeap)
// reverse-maps that OID back to the schema name with SchemaNameForOID.
func TestSchemaOIDNameRoundTrip(t *testing.T) {
	c := NewInMemory()

	// Unregistered schema: forward lookup is 0, reverse is "".
	if got := c.SchemaOID("s1"); got != 0 {
		t.Fatalf("SchemaOID(unregistered) = %d, want 0", got)
	}

	c.RegisterSchema("s1")
	c.RegisterSchema("s2")

	oid1 := c.SchemaOID("s1")
	oid2 := c.SchemaOID("s2")
	if oid1 == 0 || oid2 == 0 {
		t.Fatalf("registered schemas got zero OIDs: s1=%d s2=%d", oid1, oid2)
	}
	if oid1 == oid2 {
		t.Fatalf("distinct schemas share an OID: %d", oid1)
	}

	// Forward then reverse must round-trip (registry lower-cases names).
	if got := c.SchemaNameForOID(oid1); got != "s1" {
		t.Errorf("SchemaNameForOID(%d) = %q, want %q", oid1, got, "s1")
	}
	if got := c.SchemaNameForOID(oid2); got != "s2" {
		t.Errorf("SchemaNameForOID(%d) = %q, want %q", oid2, got, "s2")
	}

	// OID 0 and unknown OIDs reverse to "".
	if got := c.SchemaNameForOID(0); got != "" {
		t.Errorf("SchemaNameForOID(0) = %q, want \"\"", got)
	}
	if got := c.SchemaNameForOID(oid2 + 9999); got != "" {
		t.Errorf("SchemaNameForOID(unknown) = %q, want \"\"", got)
	}

	// After recovery-style registration the carried OID must round-trip so a
	// pre-crash relnamespace resolves to the same name post-restart.
	c.RegisterSchemaDuringRecovery("s3", 60123)
	if got := c.SchemaNameForOID(60123); got != "s3" {
		t.Errorf("SchemaNameForOID(60123) = %q, want %q", got, "s3")
	}

	// Dropping a schema removes both directions.
	c.UnregisterSchema("s1")
	if got := c.SchemaOID("s1"); got != 0 {
		t.Errorf("SchemaOID after drop = %d, want 0", got)
	}
	if got := c.SchemaNameForOID(oid1); got != "" {
		t.Errorf("SchemaNameForOID after drop = %q, want \"\"", got)
	}
}
