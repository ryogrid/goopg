package catalog

import "testing"

// TestBootstrapNamespaceOIDsMatchPG18 pins the four namespace OIDs that
// NewInMemory pre-registers to the values a real PostgreSQL 18.3 cluster
// carries. These OIDs are not decorative: they are what the virtual
// pg_catalog.pg_namespace (catalog.go, "pg_namespace" builder) reports to every
// client, and what SchemaNameForOID reverses when the restart path rebuilds a
// user table's schema from pg_class.relnamespace.
//
// Three of the four are bootstrap namespaces declared in upstream's
// pg_namespace.dat, so they are stable across builds:
//
//	pg_catalog = 11, pg_toast = 99, public = 2200
//
// information_schema is different in kind. It is created while initdb runs
// postgres/src/backend/catalog/information_schema.sql, i.e. AFTER bootstrap, so
// its OID is handed out by the post-bootstrap counter starting at
// FirstUnpinnedObjectId = 12000 (postgres/src/include/access/transam.h). That
// makes it deterministic for a given server build but NOT declared anywhere in
// the .dat files — it can only be measured:
//
//	initdb -D d -U postgres --no-sync
//	pg_ctl -D d -o "-p 5539 -k d -h ''" start
//	psql -h d -p 5539 -U postgres -Atc \
//	  "select oid from pg_namespace where nspname='information_schema'"
//
// Measured twice against postgres/local_install (PG 18.3) on 2026-08-12 for
// M0131-S9.4: both fresh clusters answered 13273. The value goopg carried
// before that measurement was 13183, which no run of this build produces.
//
// The same counter assigns the 80 system-view OIDs that M0131-S9 pinned on disk
// (12000..12355) and, immediately after the 894 pg_description rows for those
// views, the whole information_schema object band (13273..13621). So this
// constant and the S9 pin table are two ends of one sequence: if a future PG
// rebase moves one, it moves the other, and this test is the tripwire.
func TestBootstrapNamespaceOIDsMatchPG18(t *testing.T) {
	c := NewInMemory()
	for _, tc := range []struct {
		name string
		oid  uint32
	}{
		{"pg_catalog", 11},
		{"pg_toast", 99},
		{"public", 2200},
		{"information_schema", 13273},
	} {
		if got := c.SchemaOID(tc.name); got != tc.oid {
			t.Errorf("SchemaOID(%q) = %d, want %d (PG 18.3)", tc.name, got, tc.oid)
		}
		if got := c.SchemaNameForOID(tc.oid); got != tc.name {
			t.Errorf("SchemaNameForOID(%d) = %q, want %q", tc.oid, got, tc.name)
		}
	}
}
