package testport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PerDatabaseSchemaSurvivesRestart pins that a schema created inside a
// CREATE DATABASEd database still exists after a restart, and that an extension
// installed into it keeps its extnamespace.
//
// The schema used to VANISH: the startup reload read only the connecting
// catalog's own pg_namespace heap, so the row sat unread in
// base/<thatDbOid>/2615 and the schema simply ceased to exist. Tables in the
// same database reloaded fine — they have their own per-database pass — which
// is exactly what disguised this as an extension-only problem, since the
// extension reload could then not resolve the schema OID and silently fell back
// to "public".
//
// Both halves are asserted deliberately. The extension's namespace alone would
// pass if the reload merely guessed the right schema name; the schema's own
// existence is what proves the pg_namespace heap was actually read. And the
// "public" fallback is a plausible-looking wrong answer, so asserting the
// extension resolves to `uext` specifically is what distinguishes a fix from a
// coincidence.
func TestPort_PerDatabaseSchemaSurvivesRestart(t *testing.T) {
	repo := repoRoot(t)
	c, err := cluster.New("perdb_schema_reload", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(t.TempDir(), "data"),
		PSQLPath:     filepath.Join(repo, "postgres", "local_install", "bin", "psql"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, sql := range []string{
		`CREATE DATABASE userdb`,
	} {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// The fixture lives in userdb, so it goes through psql with an explicit -d.
	if res, err := c.PSQL("-d", "userdb", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE SCHEMA uext; CREATE EXTENSION amcheck SCHEMA uext;"); err != nil || res.ExitCode != 0 {
		t.Fatalf("create schema+extension in userdb: err=%v exit=%d stderr=%s",
			err, res.ExitCode, res.Stderr)
	}

	// Sanity: correct BEFORE the restart, so a failure after it is squarely the
	// reload's and not the DDL's.
	if got := psqlScalarInDB(t, c, "userdb",
		`select n.nspname from pg_extension e join pg_namespace n on e.extnamespace = n.oid where e.extname='amcheck'`); got != "uext" {
		t.Fatalf("before restart: extnamespace = %q, want uext — the fixture itself is wrong", got)
	}

	if err := c.Stop(cluster.ShutdownImmediate); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := psqlScalarInDB(t, c, "userdb",
		`select nspname from pg_namespace where nspname='uext'`); got != "uext" {
		t.Errorf("after restart: schema uext = %q, want uext — a CREATE SCHEMA in a "+
			"CREATE DATABASEd database must survive a restart", got)
	}
	if got := psqlScalarInDB(t, c, "userdb",
		`select n.nspname from pg_extension e join pg_namespace n on e.extnamespace = n.oid where e.extname='amcheck'`); got != "uext" {
		t.Errorf("after restart: extnamespace = %q, want uext (%q is the silent fallback)",
			got, "public")
	}
}

// psqlScalarInDB runs a single-value query against a named database through the
// real psql binary, since the cluster's own Query helper is bound to the
// cluster's default database.
func psqlScalarInDB(t *testing.T, c *cluster.Cluster, db, sqlText string) string {
	t.Helper()
	res, err := c.PSQL("-d", db, "-At", "-c", sqlText)
	if err != nil {
		t.Fatalf("psql -d %s: %v (stderr=%s)", db, err, res.Stderr)
	}
	return strings.TrimSpace(res.Stdout)
}
