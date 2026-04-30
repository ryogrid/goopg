package testport

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
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

// TestPort_PLpgSQLViaPsql is the psql-based counterpart that
// exercises stored procedures through the psql CLI. Skipped
// when psql is not on PATH.
func TestPort_PLpgSQLViaPsql(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed")
	}
	c := newCluster(t, "plpgsql_psql")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Create, call, drop via psql.
	res, err := c.PSQL("-Atqc", "CREATE PROCEDURE greet(name text) LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("CREATE PROCEDURE via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res, err = c.PSQL("-Atqc", "CALL greet('world')")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("CALL via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res, err = c.PSQL("-Atqc", "DROP PROCEDURE greet(text)")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("DROP PROCEDURE via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Also test a function via psql.
	res, err = c.PSQL("-Atqc", "CREATE FUNCTION add_one(x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN x + 1; END $$")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("CREATE FUNCTION via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res, err = c.PSQL("-Atqc", "SELECT add_one(41)")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("SELECT via psql exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "42" {
		t.Fatalf("add_one(41) via psql = %q, want 42", res.Stdout)
	}
}
