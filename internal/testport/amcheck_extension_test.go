package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_AmcheckCreateExtension exercises the CREATE EXTENSION amcheck SQL
// surface (M0110-0003, slices S1+S2 of docs/design/0110-0008). This is the
// server-side requirement pg_amcheck's 002_nonesuch TAP test depends on: the
// extension installs, and pg_amcheck's "is amcheck installed?" probe query
// answers correctly.
//
//	SELECT n.nspname, x.extversion FROM pg_catalog.pg_extension x
//	  JOIN pg_catalog.pg_namespace n ON x.extnamespace = n.oid
//	WHERE x.extname = 'amcheck'   (pg_amcheck.c:173)
//
// Before the extension is installed the probe returns 0 rows (pg_amcheck logs
// "skipping database … amcheck is not installed"); after, exactly one row whose
// nspname/extversion drive the client's schema-qualification + version gate.
func TestPort_AmcheckCreateExtension(t *testing.T) {
	c := newCluster(t, "amcheck_ext")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	const probe = `SELECT n.nspname, x.extversion FROM pg_catalog.pg_extension x ` +
		`JOIN pg_catalog.pg_namespace n ON x.extnamespace = n.oid ` +
		`WHERE x.extname = 'amcheck'`

	// Not installed yet → probe returns no rows.
	if rows := runSQL(t, c, probe); len(rows) != 0 {
		t.Fatalf("pre-install probe returned %d rows, want 0: %v", len(rows), rows)
	}

	// Install.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("CREATE EXTENSION amcheck: %v", err)
	}

	// Probe now returns exactly one row: (public, 1.4).
	rows := runSQL(t, c, probe)
	if len(rows) != 1 {
		t.Fatalf("post-install probe returned %d rows, want 1: %v", len(rows), rows)
	}
	if rows[0][0] != "public" {
		t.Errorf("nspname = %q, want public", rows[0][0])
	}
	if rows[0][1] != "1.4" {
		t.Errorf("extversion = %q, want 1.4", rows[0][1])
	}

	// Re-installing without IF NOT EXISTS errors (PG: 42710 duplicate_object).
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err == nil {
		t.Errorf("duplicate CREATE EXTENSION amcheck: want error, got nil")
	}

	// IF NOT EXISTS is idempotent and the registry is unchanged.
	if err := runSQLSimple(t, c, "CREATE EXTENSION IF NOT EXISTS amcheck"); err != nil {
		t.Errorf("CREATE EXTENSION IF NOT EXISTS amcheck: %v", err)
	}
	if rows := runSQL(t, c, probe); len(rows) != 1 {
		t.Fatalf("probe after IF NOT EXISTS returned %d rows, want 1: %v", len(rows), rows)
	}

	// An unknown extension errors as if its control file were missing.
	if err := runSQLSimple(t, c, "CREATE EXTENSION no_such_extension"); err == nil {
		t.Errorf("CREATE EXTENSION no_such_extension: want error, got nil")
	}
}
