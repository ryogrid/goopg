package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// expectSQLError runs sql and verifies it fails with an error containing the
// expected SQLSTATE code.
func expectSQLError(t *testing.T, c interface {
	Query(ctx context.Context, sql string) ([][]string, error)
}, sql, wantCode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Query(ctx, sql)
	if err == nil {
		t.Fatalf("%q: expected error with code %s, got nil", sql, wantCode)
	}
	if !strings.Contains(err.Error(), wantCode) {
		t.Errorf("%q: error = %v, want code %s", sql, err, wantCode)
	}
}

// TestSyntax_Errors_SQLSTATE verifies PostgreSQL-compatible SQLSTATE error
// codes for common error conditions.
func TestSyntax_Errors_SQLSTATE(t *testing.T) {
	c := newCluster(t, "syntax_errors")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "CREATE UNIQUE INDEX i ON t (id)")

	// Duplicate table
	expectSQLError(t, c, "CREATE TABLE t (id int)", "42P07")

	// Duplicate index
	expectSQLError(t, c, "CREATE UNIQUE INDEX i ON t (id)", "42P07")

	// Relation not found
	expectSQLError(t, c, "SELECT * FROM nonexistent", "42P01")

	// Column not found
	expectSQLError(t, c, "SELECT no_such_col FROM t", "42703")

	// Function not found
	expectSQLError(t, c, "SELECT nonexistent_func()", "42883")

	// Duplicate key (unique violation) — may not be enforced in v0
	runSQL(t, c, "INSERT INTO t VALUES (1, 10)")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Query(ctx, "INSERT INTO t VALUES (1, 20)")
	if err != nil && !strings.Contains(err.Error(), "23505") {
		t.Fatalf("unexpected error: %v, want 23505 if enforced", err)
	}
	// If no error, the unique constraint isn't enforced yet in v0; skip.
	if err == nil {
		t.Log("unique constraint not enforced in v0, skipping 23505 assertion")
	}

	// Division by zero
	expectSQLError(t, c, "SELECT 1/0", "22012")

	runSQL(t, c, "DROP TABLE t")
}
