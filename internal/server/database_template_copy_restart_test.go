package server

// End-to-end coverage for CREATE DATABASE ... TEMPLATE's real relation-copy
// mechanism (M0122-0007 4e's last item, database_ddl.go's copyTemplateTables),
// mirroring table_dbid_restart_test.go's real wire-protocol + real data-dir
// round-trip shape (M0122-0007 4e follow-up 39) but for a TEMPLATE copy
// instead of a plain CREATE TABLE.
//
// Before this, CREATE DATABASE ... TEMPLATE against a template with any user
// table rejected outright with FeatureNotSupported (resolveCreateDatabaseTemplate's
// prior "must be empty" check) — TEMPLATE never actually copied anything.
// This test proves the bounded plain-table case now does: a table WITH DATA
// created via the real wire protocol/executor (so its physical file and
// catalog-heap rows are real, not test-fixture shortcuts) survives being
// copied into a fresh database, both immediately and after a full server
// restart from the same data directory.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestCreateDatabaseTemplatePlainTableCopiesDataAndSurvivesRestart: CREATE
// DATABASE ... TEMPLATE against a source database containing one plain,
// unindexed heap table (with rows inserted through the real executor) copies
// that table — schema AND data — into the new database. The copy is visible
// immediately and, after a full data-dir round trip, still visible and
// unchanged: proof the copy is durable (real pg_class/pg_attribute catalog-
// heap rows + a real physical relation file), not just an in-memory
// illusion that a restart would silently drop.
func TestCreateDatabaseTemplatePlainTableCopiesDataAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE tmpl_src"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE tmpl_src: %v", err)
	}

	src := s1.open(t, "tmpl_src")
	for _, stmt := range []string{
		"CREATE TABLE t_copy(a int, b text)",
		"INSERT INTO t_copy VALUES (1,'x'),(2,'y'),(3,'z')",
	} {
		if _, err := src.ExecContext(ctx, stmt); err != nil {
			src.Close()
			pg.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := pg.ExecContext(ctx, "CREATE DATABASE tmpl_copy TEMPLATE tmpl_src"); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE tmpl_copy TEMPLATE tmpl_src: %v", err)
	}

	assertTCopyRows := func(db *sql.DB, label string) {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT a, b FROM t_copy ORDER BY a")
		if err != nil {
			t.Fatalf("%s: SELECT t_copy: %v", label, err)
		}
		defer rows.Close()
		var got []struct {
			a int
			b string
		}
		for rows.Next() {
			var r struct {
				a int
				b string
			}
			if err := rows.Scan(&r.a, &r.b); err != nil {
				t.Fatalf("%s: scan: %v", label, err)
			}
			got = append(got, r)
		}
		if len(got) != 3 || got[0].a != 1 || got[0].b != "x" ||
			got[1].a != 2 || got[1].b != "y" || got[2].a != 3 || got[2].b != "z" {
			t.Fatalf("%s: rows = %+v, want [(1,x) (2,y) (3,z)]", label, got)
		}
	}

	// Immediate visibility (no restart): the copy's table AND its rows are
	// visible in the new database, and the source is untouched.
	cpy := s1.open(t, "tmpl_copy")
	assertTCopyRows(cpy, "tmpl_copy (pre-restart)")
	assertTCopyRows(src, "tmpl_src (pre-restart, source unaffected)")
	cpy.Close()
	src.Close()
	pg.Close()
	s1.close(t)

	// Full restart from the same data dir.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	cpy = s2.open(t, "tmpl_copy")
	defer cpy.Close()
	assertTCopyRows(cpy, "tmpl_copy (post-restart)")

	src2 := s2.open(t, "tmpl_src")
	defer src2.Close()
	assertTCopyRows(src2, "tmpl_src (post-restart, source unaffected)")

	// Nothing leaked into the postgres namespace (mirrors
	// TestDistinctDatabaseTableSurvivesRestartInOwnNamespace's own leak probe).
	pg2 := s2.open(t, "postgres")
	defer pg2.Close()
	var leaked int
	if err := pg2.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_class WHERE relname = 't_copy'").Scan(&leaked); err != nil {
		t.Fatalf("pg_class leak probe: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("t_copy leaked into postgres pg_class (count=%d, want 0)", leaked)
	}
}
