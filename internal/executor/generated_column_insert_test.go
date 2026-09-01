package executor

import (
	"strings"
	"testing"
)

// TestInsertGeneratedColumnComputedBeforeNotNullCheck: M0134-0187. Real
// PostgreSQL computes GENERATED ALWAYS AS … STORED columns (nodeModifyTable.c
// ExecComputeStoredGenerated) AFTER before-row triggers but BEFORE any
// constraint enforcement (ExecConstraints). goopg used to compute them right
// before partition routing — well after the NOT NULL check — so a NOT
// NULL-declared generated column with a non-null result (e.g.
// `nullif(a, 0)` on a=1) raised a false "null value … violates not-null
// constraint" because the check saw the placeholder NULL, not the computed
// value. Mirrors generated_stored.sql's gtest21a/gtest21b cases.
func TestInsertGeneratedColumnComputedBeforeNotNullCheck(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE gtest21a (a int PRIMARY KEY, b int GENERATED ALWAYS AS (nullif(a, 0)) STORED NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// a=1 -> nullif(1,0)=1, a non-null result; must NOT trip the NOT NULL
	// check on b.
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest21a (a) VALUES (1)`); err != nil {
		t.Fatalf("INSERT (a) VALUES (1): unexpected error: %v", err)
	}
	rows := runSQL(t, ctx, `SELECT a, b FROM gtest21a WHERE a = 1`)
	if len(rows) != 1 || rows[0][1].Kind != KindInt || rows[0][1].Int != 1 {
		t.Fatalf("row=%v want (1,1)", rows)
	}

	// a=0 -> nullif(0,0)=NULL, which genuinely violates NOT NULL.
	_, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest21a (a) VALUES (0)`)
	if err == nil {
		t.Fatal("INSERT (a) VALUES (0): want NOT NULL violation, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "23502" {
		t.Errorf("err=%v want ExecError 23502", err)
	}
}

// TestInsertGeneratedColumnAcceptsDefaultRejectsValue: M0134-0187. The
// implicit column list includes a GENERATED ALWAYS AS … STORED column, so
// `INSERT INTO t VALUES (v, DEFAULT)` (explicit DEFAULT in the generated
// column's own position) must be accepted and computed, while `INSERT INTO
// t VALUES (v, v2)` (a real value there) must be rejected — even when v2
// happens to equal what the expression would compute. Mirrors
// generated_stored.sql's gtest1 cases.
func TestInsertGeneratedColumnAcceptsDefaultRejectsValue(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE gtest1 (a int PRIMARY KEY, b int GENERATED ALWAYS AS (a * 2) STORED)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// Shorter row, no explicit column list: generated column implicitly
	// defaulted.
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest1 VALUES (1)`); err != nil {
		t.Fatalf("INSERT VALUES (1): unexpected error: %v", err)
	}
	// Explicit DEFAULT in the generated column's position: accepted.
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest1 VALUES (2, DEFAULT)`); err != nil {
		t.Fatalf("INSERT VALUES (2, DEFAULT): unexpected error: %v", err)
	}
	rows := runSQL(t, ctx, `SELECT a, b FROM gtest1 ORDER BY a`)
	if len(rows) != 2 || rows[0][1].Int != 2 || rows[1][1].Int != 4 {
		t.Fatalf("rows=%v want b=(2,4)", rows)
	}
	// A real (non-DEFAULT) value in the generated column's position: rejected.
	_, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest1 VALUES (3, 33)`)
	if err == nil {
		t.Fatal("INSERT VALUES (3, 33): want error, got nil")
	}
	if want := `cannot insert a non-DEFAULT value into column "b"`; !strings.Contains(err.Error(), want) {
		t.Errorf("err=%v want message containing %q", err, want)
	}
	// The rejected row must not have been written.
	rows = runSQL(t, ctx, `SELECT a FROM gtest1 WHERE a = 3`)
	if len(rows) != 0 {
		t.Errorf("rows=%v want none for rejected insert", rows)
	}
}

// TestGeneratedColumnEvaluatesCoalesce: M0134-0187 — evalGenFuncCall's
// whitelist previously had no coalesce arm, so any GENERATED ALWAYS AS
// expression using it silently fell through to NullDatum (indistinguishable
// from a legitimately NULL result) instead of evaluating the first
// non-null argument, mirroring evalExprSlot's "coalesce" case in expr.go.
func TestGeneratedColumnEvaluatesCoalesce(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE gtest_coalesce (a int, b int GENERATED ALWAYS AS (coalesce(a, -1)) STORED)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO gtest_coalesce (a) VALUES (NULL), (5)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows := runSQL(t, ctx, `SELECT a, b FROM gtest_coalesce ORDER BY b`)
	if len(rows) != 2 || rows[0][1].Int != -1 || rows[1][1].Int != 5 {
		t.Fatalf("rows=%v want b=(-1,5)", rows)
	}
}
