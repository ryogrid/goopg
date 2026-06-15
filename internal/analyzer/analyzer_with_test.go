package analyzer

import (
	"strings"
	"testing"
)

// TestAnalyzeWithCTEResolvesInFROM: a CTE name declared in WITH
// resolves as a FROM-clause range-var. Without the
// scope.ctes / resolveTable plumbing this would error with
// 42P01 (relation does not exist).
func TestAnalyzeWithCTEResolvesInFROM(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT aid, abalance FROM pgbench_accounts) SELECT aid FROM a")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze: %v", err)
	}
}

// TestAnalyzeWithCTELeftToRightVisibility: a later CTE in the
// same WITH list can reference an earlier one. Pins the
// left-to-right scope rule from the milestone DoD.
func TestAnalyzeWithCTELeftToRightVisibility(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t, "WITH a AS (SELECT aid FROM pgbench_accounts), b AS (SELECT aid FROM a) SELECT aid FROM b")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze: %v", err)
	}
}

// TestAnalyzeWithCTERejectsForwardReference: an earlier CTE
// can NOT reference a later sibling. PostgreSQL enforces this
// for non-recursive WITH; goopg matches.
func TestAnalyzeWithCTERejectsForwardReference(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH a AS (SELECT aid FROM b), b AS (SELECT aid FROM pgbench_accounts) SELECT aid FROM a",
		"42P01")
}

// TestAnalyzeWithRecursivePasses verifies that WITH RECURSIVE
// now passes through the analyzer (the planner enforces the
// UNION ALL requirement).
func TestAnalyzeWithRecursivePasses(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t,
		"WITH RECURSIVE r AS (SELECT aid FROM pgbench_accounts) SELECT aid FROM r")
	err := Analyze(stmt, cat)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// TestAnalyzeWithDuplicateCTEName: two CTEs with the same
// name in one WITH list error with 42710 (duplicate_alias).
func TestAnalyzeWithDuplicateCTEName(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH a AS (SELECT aid FROM pgbench_accounts), a AS (SELECT bid FROM pgbench_accounts) SELECT aid FROM a",
		"42710")
}

// TestAnalyzeWithColumnAliasArityMismatch: declaring N column
// aliases for a CTE whose inner SELECT produces M != N columns
// errors with 42P10 (invalid_column_reference).
func TestAnalyzeWithColumnAliasArityMismatch(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH a(x, y, z) AS (SELECT aid, bid FROM pgbench_accounts) SELECT * FROM a",
		"42P10")
}

// TestAnalyzeWithColumnAliasesRenameColumns: when arity
// matches, the CTE's exposed columns use the alias names.
// A reference to the original column name fails.
func TestAnalyzeWithColumnAliasesRenameColumns(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t, "WITH a(x, y) AS (SELECT aid, bid FROM pgbench_accounts) SELECT x FROM a")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze with renamed columns: %v", err)
	}
	// Original name shouldn't resolve any more.
	expectAnalyzeCode(t, cat,
		"WITH a(x, y) AS (SELECT aid, bid FROM pgbench_accounts) SELECT aid FROM a",
		"42703")
}

// TestAnalyzeWithCTEShadowsBaseRelation: a CTE named after an
// existing table shadows the table inside the SELECT. Mirrors
// PostgreSQL — operationally useful for "factor out a tweaked
// view of an existing table".
func TestAnalyzeWithCTEShadowsBaseRelation(t *testing.T) {
	cat := analyzerCatalog(t)
	// CTE named pgbench_accounts shadows the base table; the
	// SELECT * FROM pgbench_accounts in the outer query sees
	// only the CTE's two columns (aid, abalance).
	stmt := parseOne(t, "WITH pgbench_accounts AS (SELECT aid, abalance FROM pgbench_accounts) SELECT aid FROM pgbench_accounts")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze shadowing: %v", err)
	}
	// And `bid` (which the base table has but the CTE doesn't)
	// should NOT resolve — confirms shadowing is real, not
	// fall-through.
	expectAnalyzeCode(t, cat,
		"WITH pgbench_accounts AS (SELECT aid, abalance FROM pgbench_accounts) SELECT bid FROM pgbench_accounts",
		"42703")
}

// TestAnalyzeWithoutCTEUnchanged is the regression guard: a
// SELECT without a WITH clause goes through exactly the same
// pre-M0016 path. Without this guard a future refactor that
// accidentally requires a non-nil With could break every
// pre-existing test.
func TestAnalyzeWithoutCTEUnchanged(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("plain SELECT: %v", err)
	}
}

// TestAnalyzeWithCTEStarBodyExpands: a CTE whose body is
// `SELECT *` must expand the star into the inner relation's
// columns so the outer query can reference them by name.
// Before the registerAnalyzedCTE star-handling fix this errored
// with 42601 ("'*' is not allowed here") because the CTE body's
// target list was fed straight into analyzeExpr, which rejects a
// bare StarExpr. M0097-0003 regression guard.
func TestAnalyzeWithCTEStarBodyExpands(t *testing.T) {
	cat := analyzerCatalog(t)
	for _, sql := range []string{
		"WITH a AS (SELECT * FROM pgbench_accounts) SELECT aid FROM a",
		"WITH a AS (SELECT * FROM pgbench_accounts) SELECT abalance FROM a",
		"WITH a AS (SELECT pgbench_accounts.* FROM pgbench_accounts) SELECT aid FROM a",
		"WITH a AS (SELECT * FROM pgbench_accounts) SELECT * FROM a",
	} {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Errorf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWithCTEStarBodyKnowsColumnSet confirms the star
// expanded to a concrete column set rather than a permissive
// wildcard: a reference to a column the inner relation does not
// have still fails with 42703. Pins the expansion's correctness.
func TestAnalyzeWithCTEStarBodyKnowsColumnSet(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH a AS (SELECT * FROM pgbench_accounts) SELECT no_such_col FROM a",
		"42703")
}

// TestAnalyzeWithCTEErrorsBubbleUp: a type/column error inside
// a CTE body must surface from Analyze (not silently be
// ignored). Pins the recursion through analyzeSelectWithParent.
func TestAnalyzeWithCTEErrorsBubbleUp(t *testing.T) {
	cat := analyzerCatalog(t)
	err := Analyze(parseOne(t,
		"WITH a AS (SELECT no_such_col FROM pgbench_accounts) SELECT * FROM a"), cat)
	if err == nil {
		t.Fatal("expected error from bad column ref inside CTE")
	}
	if !strings.Contains(err.Error(), "no_such_col") {
		t.Errorf("err = %v; want it to mention no_such_col", err)
	}
}

// TestAnalyzeWithValuesCTEColumnAliases: a CTE whose body is a VALUES list
// derives its column count from the first row, so explicit column aliases
// matching that arity are accepted (not rejected with 42P10 "inner query
// produces 0 columns"). pg_amcheck's database-resolution query uses this
// shape (`include_raw (pattern_id, rgx) AS (VALUES (0,'^(x)$'), …)`).
// Mirrors the VALUES anchor handling in analyzeRecursiveCTE — these two
// paths must stay in sync. Regression for M0110-0003 / AC-002.
func TestAnalyzeWithValuesCTEColumnAliases(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t,
		"WITH v(pattern_id, rgx) AS (VALUES (0, '^(x)$'), (1, '^(y)$')) SELECT pattern_id, rgx FROM v")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze VALUES-CTE with column aliases: %v", err)
	}
}

// TestAnalyzeWithValuesCTEArityMismatch: with the VALUES column count now
// derived, an alias-list whose arity differs from the VALUES row width is
// still correctly rejected with 42P10 (i.e. the derivation feeds the same
// arity check, it doesn't bypass it).
func TestAnalyzeWithValuesCTEArityMismatch(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH v(a, b, c) AS (VALUES (0, '^(x)$')) SELECT * FROM v",
		"42P10")
}

// TestAnalyzeWithValuesCTEDefaultColumnNames: without an explicit alias
// list, a VALUES-CTE exposes default "columnN" names, resolvable in the
// outer query.
func TestAnalyzeWithValuesCTEDefaultColumnNames(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t,
		"WITH v AS (VALUES (1, 2), (3, 4)) SELECT column1, column2 FROM v")
	if err := Analyze(stmt, cat); err != nil {
		t.Errorf("Analyze VALUES-CTE default column names: %v", err)
	}
}
