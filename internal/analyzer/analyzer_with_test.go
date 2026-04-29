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

// TestAnalyzeWithRecursiveRejected pins the Stage A
// limitation: WITH RECURSIVE is parsed but rejected at the
// analyzer with SQLSTATE 0A000. Stage B (M0016-0003) flips
// this to actual recursive analysis.
func TestAnalyzeWithRecursiveRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"WITH RECURSIVE r AS (SELECT aid FROM pgbench_accounts) SELECT aid FROM r",
		"0A000")
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
