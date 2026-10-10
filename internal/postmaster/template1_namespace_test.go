package postmaster

import (
	"context"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestTemplate1HasItsOwnNamespace pins 0119-0006bv Option A: template1 no
// longer shares the DefaultDBOid namespace with postgres. Before, a table
// created in template1 was visible from postgres and vice versa (PG raises
// 42P01 — separate databases), and pg_dumpall emitted postgres' tables under
// template1 too. It also pins the two pg_database-row writers that key by
// oid: template1's namespace oid (2) is not its pg_database row oid (1), so a
// GRANT on template1 and a CREATE DATABASE (whose row copies template1's
// encoding) must still persist across a restart.
func TestTemplate1HasItsOwnNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	ctx := context.Background()
	exec := func(s *dbidRestartServer, db, stmt string) error {
		t.Helper()
		c := s.open(t, db)
		defer c.Close()
		_, err := c.ExecContext(ctx, stmt)
		return err
	}
	count := func(s *dbidRestartServer, db, query string) (int, error) {
		t.Helper()
		c := s.open(t, db)
		defer c.Close()
		var n int
		err := c.QueryRowContext(ctx, query).Scan(&n)
		return n, err
	}

	s1 := startDBIDRestartServer(t, dir)
	for _, st := range []struct{ db, sql string }{
		{"template1", "CREATE TABLE t1_marker(x int)"},
		{"template1", "INSERT INTO t1_marker VALUES (7)"},
		{"postgres", "CREATE TABLE in_postgres(a int)"},
		{"postgres", "GRANT CREATE ON DATABASE template1 TO PUBLIC"},
		{"postgres", "CREATE DATABASE after_t1"},
	} {
		if err := exec(s1, st.db, st.sql); err != nil {
			s1.close(t)
			t.Fatalf("%s on %s: %v", st.sql, st.db, err)
		}
	}
	check := func(s *dbidRestartServer, phase string) {
		t.Helper()
		if _, err := count(s, "postgres", "SELECT count(*) FROM t1_marker"); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("%s: template1's table visible from postgres (err=%v)", phase, err)
		}
		if _, err := count(s, "template1", "SELECT count(*) FROM in_postgres"); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("%s: postgres' table visible from template1 (err=%v)", phase, err)
		}
		if n, err := count(s, "template1", "SELECT count(*) FROM t1_marker"); err != nil || n != 1 {
			t.Errorf("%s: template1's own table: n=%d err=%v", phase, n, err)
		}
		if n, err := count(s, "postgres", "SELECT count(*) FROM pg_database WHERE datname='template1' AND oid=1"); err != nil || n != 1 {
			t.Errorf("%s: template1's displayed pg_database.oid must stay 1: n=%d err=%v", phase, n, err)
		}
		if n, err := count(s, "postgres", "SELECT count(*) FROM pg_database WHERE datname='template1' AND datacl::text LIKE '%{=CTc/%'"); err != nil || n != 1 {
			t.Errorf("%s: GRANT CREATE ON DATABASE template1 TO PUBLIC not in datacl: n=%d err=%v", phase, n, err)
		}
		if n, err := count(s, "after_t1", "SELECT 1"); err != nil || n != 1 {
			t.Errorf("%s: database created from template1 is not connectable: err=%v", phase, err)
		}
	}
	check(s1, "before restart")
	s1.close(t)

	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)
	check(s2, "after restart")
}
