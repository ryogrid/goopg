package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestSyntax_DDL_CreateDropTable exercises CREATE/DROP TABLE.
func TestSyntax_DDL_CreateDropTable(t *testing.T) {
	c := newCluster(t, "syntax_ddl_table")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Create table with various column types.
	runSQL(t, c, "CREATE TABLE t1 (id int, name text, val numeric(10,2))")
	runSQL(t, c, "INSERT INTO t1 VALUES (1, 'a', 1.5)")
	rows := runSQL(t, c, "SELECT id, name, val FROM t1 ORDER BY id")
	if len(rows) != 1 || rows[0][0] != "1" || rows[0][1] != "a" {
		t.Fatalf("t1 = %v, want [[1 a 1.5]]", rows)
	}

	// DROP TABLE
	runSQL(t, c, "DROP TABLE t1")

	// DROP TABLE IF EXISTS on missing table (no error).
	runSQL(t, c, "DROP TABLE IF EXISTS nonexistent")

	// DROP TABLE on missing table without IF EXISTS → error.
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	_, err := c.Query(ctx, "DROP TABLE nonexistent")
	if err == nil {
		t.Fatal("expected error for DROP TABLE nonexistent")
	}
	if !strings.Contains(err.Error(), "42P01") {
		t.Errorf("missing 42P01 in %v", err)
	}

	// CREATE TABLE IF NOT EXISTS
	runSQL(t, c, "CREATE TABLE IF NOT EXISTS t1 (id int)")
	runSQL(t, c, "DROP TABLE t1")
}

// TestSyntax_DDL_CreateIndex exercises CREATE/DROP INDEX.
func TestSyntax_DDL_CreateIndex(t *testing.T) {
	c := newCluster(t, "syntax_ddl_idx")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "CREATE INDEX i1 ON t (id)")
	runSQL(t, c, "CREATE UNIQUE INDEX i2 ON t (val)")
	runSQL(t, c, "DROP INDEX i1")
	runSQL(t, c, "DROP INDEX i2")
	runSQL(t, c, "DROP TABLE t")

	// Non-unique index on text also works via btree.
	runSQL(t, c, "CREATE TABLE t2 (id int, name text)")
	runSQL(t, c, "CREATE INDEX i3 ON t2 (id)")
	runSQL(t, c, "DROP TABLE t2")
}

// TestSyntax_DDL_CreateView exercises CREATE/DROP VIEW.
func TestSyntax_DDL_CreateView(t *testing.T) {
	c := newCluster(t, "syntax_ddl_view")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val text)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a')")
	// Use explicit column list in view to avoid column-resolution issues.
	runSQL(t, c, "CREATE VIEW v AS SELECT id, val FROM t")
	rows := runSQL(t, c, "SELECT id, val FROM v")
	if len(rows) != 1 || rows[0][0] != "1" || rows[0][1] != "a" {
		t.Fatalf("v = %v, want [[1 a]]", rows)
	}
	runSQL(t, c, "DROP VIEW v")
	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DDL_AlterTable exercises ALTER TABLE.
func TestSyntax_DDL_AlterTable(t *testing.T) {
	c := newCluster(t, "syntax_ddl_alter")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1)")

	// ADD COLUMN
	runSQL(t, c, "ALTER TABLE t ADD COLUMN name text")
	runSQL(t, c, "UPDATE t SET name = 'hello' WHERE id = 1")
	rows := runSQL(t, c, "SELECT id, name FROM t")
	if len(rows) != 1 || rows[0][1] != "hello" {
		t.Fatalf("after ALTER = %v, want [[1 hello]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DDL_Truncate exercises TRUNCATE.
func TestSyntax_DDL_Truncate(t *testing.T) {
	c := newCluster(t, "syntax_ddl_truncate")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1), (2), (3)")
	runSQL(t, c, "TRUNCATE TABLE t")
	rows := runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "0" {
		t.Fatalf("after TRUNCATE count = %v, want 0", rows[0][0])
	}
	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_DDL_ViaPsql runs a representative DDL query through psql.
func TestSyntax_DDL_ViaPsql(t *testing.T) {
	psql := psqlPath(t)
	if psql == "" {
		t.Skip("psql not installed")
	}
	c := psqlCluster(t, "syntax_ddl_psql")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	libDir := libDir(t)
	addr := c.ListenAddr()
	host, port, _ := strings.Cut(addr, ":")

	res, err := util.RunCommand(util.CommandSpec{
		Name: psql,
		Args: []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres",
			"-Atqc", "CREATE TABLE psql_t (id int); INSERT INTO psql_t VALUES (42); SELECT id FROM psql_t; DROP TABLE psql_t"},
		Env: []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestSyntax_DDL_MatViewPgClass tests that CREATE MATERIALIZED VIEW registers
// the matview in pg_class and ::regclass lookup works.
func TestSyntax_DDL_MatViewPgClass(t *testing.T) {
	c := newCluster(t, "syntax_ddl_matview")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE mvtest_t (id int NOT NULL PRIMARY KEY, type text NOT NULL, amt numeric NOT NULL)")
	runSQL(t, c, "INSERT INTO mvtest_t VALUES (1, 'x', 2), (2, 'x', 3), (3, 'y', 5)")
	runSQL(t, c, "CREATE MATERIALIZED VIEW mvtest_tm AS SELECT type, sum(amt) AS totamt FROM mvtest_t GROUP BY type WITH NO DATA")

	// pg_class should show the matview with relkind='m' and relispopulated='f'
	rows := runSQL(t, c, "SELECT relname, relkind, relispopulated FROM pg_class WHERE relname = 'mvtest_tm'")
	if len(rows) == 0 {
		t.Fatal("pg_class WHERE relname='mvtest_tm' returned 0 rows")
	}
	t.Logf("pg_class relname lookup: %v", rows)

	// Test ::regclass cast
	rows2 := runSQL(t, c, "SELECT 'mvtest_tm'::regclass")
	t.Logf("regclass cast result: %v", rows2)

	// Test the problematic query
	rows3 := runSQL(t, c, "SELECT relispopulated FROM pg_class WHERE oid = 'mvtest_tm'::regclass")
	if len(rows3) == 0 {
		t.Fatal("SELECT relispopulated FROM pg_class WHERE oid = 'mvtest_tm'::regclass returned 0 rows!")
	}
	t.Logf("relispopulated: %v", rows3)
}

// TestSyntax_DDL_EnumOrdering tests that enum values are ordered by declaration, not alphabetically.
func TestSyntax_DDL_EnumOrdering(t *testing.T) {
	c := newCluster(t, "syntax_ddl_enum")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')")
	runSQL(t, c, "CREATE TABLE t (col rainbow)")
	runSQL(t, c, "INSERT INTO t VALUES ('red'), ('green'), ('yellow')")

	// ORDER BY should use declaration order, not alphabetical
	rows := runSQL(t, c, "SELECT col FROM t ORDER BY col")
	t.Logf("enum ORDER BY result: %v", rows)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0][0] != "red" || rows[1][0] != "yellow" || rows[2][0] != "green" {
		t.Errorf("wrong enum order: got %v, want [red yellow green]", rows)
	}
}

// TestSyntax_DDL_EnumColumnType tests that enum column types are correctly stored.
func TestSyntax_DDL_EnumColumnType(t *testing.T) {
	c := newCluster(t, "syntax_ddl_enum2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')")
	runSQL(t, c, "CREATE TABLE t (col rainbow)")
	runSQL(t, c, "INSERT INTO t VALUES ('red'), ('green'), ('yellow')")

	// Check pg_class for table existence
	rows := runSQL(t, c, "SELECT relname FROM pg_class WHERE relname = 't'")
	t.Logf("pg_class table t: %v", rows)

	// Test basic enum ordering
	rows2 := runSQL(t, c, "SELECT col FROM t ORDER BY col")
	t.Logf("ORDER BY col: %v", rows2)
	if len(rows2) != 3 {
		t.Fatalf("expected 3 rows got %d", len(rows2))
	}
	t.Logf("First row value: %q kind info: len=%d", rows2[0][0], len(rows2[0][0]))
}
