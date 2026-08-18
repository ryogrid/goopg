package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCheckConstraintTableoidEnforcedOnInsert verifies M0134-0005aa bucket
// (A): a CHECK constraint referencing the tableoid system column is
// evaluated (not silently skipped) at INSERT time, matching PG's
// SYS_COL_CHECK_TBL case in constraints.sql. Before the fix, checkConstraints
// (internal/executor/operators_fk.go) re-planned the CHECK against a
// synthetic SELECT that only bound the table's real columns; tableoid failed
// to resolve, the planner error was silently swallowed by `continue`, and
// the violating row was accepted instead of raising 23514.
func TestCheckConstraintTableoidEnforcedOnInsert(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sys_col_check_tbl (city text, state text, is_capital bool,
		altitude int,
		CHECK (NOT (is_capital AND tableoid::regclass::text = 'sys_col_check_tbl')))`); err != nil {
		t.Fatalf("CREATE TABLE sys_col_check_tbl: %v (tableoid CHECK must be ACCEPTED at DDL time)", err)
	}

	// Non-capital row: tableoid::regclass::text = 'sys_col_check_tbl' is true,
	// but is_capital is false, so NOT (false AND true) = true -> must pass.
	if err := runDDL(t, ctx, `INSERT INTO sys_col_check_tbl VALUES ('Seattle', 'Washington', false, 100)`); err != nil {
		t.Fatalf("INSERT Seattle row: unexpected error %v", err)
	}

	// Capital row: is_capital is true and tableoid resolves the table's own
	// name, so NOT (true AND true) = false -> must raise 23514.
	err := runDDL(t, ctx, `INSERT INTO sys_col_check_tbl VALUES ('Olympia', 'Washington', true, 100)`)
	if err == nil {
		t.Fatal("INSERT Olympia row should violate the tableoid CHECK constraint (PG raises 23514)")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23514" {
		t.Errorf("Code = %q, want 23514", ee.Code)
	}
	if !strings.Contains(ee.Message, `relation "sys_col_check_tbl" violates check constraint`) {
		t.Errorf("Message = %q, want it to name the table and constraint", ee.Message)
	}

	rows := runQuery(t, ctx, `SELECT city FROM sys_col_check_tbl`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row in sys_col_check_tbl after the rejected INSERT, got %d: %v", len(rows), rows)
	}
}

// TestCheckConstraintSystemColumnRejectedAtCreateTable verifies M0134-0005aa
// bucket (B): a CHECK expression referencing a system column other than
// tableoid (ctid here) is rejected at CREATE TABLE time with SQLSTATE 42703,
// mirroring PG's scanNSItemForColumn EXPR_KIND_CHECK_CONSTRAINT gate
// (postgres/src/backend/parser/parse_relation.c:707-713).
func TestCheckConstraintSystemColumnRejectedAtCreateTable(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE TABLE sys_col_check_tbl2 (city text, state text, is_capital bool,
		altitude int,
		CHECK (NOT (is_capital AND ctid::text = 'sys_col_check_tbl2')))`)
	if err == nil {
		t.Fatal("CREATE TABLE with a ctid CHECK should be rejected")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42703" {
		t.Errorf("Code = %q, want 42703", ee.Code)
	}
	if ee.Message != `system column "ctid" reference in check constraint is invalid` {
		t.Errorf("Message = %q, want PG's exact wording", ee.Message)
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "sys_col_check_tbl2"}); ok {
		t.Fatal("sys_col_check_tbl2 must not exist after the rejected CREATE TABLE")
	}
}

// TestCheckConstraintSystemColumnRejectedAtAlterTable is the ALTER TABLE
// sibling of TestCheckConstraintSystemColumnRejectedAtCreateTable — the same
// 42703 gate must fire for `ALTER TABLE ... ADD CONSTRAINT ... CHECK`, not
// just CREATE TABLE. M0134-0005aa.
func TestCheckConstraintSystemColumnRejectedAtAlterTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sys_col_check_tbl3 (city text, is_capital bool)`); err != nil {
		t.Fatalf("CREATE TABLE sys_col_check_tbl3: %v", err)
	}
	err := runDDL(t, ctx, `ALTER TABLE sys_col_check_tbl3 ADD CONSTRAINT bad_chk CHECK (ctid::text <> '')`)
	if err == nil {
		t.Fatal("ALTER TABLE ADD CONSTRAINT with a ctid CHECK should be rejected")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42703" {
		t.Errorf("Code = %q, want 42703", ee.Code)
	}
	if ee.Message != `system column "ctid" reference in check constraint is invalid` {
		t.Errorf("Message = %q, want PG's exact wording", ee.Message)
	}
}

// TestCheckConstraintTableoidAcceptedAtAlterTable guards against
// over-rejection: tableoid (the one permitted system column) must still be
// accepted by ADD CONSTRAINT ... CHECK, not swept up by the ctid/xmin/xmax/
// cmin/cmax gate. M0134-0005aa.
func TestCheckConstraintTableoidAcceptedAtAlterTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sys_col_check_tbl4 (city text)`); err != nil {
		t.Fatalf("CREATE TABLE sys_col_check_tbl4: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE sys_col_check_tbl4 ADD CONSTRAINT ok_chk CHECK (tableoid::regclass::text = 'sys_col_check_tbl4')`); err != nil {
		t.Fatalf("ALTER TABLE ADD CONSTRAINT with a tableoid CHECK: unexpected error %v", err)
	}
}
