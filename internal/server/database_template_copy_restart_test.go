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


// TestCreateDatabaseTemplateSequenceCopiesStateAndSurvivesRestart:
// CREATE DATABASE ... TEMPLATE against a source database containing a
// sequence (with real nextval() activity through the real executor) copies
// the sequence's live counter state — not just its definition — into the new
// database, as an independent counter (advancing one must not move the
// other), both immediately and after a full data-dir round trip. Mirrors
// TestCreateDatabaseTemplatePlainTableCopiesDataAndSurvivesRestart's shape
// for M0122-0007 4e follow-up 41 (sequence cloning has no relation file, so
// this is a separate code path — database_ddl.go's copyTemplateSequences —
// from the plain-table one, exercised here end to end for the first time).
func TestCreateDatabaseTemplateSequenceCopiesStateAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE seq_tmpl_src"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE seq_tmpl_src: %v", err)
	}

	src := s1.open(t, "seq_tmpl_src")
	if _, err := src.ExecContext(ctx, "CREATE SEQUENCE s_copy START 1"); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE SEQUENCE s_copy: %v", err)
	}
	var srcVal int64
	if err := src.QueryRowContext(ctx, "SELECT nextval('s_copy')").Scan(&srcVal); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on source: %v", err)
	}
	if srcVal != 1 {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on source = %d, want 1", srcVal)
	}

	if _, err := pg.ExecContext(ctx, "CREATE DATABASE seq_tmpl_copy TEMPLATE seq_tmpl_src"); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE seq_tmpl_copy TEMPLATE seq_tmpl_src: %v", err)
	}

	// Immediate visibility (no restart): the clone continues exactly from
	// the source's counter (1) at copy time.
	cpy := s1.open(t, "seq_tmpl_copy")
	var cpyVal int64
	if err := cpy.QueryRowContext(ctx, "SELECT nextval('s_copy')").Scan(&cpyVal); err != nil {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on copy (pre-restart): %v", err)
	}
	if cpyVal != 2 {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on copy (pre-restart) = %d, want 2", cpyVal)
	}

	// Advancing the copy must not affect the source's own counter.
	if err := src.QueryRowContext(ctx, "SELECT nextval('s_copy')").Scan(&srcVal); err != nil {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on source (post-copy): %v", err)
	}
	if srcVal != 2 {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("nextval(s_copy) on source (post-copy) = %d, want 2 (independent counter, not aliased to the copy)", srcVal)
	}

	cpy.Close()
	src.Close()
	pg.Close()
	s1.close(t)

	// Full restart from the same data dir. Post-restart values only need to
	// continue strictly above their own pre-restart value — nextval
	// pre-logs 32 values ahead (upstream SEQ_LOG_VALS), so an exact +1 is
	// not guaranteed across a restart (same gap semantics
	// TestPort_SerialSequenceSurvivesRestart already documents).
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	cpy2 := s2.open(t, "seq_tmpl_copy")
	defer cpy2.Close()
	var cpyValAfterRestart int64
	if err := cpy2.QueryRowContext(ctx, "SELECT nextval('s_copy')").Scan(&cpyValAfterRestart); err != nil {
		t.Fatalf("nextval(s_copy) on copy (post-restart): %v", err)
	}
	if cpyValAfterRestart <= 2 {
		t.Fatalf("nextval(s_copy) on copy (post-restart) = %d, want > 2 (durable continuation)", cpyValAfterRestart)
	}

	src2 := s2.open(t, "seq_tmpl_src")
	defer src2.Close()
	var srcValAfterRestart int64
	if err := src2.QueryRowContext(ctx, "SELECT nextval('s_copy')").Scan(&srcValAfterRestart); err != nil {
		t.Fatalf("nextval(s_copy) on source (post-restart): %v", err)
	}
	if srcValAfterRestart <= 2 {
		t.Fatalf("nextval(s_copy) on source (post-restart) = %d, want > 2 (source's own durable counter, unaffected by the copy)", srcValAfterRestart)
	}
}


// TestCreateDatabaseTemplateViewCopiesQueryAndSurvivesRestart: CREATE
// DATABASE ... TEMPLATE against a source database containing a plain
// (non-materialized) view — created through the real executor, referencing a
// plain table also present in the template — copies both the table and the
// view into the new database as independent objects, both immediately and
// after a full data-dir round trip. Mirrors
// TestCreateDatabaseTemplateSequenceCopiesStateAndSurvivesRestart's shape for
// M0122-0007 4e follow-up 42 (view cloning has no relation file of its own,
// so this is a separate code path — database_ddl.go's copyTemplateViews —
// exercised here end to end for the first time). Also confirms the two
// databases' views are independent objects: DROP VIEW in one leaves the
// other's view (and its underlying data) intact.
func TestCreateDatabaseTemplateViewCopiesQueryAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE view_tmpl_src"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE view_tmpl_src: %v", err)
	}

	src := s1.open(t, "view_tmpl_src")
	for _, stmt := range []string{
		"CREATE TABLE t_view(a int, b text)",
		"INSERT INTO t_view VALUES (1,'x'),(2,'y'),(3,'z')",
		"CREATE VIEW v_copy AS SELECT a, b FROM t_view WHERE a > 1",
	} {
		if _, err := src.ExecContext(ctx, stmt); err != nil {
			src.Close()
			pg.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := pg.ExecContext(ctx, "CREATE DATABASE view_tmpl_copy TEMPLATE view_tmpl_src"); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE view_tmpl_copy TEMPLATE view_tmpl_src: %v", err)
	}

	assertVCopyRows := func(db *sql.DB, label string) {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT a, b FROM v_copy ORDER BY a")
		if err != nil {
			t.Fatalf("%s: SELECT v_copy: %v", label, err)
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
		if len(got) != 2 || got[0].a != 2 || got[0].b != "y" || got[1].a != 3 || got[1].b != "z" {
			t.Fatalf("%s: rows = %+v, want [(2,y) (3,z)]", label, got)
		}
	}

	// Immediate visibility (no restart): the view's query works against the
	// copied table in the new database, and the source is untouched.
	cpy := s1.open(t, "view_tmpl_copy")
	assertVCopyRows(cpy, "view_tmpl_copy (pre-restart)")
	assertVCopyRows(src, "view_tmpl_src (pre-restart, source unaffected)")

	// The two databases' views are independent objects: dropping the copy's
	// view must not touch the source's.
	if _, err := cpy.ExecContext(ctx, "DROP VIEW v_copy"); err != nil {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("DROP VIEW v_copy on copy: %v", err)
	}
	assertVCopyRows(src, "view_tmpl_src (after dropping the copy's view, source unaffected)")
	cpy.Close()
	src.Close()
	pg.Close()
	s1.close(t)

	// Full restart from the same data dir: the source's view (never
	// dropped) must still resolve and return the same rows.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	src2 := s2.open(t, "view_tmpl_src")
	defer src2.Close()
	assertVCopyRows(src2, "view_tmpl_src (post-restart)")

	// Nothing leaked into the postgres namespace.
	pg2 := s2.open(t, "postgres")
	defer pg2.Close()
	var leaked int
	if err := pg2.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_class WHERE relname = 'v_copy'").Scan(&leaked); err != nil {
		t.Fatalf("pg_class leak probe: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("v_copy leaked into postgres pg_class (count=%d, want 0)", leaked)
	}
}

// TestCreateDatabaseTemplateMatViewCopiesDataAndSurvivesRestart: CREATE
// DATABASE ... TEMPLATE against a source database containing a materialized
// view — created and populated through the real executor — copies both its
// defining query AND its already-materialized heap data into the new
// database, both immediately and after a full data-dir round trip. Mirrors
// TestCreateDatabaseTemplateViewCopiesQueryAndSurvivesRestart's shape for
// M0122-0007 4e follow-up 43 (a materialized view combines the plain-table
// case's physical relation-file copy with the plain-view case's AST/
// ViewDef/IsPopulated field copy — database_ddl.go's copyTemplateMatViews —
// exercised here end to end for the first time). Also confirms the two
// databases' matviews are physically independent: REFRESH in the copy after
// changing its own underlying table must not alter the source's
// still-materialized rows.
func TestCreateDatabaseTemplateMatViewCopiesDataAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE mv_tmpl_src"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE mv_tmpl_src: %v", err)
	}

	src := s1.open(t, "mv_tmpl_src")
	for _, stmt := range []string{
		"CREATE TABLE t_mv(a int, b text)",
		"INSERT INTO t_mv VALUES (1,'x'),(2,'y'),(3,'z')",
		"CREATE MATERIALIZED VIEW mv_copy AS SELECT a, b FROM t_mv WHERE a > 1",
	} {
		if _, err := src.ExecContext(ctx, stmt); err != nil {
			src.Close()
			pg.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := pg.ExecContext(ctx, "CREATE DATABASE mv_tmpl_copy TEMPLATE mv_tmpl_src"); err != nil {
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE mv_tmpl_copy TEMPLATE mv_tmpl_src: %v", err)
	}

	assertMVCopyRows := func(db *sql.DB, label string, want [][2]any) {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT a, b FROM mv_copy ORDER BY a")
		if err != nil {
			t.Fatalf("%s: SELECT mv_copy: %v", label, err)
		}
		defer rows.Close()
		var got [][2]any
		for rows.Next() {
			var a int
			var b string
			if err := rows.Scan(&a, &b); err != nil {
				t.Fatalf("%s: scan: %v", label, err)
			}
			got = append(got, [2]any{a, b})
		}
		if len(got) != len(want) {
			t.Fatalf("%s: rows = %+v, want %+v", label, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: rows = %+v, want %+v", label, got, want)
			}
		}
	}

	want := [][2]any{{2, "y"}, {3, "z"}}

	// Immediate visibility (no restart): the copy's matview already carries
	// the source's materialized data (not just the defining query), and the
	// source is untouched.
	cpy := s1.open(t, "mv_tmpl_copy")
	assertMVCopyRows(cpy, "mv_tmpl_copy (pre-restart)", want)
	assertMVCopyRows(src, "mv_tmpl_src (pre-restart, source unaffected)", want)

	// Physical independence: changing the copy's own underlying table and
	// refreshing its matview must not alter the source's still-materialized
	// rows.
	if _, err := cpy.ExecContext(ctx, "INSERT INTO t_mv VALUES (4,'w')"); err != nil {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("INSERT INTO t_mv on copy: %v", err)
	}
	if _, err := cpy.ExecContext(ctx, "REFRESH MATERIALIZED VIEW mv_copy"); err != nil {
		cpy.Close()
		src.Close()
		pg.Close()
		s1.close(t)
		t.Fatalf("REFRESH MATERIALIZED VIEW mv_copy on copy: %v", err)
	}
	assertMVCopyRows(cpy, "mv_tmpl_copy (after its own insert+refresh)", [][2]any{{2, "y"}, {3, "z"}, {4, "w"}})
	assertMVCopyRows(src, "mv_tmpl_src (after the copy's insert+refresh, source unaffected)", want)
	cpy.Close()
	src.Close()
	pg.Close()
	s1.close(t)

	// Full restart from the same data dir: the source's matview (never
	// refreshed) must still resolve and return the same materialized rows.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	src2 := s2.open(t, "mv_tmpl_src")
	defer src2.Close()
	assertMVCopyRows(src2, "mv_tmpl_src (post-restart)", want)

	// Nothing leaked into the postgres namespace.
	pg2 := s2.open(t, "postgres")
	defer pg2.Close()
	var leaked int
	if err := pg2.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_class WHERE relname = 'mv_copy'").Scan(&leaked); err != nil {
		t.Fatalf("pg_class leak probe: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("mv_copy leaked into postgres pg_class (count=%d, want 0)", leaked)
	}
}
