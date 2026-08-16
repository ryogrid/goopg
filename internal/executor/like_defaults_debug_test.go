package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestLIKEIncludingDefaultsDebug verifies that LIKE INCLUDING DEFAULTS
// copies the DefaultExpr from the source table and that INSERT DEFAULT VALUES
// uses it correctly.
func TestLIKEIncludingDefaultsDebug(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE inhx (xx text DEFAULT 'text')`); err != nil {
		t.Fatalf("create inhx: %v", err)
	}

	inhx, ok := cat.LookupTable(parser.ObjectName{Name: "inhx"})
	if !ok {
		t.Fatal("inhx not found")
	}
	if len(inhx.Columns) == 0 || inhx.Columns[0].DefaultExpr == nil {
		t.Error("inhx.xx has nil DefaultExpr")
	}

	if err := runDDL(t, ctx, `CREATE TABLE inhf (LIKE inhx INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`); err != nil {
		t.Fatalf("create inhf: %v", err)
	}

	inhf, ok := cat.LookupTable(parser.ObjectName{Name: "inhf"})
	if !ok {
		t.Fatal("inhf not found")
	}
	if len(inhf.Columns) == 0 || inhf.Columns[0].DefaultExpr == nil {
		t.Errorf("inhf.xx has nil DefaultExpr - LIKE INCLUDING DEFAULTS is broken")
	}

	runSQL(t, ctx, `INSERT INTO inhf DEFAULT VALUES`)
	rows := runSQL(t, ctx, `SELECT * FROM inhf`)
	if len(rows) != 1 || rows[0][0].StringValue() != "text" {
		t.Errorf("expected 1 row with 'text', got %v", rows)
	}
}

// TestLIKEDuplicateColumnError verifies that CREATE TABLE (LIKE t, LIKE t)
// raises "column specified more than once" (42701) when both LIKE sources
// add the same column name.
func TestLIKEDuplicateColumnError(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE inhx (xx text DEFAULT 'text')`); err != nil {
		t.Fatalf("create inhx: %v", err)
	}

	err := runDDL(t, ctx, `CREATE TABLE inhf (LIKE inhx, LIKE inhx)`)
	if err == nil {
		t.Fatal("expected error for duplicate LIKE, got nil")
	}
	if !strings.Contains(err.Error(), "specified more than once") {
		t.Errorf("expected 'specified more than once' error, got: %v", err)
	}
	t.Logf("Correctly got error: %v", err)
}

// TestLIKEMergesWithINHERITSColumns verifies that when a LIKE clause adds
// a column that was already inherited via INHERITS, a NOTICE is emitted
// rather than an error (PostgreSQL merge semantics).
func TestLIKEMergesWithINHERITSColumns(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE ctlt1 (a text, b text)`); err != nil {
		t.Fatalf("create ctlt1: %v", err)
	}

	// LIKE ctlt1 + INHERITS (ctlt1): overlapping columns → NOTICE, no error
	err := runDDL(t, ctx, `CREATE TABLE ctlt1_inh (LIKE ctlt1 INCLUDING CONSTRAINTS) INHERITS (ctlt1)`)
	if err != nil {
		t.Fatalf("LIKE + INHERITS overlap should not error, got: %v", err)
	}

	inh, ok := cat.LookupTable(parser.ObjectName{Name: "ctlt1_inh"})
	if !ok {
		t.Fatal("ctlt1_inh not found")
	}
	// Should have columns a and b (not duplicated)
	if len(inh.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d: %v", len(inh.Columns), inh.Columns)
	}
}

// runSQLErr attempts to run a SQL statement and returns any runtime or plan error.
func runSQLErr(t *testing.T, sql string) ([]Row, error) {
	t.Helper()
	cat := catalog.NewInMemory()
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], cat)
	if err != nil {
		return nil, err
	}
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	ctx := NewContext()
	ctx.Catalog = cat
	rows, err := Run(op, ctx)
	return rows, err
}

// TestAbsMinInt64RaisesOverflow verifies that abs(MinInt64) raises an error.
func TestAbsMinInt64RaisesOverflow(t *testing.T) {
	_, err := runSQLErr(t, `SELECT abs('-9223372036854775808'::int8)`)
	if err == nil {
		t.Fatal("expected bigint out of range error, got nil")
	}
	if !strings.Contains(err.Error(), "bigint out of range") {
		t.Errorf("unexpected error: %v", err)
	}
	t.Logf("Correctly got error: %v", err)
}

// TestConstFoldingInt8OverflowRaisesError verifies that constant expressions
// detect int64 arithmetic overflow and raise an error.
func TestConstFoldingInt8OverflowRaisesError(t *testing.T) {
	cases := []string{
		`SELECT '9223372036854775800'::int8 + '100'::int8`,
		`SELECT '-9223372036854775800'::int8 - '100'::int8`,
		`SELECT '9223372036854775800'::int8 * '1000'::int8`,
	}
	for _, sql := range cases {
		_, err := runSQLErr(t, sql)
		if err == nil {
			t.Errorf("expected overflow error for %q, got nil", sql)
			continue
		}
		if !strings.Contains(err.Error(), "bigint out of range") {
			t.Errorf("expected 'bigint out of range' for %q, got: %v", sql, err)
		} else {
			t.Logf("OK %q → %v", sql, err)
		}
	}
}

// TestInt8PlusInt4OverflowRaisesError verifies that int8 + int4 cross-type
// arithmetic detects overflow at runtime.
func TestInt8PlusInt4OverflowRaisesError(t *testing.T) {
	cases := []struct {
		sql    string
		expect string
	}{
		{`SELECT '9223372036854775800'::int8 + '100'::int4`, "bigint out of range"},
		{`SELECT '-9223372036854775800'::int8 - '100'::int4`, "bigint out of range"},
		{`SELECT '9223372036854775800'::int8 * '100'::int4`, "bigint out of range"},
		{`SELECT abs('-9223372036854775808'::int8)`, "bigint out of range"},
	}
	for _, tc := range cases {
		_, err := runSQLErr(t, tc.sql)
		if err == nil {
			t.Errorf("expected error for %q, got nil", tc.sql)
			continue
		}
		if !strings.Contains(err.Error(), tc.expect) {
			t.Errorf("expected %q in error for %q, got: %v", tc.expect, tc.sql, err)
		} else {
			t.Logf("OK: %q → error contains '%s'", tc.sql, tc.expect)
		}
	}
}
