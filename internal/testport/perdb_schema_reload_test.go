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

// TestPort_SchemasArePerDatabase pins pg_namespace's per-database scope
// (M0119-0006). goopg registered every user schema process-wide, so a CREATE
// SCHEMA in one database showed in every other's pg_namespace, and a DROP
// SCHEMA in one removed the other database's registry entry — after which
// ALTER SCHEMA there failed "does not exist" while its tables lived on. Each
// assertion is one of those measured symptoms, checked before and after a
// restart (the startup reload must register a schema in its own database).
func TestPort_SchemasArePerDatabase(t *testing.T) {
	repo := repoRoot(t)
	c, err := cluster.New("perdb_schema_scope", cluster.Options{
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

	run := func(db, sql string) {
		t.Helper()
		if res, err := c.PSQL("-d", db, "-v", "ON_ERROR_STOP=1", "-c", sql); err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %s: err=%v exit=%d stderr=%s", db, sql, err, res.ExitCode, res.Stderr)
		}
	}
	if err := runSQLSimple(t, c, `CREATE DATABASE userdb`); err != nil {
		t.Fatalf("create database: %v", err)
	}
	run("userdb", "CREATE SCHEMA only_user; CREATE SCHEMA shared_name; CREATE TABLE shared_name.t(a int); INSERT INTO shared_name.t VALUES (1)")
	run("postgres", "CREATE SCHEMA shared_name; CREATE TABLE shared_name.t(a int); INSERT INTO shared_name.t VALUES (2), (3)")

	check := func(phase string) {
		t.Helper()
		if got := psqlScalarInDB(t, c, "postgres", `select count(*) from pg_namespace where nspname = 'only_user'`); got != "0" {
			t.Errorf("%s: userdb's schema visible in postgres' pg_namespace (count %s)", phase, got)
		}
		if got := psqlScalarInDB(t, c, "userdb", `select count(*) from pg_namespace where nspname in ('only_user', 'shared_name')`); got != "2" {
			t.Errorf("%s: userdb's pg_namespace lists %s of its 2 schemas", phase, got)
		}
		a := psqlScalarInDB(t, c, "userdb", `select oid from pg_namespace where nspname = 'shared_name'`)
		b := psqlScalarInDB(t, c, "postgres", `select oid from pg_namespace where nspname = 'shared_name'`)
		if a == "" || a == b {
			t.Errorf("%s: shared_name oids userdb=%q postgres=%q — two databases' schemas must be distinct", phase, a, b)
		}
		if got := psqlScalarInDB(t, c, "userdb", `select c.relnamespace = n.oid from pg_class c join pg_namespace n on n.nspname = 'shared_name' where c.relname = 't' and c.relkind = 'r'`); got != "t" {
			t.Errorf("%s: userdb's shared_name.t relnamespace does not name userdb's schema (%q)", phase, got)
		}
	}
	check("before restart")

	// DROP SCHEMA in postgres must leave userdb's same-named schema alone.
	run("postgres", "DROP SCHEMA shared_name CASCADE")
	run("userdb", "ALTER SCHEMA shared_name RENAME TO renamed")
	if got := psqlScalarInDB(t, c, "userdb", `select count(*) from renamed.t`); got != "1" {
		t.Errorf("after drop in postgres + rename in userdb: renamed.t count = %q, want 1", got)
	}
	run("userdb", "ALTER SCHEMA renamed RENAME TO shared_name")
	run("postgres", "CREATE SCHEMA shared_name; CREATE TABLE shared_name.t(a int)")

	if err := c.Stop(cluster.ShutdownImmediate); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	check("after restart")
}
