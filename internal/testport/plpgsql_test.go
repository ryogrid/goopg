package testport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// runSQL is a helper that runs SQL via the Go-native Query method
// and fatals on non-nil error returns.
func runSQL(t *testing.T, c *cluster.Cluster, sql string) [][]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := c.Query(ctx, sql)
	if err != nil {
		t.Fatalf("Query(%q): %v", sql, err)
	}
	return rows
}

// TestPort_PLpgSQLCreateCallProcedure exercises the full
// CREATE PROCEDURE -> CALL -> DROP PROCEDURE path via the
// Go-native wire-protocol query interface.
func TestPort_PLpgSQLCreateCallProcedure(t *testing.T) {
	c := newCluster(t, "plpgsql_proc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// CREATE PROCEDURE with no args, PL/pgSQL body.
	runSQL(t, c, `
		CREATE PROCEDURE hello() LANGUAGE plpgsql AS $$
		BEGIN
			RETURN 1;
		END $$
	`)

	// CALL the procedure.
	runSQL(t, c, "CALL hello()")

	// DROP PROCEDURE.
	runSQL(t, c, "DROP PROCEDURE hello()")
}

// TestPort_PLpgSQLCreateCallProcedureWithArgs exercises a procedure
// with IN arguments.
func TestPort_PLpgSQLCreateCallProcedureWithArgs(t *testing.T) {
	c := newCluster(t, "plpgsql_proc_args")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, `
		CREATE PROCEDURE greet(name text) LANGUAGE plpgsql AS $$
		BEGIN
			RETURN 1;
		END $$
	`)

	runSQL(t, c, "CALL greet('world')")

	runSQL(t, c, "DROP PROCEDURE greet(text)")
}

// TestPort_PLpgSQLCreateFunctionAndCallUDF exercises a PL/pgSQL
// function called from SELECT (the Stage A path), verifying
// the result value via the wire protocol.
func TestPort_PLpgSQLCreateFunctionAndCallUDF(t *testing.T) {
	c := newCluster(t, "plpgsql_func")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, `
		CREATE FUNCTION add_one(x int) RETURNS int LANGUAGE plpgsql AS $$
		BEGIN
			RETURN x + 1;
		END $$
	`)

	// Call the function from SELECT.
	rows := runSQL(t, c, "SELECT add_one(41)")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "42" {
		t.Fatalf("SELECT add_one(41) = %v, want [[42]]", rows)
	}

	runSQL(t, c, "DROP FUNCTION add_one(int)")
}

// TestPort_PLpgSQLCallFunctionInExprContext exercises a UDF called
// from a CASE expression, verifying the function-invocation
// expression-context path.
func TestPort_PLpgSQLCallFunctionInExprContext(t *testing.T) {
	c := newCluster(t, "plpgsql_expr")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, `
		CREATE FUNCTION is_positive(x int) RETURNS text LANGUAGE plpgsql AS $$
		BEGIN
			IF x > 0 THEN
				RETURN 'yes';
			ELSE
				RETURN 'no';
			END IF;
		END $$
	`)

	rows := runSQL(t, c,
		"SELECT CASE WHEN is_positive(5) = 'yes' THEN 'pass' ELSE 'fail' END")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "pass" {
		t.Fatalf("CASE expr result = %v, want [[pass]]", rows)
	}

	rows = runSQL(t, c,
		"SELECT CASE WHEN is_positive(-1) = 'yes' THEN 'fail' ELSE 'pass' END")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "pass" {
		t.Fatalf("CASE expr result = %v, want [[pass]]", rows)
	}

	runSQL(t, c, "DROP FUNCTION is_positive(int)")
}

// TestPort_PLpgSQLProcedureWithOutParams exercises a procedure
// with OUT parameters, verifying CALL returns the output row.
func TestPort_PLpgSQLProcedureWithOutParams(t *testing.T) {
	c := newCluster(t, "plpgsql_out")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, `
		CREATE PROCEDURE double_it(IN x int, OUT result int) LANGUAGE plpgsql AS $$
		BEGIN
			result := x * 2;
		END $$
	`)

	rows := runSQL(t, c, "CALL double_it(21)")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "42" {
		t.Fatalf("CALL double_it(21) = %v, want [[42]]", rows)
	}

	runSQL(t, c, "DROP PROCEDURE double_it(int, int)")
}

// TestPort_PLpgSQLDuplicateAndMissingProcedure exercises error
// handling: duplicate procedure and missing procedure.
func TestPort_PLpgSQLDuplicateAndMissingProcedure(t *testing.T) {
	c := newCluster(t, "plpgsql_errors")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Create once — should succeed.
	runSQL(t, c, "CREATE PROCEDURE p() LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$")

	// Duplicate — should fail.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Query(ctx, "CREATE PROCEDURE p() LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$")
	if err == nil {
		t.Fatal("expected duplicate CREATE to fail, but it succeeded")
	}

	// CALL to missing — should fail.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	_, err = c.Query(ctx2, "CALL nonexistent()")
	if err == nil {
		t.Fatal("expected CALL nonexistent to fail, but it succeeded")
	}

	// DROP IF EXISTS of nonexistent — should succeed.
	runSQL(t, c, "DROP PROCEDURE IF EXISTS nonexistent()")
}

// psqlPath returns the in-tree PostgreSQL psql path when available,
// or falls back to checking the system PATH. Returns empty string if
// no psql is found anywhere.
func psqlPath(t *testing.T) string {
	t.Helper()
	// Try the in-tree local_install first (see Makefile PG_BIN_DIR).
	root := repoRoot(t)
	local := filepath.Join(root, "postgres", "local_install", "bin", "psql")
	if s, err := os.Stat(local); err == nil && s.Mode().IsRegular() {
		return local
	}
	// Fall back to PATH lookup.
	p, err := exec.LookPath("psql")
	if err != nil {
		return ""
	}
	return p
}

// psqlCluster creates a cluster configured to use the in-tree psql.
func psqlCluster(t *testing.T, name string) *cluster.Cluster {
	t.Helper()
	psql := psqlPath(t)
	c, err := cluster.New(name, cluster.Options{
		RepoRoot:      repoRoot(t),
		DataDir:       filepath.Join(t.TempDir(), "data"),
		StartupWait:   20 * time.Second,
		ShutdownWait:  20 * time.Second,
		PSQLPath:      psql,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runPSQL runs psql against the cluster with the given args, setting
// LD_LIBRARY_PATH so the in-tree libpq is found.
func runPSQL(t *testing.T, c *cluster.Cluster, args ...string) util.CommandResult {
	t.Helper()
	root := repoRoot(t)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	addr := c.ListenAddr()
	host, port, _ := strings.Cut(addr, ":")
	psqlPath := filepath.Join(root, "postgres", "local_install", "bin", "psql")
	psqlArgs := append([]string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres"}, args...)
	res, err := util.RunCommand(util.CommandSpec{
		Name: psqlPath,
		Args: psqlArgs,
		Env:  []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestPort_PLpgSQLViaPsql exercises stored procedures through the
// psql CLI. Skipped when psql is not found.
func TestPort_PLpgSQLViaPsql(t *testing.T) {
	psql := psqlPath(t)
	if psql == "" {
		t.Skip("psql not installed")
	}
	c := psqlCluster(t, "plpgsql_psql")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// CREATE PROCEDURE and CALL via psql.
	res := runPSQL(t, c, "-Atqc", "CREATE PROCEDURE greet(name text) LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$")
	if res.ExitCode != 0 {
		t.Fatalf("CREATE PROCEDURE via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res = runPSQL(t, c, "-Atqc", "CALL greet('world')")
	if res.ExitCode != 0 {
		t.Fatalf("CALL via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res = runPSQL(t, c, "-Atqc", "DROP PROCEDURE greet(text)")
	if res.ExitCode != 0 {
		t.Fatalf("DROP PROCEDURE via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Function via psql.
	res = runPSQL(t, c, "-Atqc", "CREATE FUNCTION add_one(x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN x + 1; END $$")
	if res.ExitCode != 0 {
		t.Fatalf("CREATE FUNCTION via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res = runPSQL(t, c, "-Atqc", "SELECT add_one(41)")
	if res.ExitCode != 0 {
		t.Fatalf("SELECT via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "42" {
		t.Fatalf("add_one(41) via psql = %q, want 42", res.Stdout)
	}

	// OUT params via psql.
	res = runPSQL(t, c, "-Atqc", "CREATE PROCEDURE double_it(IN x int, OUT result int) LANGUAGE plpgsql AS $$ BEGIN result := x * 2; END $$")
	if res.ExitCode != 0 {
		t.Fatalf("CREATE PROCEDURE (OUT) via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res = runPSQL(t, c, "-Atqc", "CALL double_it(21)")
	if res.ExitCode != 0 {
		t.Fatalf("CALL OUT via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "42" {
		t.Fatalf("CALL double_it(21) via psql = %q, want 42", res.Stdout)
	}
}
