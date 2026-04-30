package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
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

// TestPlanWithRecursiveRejected: non-UNION-ALL recursive CTEs are
// rejected by the planner. The analyzer passes WITH RECURSIVE
// through, but the planner requires UNION ALL.
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

// TestPlanInsertOnConflictNoMatchingArbiter — without a unique
// index covering the conflict-target columns, the planner can't
// pick an arbiter and surfaces upstream's 42P10 ("no unique or
// exclusion constraint matching the ON CONFLICT specification")
// diagnostic. Pgbench's default schema has no index on aid, so
// this stands as the no-match guard.
func TestPlanInsertOnConflictNoMatchingArbiter(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT (aid) DO UPDATE SET abalance = excluded.abalance")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected arbiter-resolution error, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "42P10" {
		t.Errorf("code=%s, want 42P10", pe.Code)
	}
}

// TestPlanInsertOnConflictWithUniqueIndex — once a unique index
// covers the conflict-target column set, the planner produces a
// fully-resolved Insert with OnConflict populated: arbiter index
// reference, ordinals matching the index columns, and the SET
// expressions resolved against the target+excluded scope.
func TestPlanInsertOnConflictWithUniqueIndex(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_pkey"}, tbl, []string{"aid"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT (aid) DO UPDATE SET abalance = excluded.abalance")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins, ok := node.(*Insert)
	if !ok {
		t.Fatalf("node = %T, want *Insert", node)
	}
	if ins.OnConflict == nil {
		t.Fatal("OnConflict nil")
	}
	if ins.OnConflict.Action != OnConflictActionUpdate {
		t.Errorf("Action = %v", ins.OnConflict.Action)
	}
	if ins.OnConflict.ArbiterIndex != idx {
		t.Errorf("ArbiterIndex = %v, want %v", ins.OnConflict.ArbiterIndex, idx)
	}
	if len(ins.OnConflict.ArbiterColumns) != 1 || ins.OnConflict.ArbiterColumns[0] != 0 {
		t.Errorf("ArbiterColumns = %v, want [0]", ins.OnConflict.ArbiterColumns)
	}
	// abalance is column ordinal 2 on pgbench_accounts.
	if ins.OnConflict.UpdateSet[2] == nil {
		t.Errorf("UpdateSet[2] = nil, want non-nil")
	}
	if ins.OnConflict.UpdateWhere != nil {
		t.Errorf("UpdateWhere = %+v, want nil", ins.OnConflict.UpdateWhere)
	}
}

// TestPlanInsertOnConflictByConstraintName — Stage B path: the
// arbiter is resolved by a named constraint instead of column
// inference. Pins (1) the planner accepts the constraint-name
// shape and (2) ArbiterIndex points at the named index.
func TestPlanInsertOnConflictByConstraintName(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_pkey"}, tbl, []string{"aid"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT ON CONSTRAINT pgbench_accounts_pkey DO UPDATE SET abalance = excluded.abalance")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins := node.(*Insert)
	if ins.OnConflict == nil || ins.OnConflict.ArbiterIndex != idx {
		t.Fatalf("ArbiterIndex = %+v, want %+v", ins.OnConflict.ArbiterIndex, idx)
	}
	if len(ins.OnConflict.ArbiterColumns) != 1 || ins.OnConflict.ArbiterColumns[0] != 0 {
		t.Errorf("ArbiterColumns = %v, want [0]", ins.OnConflict.ArbiterColumns)
	}
}

// TestPlanInsertOnConflictDoNothingNoTarget — the bare DO NOTHING
// shape (no conflict target) plans without any unique-index
// match: ArbiterIndex stays nil and the executor decides which
// indexes to consult at runtime.
func TestPlanInsertOnConflictDoNothingNoTarget(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT DO NOTHING")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins := node.(*Insert)
	if ins.OnConflict == nil {
		t.Fatal("OnConflict nil")
	}
	if ins.OnConflict.Action != OnConflictActionNothing {
		t.Errorf("Action = %v, want OnConflictActionNothing", ins.OnConflict.Action)
	}
	if ins.OnConflict.ArbiterIndex != nil {
		t.Errorf("ArbiterIndex = %+v, want nil for no-target form", ins.OnConflict.ArbiterIndex)
	}
}
