package analyzer

import "testing"

// TestAnalyzeForUpdateBasic — the headline accepted shape: a
// SELECT with a FROM clause + bare FOR UPDATE. Pins that no false
// positives surface from the row-locking validator.
func TestAnalyzeForUpdateBasic(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t, "SELECT aid FROM pgbench_accounts WHERE aid = 1 FOR UPDATE"), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}

// TestAnalyzeForUpdateOfAlias — `OF alias` resolves against the
// FROM-clause range variable's alias. Mirrors upstream's
// alias-shadows-table rule: when the FROM has an alias, the
// table name is hidden.
func TestAnalyzeForUpdateOfAlias(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t, "SELECT a.aid FROM pgbench_accounts a FOR UPDATE OF a"), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}

// TestAnalyzeForUpdateOfTableNameWithoutAlias — when no alias is
// supplied the bare table name is the locking-clause target.
func TestAnalyzeForUpdateOfTableNameWithoutAlias(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t, "SELECT aid FROM pgbench_accounts FOR UPDATE OF pgbench_accounts"), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}

// TestAnalyzeForUpdateRequiresFromClause — locking is meaningless
// without a FROM, surfaces 0A000.
func TestAnalyzeForUpdateRequiresFromClause(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "SELECT 1 FOR UPDATE", "0A000")
}

// TestAnalyzeForUpdateRejectsGroupBy — aggregation produces
// grouped rows that don't map back to individual storage tuples.
func TestAnalyzeForUpdateRejectsGroupBy(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT bid, count(*) FROM pgbench_accounts GROUP BY bid FOR UPDATE",
		"0A000")
}

// TestAnalyzeForUpdateRejectsHaving — same aggregation guard for
// the HAVING side.
func TestAnalyzeForUpdateRejectsHaving(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT bid FROM pgbench_accounts GROUP BY bid HAVING count(*) > 1 FOR UPDATE",
		"0A000")
}

// TestAnalyzeForUpdateUnknownTarget — `OF not_in_from` errors with
// 42P01 (the upstream "relation not in FROM" diagnostic).
func TestAnalyzeForUpdateUnknownTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts FOR UPDATE OF not_in_from",
		"42P01")
}

// TestAnalyzeForUpdateRejectsTableNameWhenAliased — when the FROM
// supplies an alias, the underlying table name is hidden (matches
// upstream's column-reference rule). FOR UPDATE OF <table_name>
// errors when an alias is in scope.
func TestAnalyzeForUpdateRejectsTableNameWhenAliased(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT a.aid FROM pgbench_accounts a FOR UPDATE OF pgbench_accounts",
		"42P01")
}

// TestAnalyzeForShareAcceptedSimilarly — read-intent variant
// passes the same validators as FOR UPDATE.
func TestAnalyzeForShareAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t, "SELECT aid FROM pgbench_accounts FOR SHARE"), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}

// TestAnalyzeForUpdateMultiClauseAccepted — multi-clause shape
// (`FOR UPDATE OF a NOWAIT FOR SHARE OF h`) parses each clause's
// targets independently against the same FROM list.
func TestAnalyzeForUpdateMultiClauseAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT * FROM pgbench_accounts a, pgbench_history h FOR UPDATE OF a NOWAIT FOR SHARE OF h"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}
