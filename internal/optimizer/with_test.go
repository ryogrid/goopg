package optimizer

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

// TestPlanWithRecursiveNonUnionAccepted: PostgreSQL allows WITH RECURSIVE
// CTEs whose bodies don't actually recurse (no UNION self-reference). goopg
// now matches PG: plan them as regular non-recursive CTEs.
func TestPlanWithRecursiveNonUnionAccepted(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r")
	_, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

// TestPlanWithFewerColumnAliasesAccepted: PG (parse_cte.c analyzeCTE) allows a
// column-alias list SHORTER than the inner query's output; the trailing
// columns keep their query names. Only over-aliasing errors. Sibling of the
// analyzer's TestAnalyzeWithFewerColumnAliasesAccepted — both twins must agree.
// pg_amcheck (AC-002) emits `exclude_raw (pattern_id, rgx) AS (SELECT NULL,
// NULL, NULL WHERE false)` (two aliases over three columns) and its bootstrap
// query reaches the planner, so the analyzer fix alone is insufficient.
func TestPlanWithFewerColumnAliasesAccepted(t *testing.T) {
	cat := pgbenchCatalog(t)
	for _, sql := range []string{
		// Two aliases over three SELECT columns (the amcheck shape).
		"WITH e(pattern_id, rgx) AS (SELECT 1, 2, 3) SELECT * FROM e",
		// One alias over two columns reading a base table.
		"WITH a(x) AS (SELECT aid, bid FROM pgbench_accounts) SELECT x FROM a",
	} {
		if _, err := Plan(parseOne(t, sql), cat); err != nil {
			t.Errorf("under-aliased CTE should plan, got %v\n  sql=%s", err, sql)
		}
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

// TestPlanRecursiveCTEOrderByRejected: ORDER BY in a recursive CTE is
// not implemented in PostgreSQL/goopg — it is rejected with 0A000.
func TestPlanRecursiveCTEOrderByRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x ORDER BY 1) SELECT n FROM x LIMIT 5")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected ORDER BY rejection, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%s, want 0A000", pe.Code)
	}
	if !strings.Contains(pe.Message, "ORDER BY") {
		t.Errorf("message=%q, want ORDER BY mention", pe.Message)
	}
}

// TestPlanRecursiveCTEOffsetRejected: OFFSET in a recursive CTE body is
// rejected with 0A000 matching PostgreSQL behaviour.
func TestPlanRecursiveCTEOffsetRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x OFFSET 0) SELECT n FROM x LIMIT 5")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected OFFSET rejection, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%s, want 0A000", pe.Code)
	}
	if !strings.Contains(pe.Message, "OFFSET") {
		t.Errorf("message=%q, want OFFSET mention", pe.Message)
	}
}

// TestPlanRecursiveCTEAggregateInRecursiveMemberRejected: aggregate
// functions are not allowed in a recursive query's recursive term.
func TestPlanRecursiveCTEAggregateInRecursiveMemberRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT count(*) FROM x) SELECT n FROM x")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected aggregate rejection, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%s, want 0A000", pe.Code)
	}
	if !strings.Contains(pe.Message, "aggregate") {
		t.Errorf("message=%q, want aggregate mention", pe.Message)
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

// TestPlanInsertOnConflictExactSetMatchRejectsSubset — M0134-0034:
// a bare column-list arbiter target must EXACTLY match an index's
// column set (plancat.c:883-885 bms_equal), not merely be covered
// by it. A two-column unique index (aid, abalance) does not match
// an `ON CONFLICT (aid)` target — the prior liberal/subset matcher
// wrongly accepted this and would have updated through the wrong
// arbiter; real PG raises 42P10.
func TestPlanInsertOnConflictExactSetMatchRejectsSubset(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_aid_abalance_key"}, tbl, []string{"aid", "abalance"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
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
	if pe.Pos != 0 {
		t.Errorf("Pos=%d, want 0 (PG's ereport carries no errposition() here)", pe.Pos)
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

func TestPlanInsertOnConflictDoNothingNoTargetUsesUniqueIndex(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_aid_key"}, tbl, []string{"aid"}, true, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
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
	if ins.OnConflict.ArbiterIndex != idx {
		t.Fatalf("ArbiterIndex = %+v, want %+v", ins.OnConflict.ArbiterIndex, idx)
	}
	if len(ins.OnConflict.ArbiterColumns) != 1 || ins.OnConflict.ArbiterColumns[0] != 0 {
		t.Fatalf("ArbiterColumns = %v, want [0]", ins.OnConflict.ArbiterColumns)
	}
}

// TestResolveArbiterIndexAcceptsExclusionConstraintArbiter (M0134-0005af,
// bucket F8) exercises resolveArbiterIndex DIRECTLY (not through Plan(),
// which runs analyzer.Analyze first and would mask this site — the same
// package as planner.go lets the test call the unexported function so the
// planner's own defensive re-check — self-described as covering "paths
// that bypass" the analyzer — is verified in isolation). An exclusion
// constraint IS a legal arbiter (execIndexing.c:592-596), not just an
// idx.Unique one.
func TestResolveArbiterIndexAcceptsExclusionConstraintArbiter(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_excl"}, tbl, []string{"aid"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.IsExclusion = true
	idx.ExclusionOp = "="
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT ON CONSTRAINT pgbench_accounts_excl DO NOTHING").(*parser.InsertStmt)
	if _, _, err := resolveArbiterIndex(stmt.OnConflict.Target, tbl, nil, cat); err != nil {
		t.Fatalf("resolveArbiterIndex: %v", err)
	}
}

// TestResolveArbiterIndexRejectsDeferrableExclusionArbiter (M0134-0005af,
// bucket F8) is the direct-call twin of
// TestResolveArbiterIndexAcceptsExclusionConstraintArbiter and the
// planner-side counterpart of analyzer_test.go's
// TestAnalyzeOnConflictRejectsDeferrableExclusionArbiter: a deferrable
// (INITIALLY DEFERRED) exclusion constraint is not a legal ON CONFLICT
// arbiter (execIndexing.c:604-610, indimmediate check), and must surface
// 55000, not the plain 42P10 "is not a unique constraint".
func TestResolveArbiterIndexRejectsDeferrableExclusionArbiter(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_dexcl"}, tbl, []string{"aid"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.IsExclusion = true
	idx.ExclusionOp = "="
	idx.Deferrable = true
	idx.InitiallyDeferred = true
	stmt := parseOne(t,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) "+
			"ON CONFLICT ON CONSTRAINT pgbench_accounts_dexcl DO NOTHING").(*parser.InsertStmt)
	_, _, err = resolveArbiterIndex(stmt.OnConflict.Target, tbl, nil, cat)
	if err == nil {
		t.Fatal("expected 55000 error, got nil")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("err type=%T, want *PlanError", err)
	}
	if pe.Code != "55000" {
		t.Errorf("code=%s, want 55000", pe.Code)
	}
}

// TestResolveArbiterIndexRejectsMerelyDeferrableArbiter (M0134-0161) pins the
// narrower half of the indimmediate rule that goopg previously got wrong: a
// constraint declared just `DEFERRABLE` — i.e. INITIALLY IMMEDIATE, so
// InitiallyDeferred is false — is STILL non-immediate and STILL rejected as an
// ON CONFLICT arbiter. index_create derives indimmediate from the DEFERRABLE
// flag alone (postgres/src/backend/catalog/index.c:1049, 2080-2082), so keying
// the check on InitiallyDeferred silently accepted these. Oracle-verified on
// PG 18.3: `INSERT … ON CONFLICT ON CONSTRAINT u_defer DO NOTHING` against a
// `UNIQUE (b) DEFERRABLE` constraint raises 55000.
func TestResolveArbiterIndexRejectsMerelyDeferrableArbiter(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_dkey"}, tbl, []string{"aid"}, true, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	idx.Deferrable = true
	idx.InitiallyDeferred = false // DEFERRABLE INITIALLY IMMEDIATE
	if idx.IsImmediate() {
		t.Fatal("IsImmediate() = true for a DEFERRABLE index, want false")
	}
	// Both arbiter-resolution branches must reject it: the explicit
	// ON CONSTRAINT form and the inferred-by-column form. PG rejects both
	// (execIndexing.c:604-610 runs after infer_arbiter_indexes has already
	// selected the index — plancat.c:817).
	for _, sql := range []string{
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) " +
			"ON CONFLICT ON CONSTRAINT pgbench_accounts_dkey DO NOTHING",
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) " +
			"ON CONFLICT (aid) DO NOTHING",
	} {
		stmt := parseOne(t, sql).(*parser.InsertStmt)
		_, _, err := resolveArbiterIndex(stmt.OnConflict.Target, tbl, nil, cat)
		if err == nil {
			t.Fatalf("%s: expected 55000 error, got nil", sql)
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Fatalf("%s: err type=%T, want *PlanError", sql, err)
		}
		if pe.Code != "55000" {
			t.Errorf("%s: code=%s (%s), want 55000", sql, pe.Code, pe.Message)
		}
	}
}
