package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgClassRelfilenodeZeroForStorageLessRelkinds covers M0134-0164.
//
// PG's heap_create (postgres/src/backend/catalog/heap.c:335-345) assigns a
// relfilenumber only when RELKIND_HAS_STORAGE (pg_class.h:200) holds, so a
// plain view, composite type, foreign table, partitioned table or partitioned
// index reads back with pg_class.relfilenode = 0. goopg's VIRTUAL pg_class
// builder handed out the relation OID for every kind, which is what regress
// sanity_check.sql's second query catches:
//
//	SELECT relname, relkind FROM pg_class
//	 WHERE relkind IN ('v','c','f','p','I') AND relfilenode <> 0;
//
// It listed every view in the database. The HEAP row builder
// (buildUserPGClassRow) had the right answer via an ad-hoc `relkind == "p" ||
// relkind == "v"` check with no virtual twin — the classic sibling-path
// divergence, since the two render the SAME relation for goopg's own
// introspection and for a real PG attached to the cluster.
//
// This asserts the property directly on the virtual builder rather than
// re-deriving it, so a future kind added to only one of the two builders fails
// here instead of in a regress diff.
func TestPgClassRelfilenodeZeroForStorageLessRelkinds(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, stmt := range []string{
		"CREATE TABLE rf_plain (id int4, name text)",
		"CREATE INDEX rf_plain_idx ON rf_plain (id)",
		"CREATE VIEW rf_view AS SELECT id FROM rf_plain",
		"CREATE TABLE rf_part (id int4, name text) PARTITION BY RANGE (id)",
		"CREATE TABLE rf_part_1 PARTITION OF rf_part FOR VALUES FROM (0) TO (100)",
		"CREATE INDEX rf_part_idx ON rf_part (id)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	// pg_class column ordinals: 1 relname, 7 relfilenode, 17 relkind.
	const (
		colRelname     = 1
		colRelfilenode = 7
		colRelkind     = 17
	)

	seen := map[string]bool{}
	for _, row := range im.PGClassRowsForDBOid(catalog.DefaultDBOid) {
		if len(row) <= colRelkind {
			t.Fatalf("pg_class row %q has %d columns, want > %d", row, len(row), colRelkind)
		}
		relname, relkind, relfilenode := row[colRelname], row[colRelkind], row[colRelfilenode]
		seen[relname] = true
		if catalog.RelkindHasStorage(relkind) {
			if relfilenode == "0" {
				t.Errorf("%s (relkind %q) has relfilenode 0; a kind with storage must carry its own relfilenode", relname, relkind)
			}
			continue
		}
		if relfilenode != "0" {
			t.Errorf("%s (relkind %q) has relfilenode %s, want 0 — PG leaves storage-less kinds at InvalidRelFileNumber (heap.c:335)",
				relname, relkind, relfilenode)
		}
	}

	// Guard the guard: an empty or partial enumeration would pass the loop
	// above vacuously. The storage-less kinds this test exists for must
	// actually be present.
	for _, name := range []string{"rf_plain", "rf_plain_idx", "rf_view", "rf_part", "rf_part_1", "rf_part_idx"} {
		if !seen[name] {
			t.Errorf("pg_class rows are missing %s — the relkind coverage above was vacuous for it", name)
		}
	}
}

// TestRelkindHasStorageMatchesUpstreamMacro pins catalog.RelkindHasStorage to
// upstream's RELKIND_HAS_STORAGE (postgres/src/include/catalog/pg_class.h:200)
// over the full relkind alphabet goopg can emit, so the shared rule cannot
// drift away from the oracle without a failing test. M0134-0164.
func TestRelkindHasStorageMatchesUpstreamMacro(t *testing.T) {
	want := map[string]bool{
		"r": true,  // RELKIND_RELATION
		"i": true,  // RELKIND_INDEX
		"S": true,  // RELKIND_SEQUENCE
		"t": true,  // RELKIND_TOASTVALUE
		"m": true,  // RELKIND_MATVIEW
		"v": false, // RELKIND_VIEW
		"c": false, // RELKIND_COMPOSITE_TYPE
		"f": false, // RELKIND_FOREIGN_TABLE
		"p": false, // RELKIND_PARTITIONED_TABLE
		"I": false, // RELKIND_PARTITIONED_INDEX
		"":  false, // unset
	}
	for relkind, expect := range want {
		if got := catalog.RelkindHasStorage(relkind); got != expect {
			t.Errorf("RelkindHasStorage(%q) = %v, want %v", relkind, got, expect)
		}
		cell := catalog.RelfilenodeForRelkind(relkind, 16400)
		if expect && cell != "16400" {
			t.Errorf("RelfilenodeForRelkind(%q, 16400) = %s, want 16400", relkind, cell)
		}
		if !expect && cell != "0" {
			t.Errorf("RelfilenodeForRelkind(%q, 16400) = %s, want 0", relkind, cell)
		}
	}
}
