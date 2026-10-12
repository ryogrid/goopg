package testport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_RoutinesArePerDatabase pins M0146-0156. In a database made by
// CREATE DATABASE, CREATE FUNCTION succeeded and pg_proc listed the function,
// but calling it raised `function ... does not exist` (resolution looked in the
// default database), and after a clean restart it was gone from every
// database's pg_proc (the reload scanned only the default database's pg_proc
// heap; aggregates, the heap's other consumer, likewise). PostgreSQL 18.3:
// callable, private to its database, and durable.
func TestPort_RoutinesArePerDatabase(t *testing.T) {
	repo := repoRoot(t)
	c, err := cluster.New("perdb_routines", cluster.Options{
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
	for _, db := range []string{"pa", "pb", "gone"} {
		if err := runSQLSimple(t, c, "CREATE DATABASE "+db); err != nil {
			t.Fatalf("create database %s: %v", db, err)
		}
	}
	run("pa", `CREATE FUNCTION same(int) RETURNS text LANGUAGE sql AS $$ SELECT 'pa ' || $1 $$`)
	run("pa", `CREATE FUNCTION twice(int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN $1 * 2; END $$`)
	run("pa", `CREATE PROCEDURE note() LANGUAGE sql AS $$ SELECT 1 $$`)
	// Aggregates are the pg_proc heap's other consumer (prokind 'a').
	run("pa", `CREATE AGGREGATE mysum(int) (SFUNC = int4pl, STYPE = int)`)
	run("pb", `CREATE FUNCTION same(int) RETURNS text LANGUAGE sql AS $$ SELECT 'pb ' || $1 $$`)
	run("gone", `CREATE FUNCTION doomed() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`)

	check := func(phase string) {
		t.Helper()
		if got := psqlScalarInDB(t, c, "pa", `SELECT same(1) || ' ' || twice(4)`); got != "pa 1 8" {
			t.Errorf("%s: pa calls = %q, want %q", phase, got, "pa 1 8")
		}
		if got := psqlScalarInDB(t, c, "pa", `SELECT mysum(x) FROM generate_series(1, 4) x`); got != "10" {
			t.Errorf("%s: pa mysum = %q, want 10", phase, got)
		}
		if got := psqlScalarInDB(t, c, "pb", `SELECT same(2)`); got != "pb 2" {
			t.Errorf("%s: pb same(2) = %q, want %q (each database calls its own)", phase, got, "pb 2")
		}
		if res, err := c.PSQL("-d", "pa", "-At", "-c", "CALL note()"); err != nil || res.ExitCode != 0 {
			t.Errorf("%s: CALL note() in pa failed: %v %s", phase, err, res.Stderr)
		}
		if got := psqlScalarInDB(t, c, "pa", `SELECT string_agg(proname, ',' ORDER BY proname) FROM pg_proc WHERE proname IN ('same', 'twice', 'note')`); got != "note,same,twice" {
			t.Errorf("%s: pa pg_proc = %q, want note,same,twice", phase, got)
		}
		if got := psqlScalarInDB(t, c, "postgres", `SELECT count(*) FROM pg_proc WHERE proname IN ('same', 'twice', 'note')`); got != "0" {
			t.Errorf("%s: postgres pg_proc lists %s of the other databases' routines", phase, got)
		}
		res, _ := c.PSQL("-d", "postgres", "-At", "-c", "SELECT twice(1)")
		if !strings.Contains(res.Stderr, "does not exist") {
			t.Errorf("%s: twice() is callable from postgres; want does not exist", phase)
		}
	}
	check("before restart")

	// DROP DATABASE takes its routines along.
	if err := runSQLSimple(t, c, "DROP DATABASE gone"); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DATABASE gone"); err != nil {
		t.Fatalf("recreate database: %v", err)
	}
	if got := psqlScalarInDB(t, c, "gone", `SELECT count(*) FROM pg_proc WHERE proname = 'doomed'`); got != "0" {
		t.Errorf("recreated database lists the dropped one's routine (%s)", got)
	}

	for restart := 1; restart <= 2; restart++ {
		if err := c.Stop(cluster.ShutdownFast); err != nil {
			t.Fatalf("stop %d: %v", restart, err)
		}
		if err := c.Start(); err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		check("after restart")
	}
}
