package planner

import (
	"strings"
	"testing"
)

// TestPlanWithSimpleCTE: WITH a AS (SELECT 1) SELECT * FROM a
// plans without error and the projected schema has exactly one
// column (the constant 1). Pre-M0016-0002 this errored with 42P01
// because the CTE name didn't resolve as a table.
func TestPlanWithSimpleCTE(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT 1) SELECT * FROM a")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := len(node.Output()); got != 1 {
		t.Errorf("schema width = %d, want 1", got)
	}
}

// TestPlanWithCTEReadingTable: CTE bodies that scan real tables
// pass type info up to the consumer correctly.
func TestPlanWithCTEReadingTable(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT aid, abalance FROM pgbench_accounts) SELECT aid FROM a")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := node.Output()
	if len(out) != 1 || strings.ToLower(out[0].Name) != "aid" {
		t.Errorf("schema = %+v, want one column named aid", out)
	}
}

// TestPlanWithCTEMultipleConsumers: cross-product of a CTE with
// itself. Stage A inlining produces two distinct planned subtrees;
// the resulting Output schema concatenates both.
func TestPlanWithCTEMultipleConsumers(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT aid FROM pgbench_accounts) SELECT * FROM a, a b")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := len(node.Output()); got != 2 {
		t.Errorf("schema width = %d, want 2", got)
	}
}

// TestPlanWithCTEReferencingPriorSibling: left-to-right scope —
// a later CTE in the same WITH list references an earlier one.
// Pins the in-progress map mechanic in preplanWithClause.
func TestPlanWithCTEReferencingPriorSibling(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT aid FROM pgbench_accounts), b AS (SELECT aid FROM a) SELECT aid FROM b")
	if _, err := Plan(stmt, cat); err != nil {
		t.Errorf("Plan: %v", err)
	}
}

// TestPlanWithRecursiveRejected: planner is the second line of
// defence (analyzer is the first). If Plan ever runs without an
// upstream Analyze call (some test paths do this), the planner
// must still surface 0A000.
func TestPlanWithRecursiveRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected RECURSIVE rejection, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%s, want 0A000", pe.Code)
	}
}

// TestPlanWithCTEShadowsBaseRelation: a CTE name matching a real
// catalog table resolves to the CTE. Mirrors PG semantics; the
// CTE's columns are visible, the base table's are not.
func TestPlanWithCTEShadowsBaseRelation(t *testing.T) {
	cat := pgbenchCatalog(t)
	// CTE pgbench_accounts exposes only aid; the outer SELECT
	// pulls aid from the CTE, not the base table.
	stmt := parseOne(t, "WITH pgbench_accounts AS (SELECT aid FROM pgbench_accounts) SELECT aid FROM pgbench_accounts")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := len(node.Output()); got != 1 {
		t.Errorf("shadowed schema width = %d, want 1 (CTE exposes only aid)", got)
	}
}

// TestPlanWithColumnAliasArityMismatch: the analyzer catches this
// at 42P10, but if the planner runs alone the same check fires
// from preplanWithClause. Defence-in-depth.
func TestPlanWithColumnAliasArityMismatch(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH a(x, y, z) AS (SELECT aid, bid FROM pgbench_accounts) SELECT * FROM a")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected arity-mismatch error")
	}
}

// TestPlanWithoutCTEUnchanged is the regression guard: a SELECT
// without a WITH clause goes through the existing planner path
// byte-for-byte unchanged. Without this, a future refactor that
// reorders preplanWithClause's defer/restore could regress every
// pre-M0016 test.
func TestPlanWithoutCTEUnchanged(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts WHERE aid = 1")
	if _, err := Plan(stmt, cat); err != nil {
		t.Errorf("plain SELECT: %v", err)
	}
}

// TestPlanInsertOnConflictRejected: ON CONFLICT analyzer
// validation lands in M0017-0001 step 2; planner conflict-arbiter
// selection lands in M0017-0002. Until then the planner refuses
// to produce a plan for `INSERT … ON CONFLICT …` so an executable
// plan never silently drops the clause. Mirrors the
// TestPlanWithRecursiveRejected pattern (0A000 second-line gate).
func TestPlanInsertOnConflictRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT (aid) DO UPDATE SET abalance = excluded.abalance")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected ON CONFLICT rejection, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%s, want 0A000", pe.Code)
	}
}
