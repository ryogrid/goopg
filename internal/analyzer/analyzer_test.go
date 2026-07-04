package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func analyzerCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}},
		{Name: "filler", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_history"}, []catalog.Column{
		{Name: "tid", Type: catalog.Type{Name: "int4"}},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "aid", Type: catalog.Type{Name: "int4"}},
		{Name: "delta", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func parseOne(t *testing.T, sql string) parser.Stmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d statements", sql, len(stmts))
	}
	return stmts[0]
}

func expectAnalyzeCode(t *testing.T, cat catalog.Catalog, sql, code string) {
	t.Helper()
	err := Analyze(parseOne(t, sql), cat)
	if err == nil {
		t.Fatalf("Analyze(%q) expected error", sql)
	}
	ae, ok := err.(*AnalyzeError)
	if !ok {
		t.Fatalf("Analyze(%q) err type=%T", sql, err)
	}
	if ae.Code != code {
		t.Fatalf("Analyze(%q) code=%s want %s", sql, ae.Code, code)
	}
}

func TestAnalyzeSelectAliasAndQualifiedStar(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT a.aid, a.* FROM pgbench_accounts a WHERE a.aid = 1 ORDER BY a.aid LIMIT 10 OFFSET 2"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeRejectUnsupportedSelectFeatures(t *testing.T) {
	cat := analyzerCatalog(t)
	// DISTINCT (M0097-0005) and the set operations UNION/INTERSECT/EXCEPT
	// with optional ALL (M0097-0024) are all accepted by the analyzer now.
	accepted := []string{
		"SELECT DISTINCT aid FROM pgbench_accounts",
		"SELECT 1 UNION SELECT 2",
		"SELECT 1 UNION ALL SELECT 2",
		"SELECT 1 INTERSECT SELECT 2",
		"SELECT 1 EXCEPT ALL SELECT 2",
	}
	for _, sql := range accepted {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Errorf("Analyze(%q) should be supported now, got: %v", sql, err)
		}
	}
}

func TestAnalyzeSelectJoinAndGroupBy(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT a.aid, sum(h.delta) FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid GROUP BY a.aid HAVING sum(h.delta) > 0 ORDER BY a.aid"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeAmbiguousColumnError(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid", "42702")
}

func TestAnalyzeTypeErrors(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts WHERE aid", "42804")
	expectAnalyzeCode(t, cat, "SELECT aid || abalance FROM pgbench_accounts", "42883")
	// A bare string literal is now typed `unknown` (like PG's UNKNOWNOID), so
	// `LIMIT 'x'` no longer fails at analysis — it defers to runtime, which
	// rejects a non-integer LIMIT with 42804 (operators.go limitOp.Open),
	// consistent with how string-literal-into-int is deferred (M0097-0003).
	// A concrete non-integer type (bool) is still caught at analysis:
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts LIMIT true", "42804")
}

func TestAnalyzeDMLTypeAndReturningErrors(t *testing.T) {
	// String literals ('x') are now allowed for integer columns at analysis time
	// (for PostgreSQL compatibility: untyped literals can be assigned to any type,
	// with validation deferred to runtime). See M0097-0003.
	// INSERT/UPDATE of 'x' into int4 columns now fails at runtime (22P02).
	// DELETE/UPDATE RETURNING is now supported (M0100-0005); no error expected.
}

func TestAnalyzeWindowFunctionAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT row_number() OVER () FROM pgbench_accounts",
		"SELECT rank() OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
		"SELECT aid FROM pgbench_accounts ORDER BY row_number() OVER (ORDER BY aid)",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

func TestAnalyzeWindowFunctionUnsupportedRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT count(*) OVER () FROM pgbench_accounts",
		"0A000")
}

// TestAnalyzeCreateFunctionPassesThrough pins M0015 Stage A step 3:
// the analyzer drops the step-1 SQLSTATE 0A000 rejection now that
// the executor's CREATE FUNCTION operator lands the catalog row.
// DDL flows straight through Plan to the executor; the analyzer
// no longer needs to gatekeep these statements.
func TestAnalyzeCreateFunctionPassesThrough(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t,
		"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$"), cat); err != nil {
		t.Errorf("Analyze CREATE FUNCTION = %v, want nil", err)
	}
	if err := Analyze(parseOne(t, "DROP FUNCTION IF EXISTS f(int)"), cat); err != nil {
		t.Errorf("Analyze DROP FUNCTION = %v, want nil", err)
	}
}

func TestAnalyzeWindowFunctionArgShapeRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT row_number(1) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT rank(DISTINCT aid) OVER () FROM pgbench_accounts",
		"42601")
}

func TestAnalyzeWindowFunctionPlacementRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts WHERE row_number() OVER () > 0",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts GROUP BY row_number() OVER ()",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts HAVING row_number() OVER () > 0",
		"0A000")
}

// TestAnalyzeNamedWindowClauseAccepted pins the M0020 named-window
// slice: a bare `OVER w` reference resolves against a trailing
// `WINDOW w AS (...)` clause, including two functions sharing one
// named window and two functions each with their own.
func TestAnalyzeNamedWindowClauseAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid ORDER BY aid)",
		"SELECT row_number() OVER w, rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid ORDER BY aid)",
		"SELECT row_number() OVER w1, rank() OVER w2 FROM pgbench_accounts WINDOW w1 AS (PARTITION BY bid), w2 AS (ORDER BY aid)",
		"SELECT aid FROM pgbench_accounts WINDOW w AS (ORDER BY aid) ORDER BY row_number() OVER w",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeNamedWindowUndefinedRejected pins the 42P20 diagnostic
// for `OVER name` with no matching WINDOW clause item — upstream's
// "window %q does not exist".
func TestAnalyzeNamedWindowUndefinedRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER missing FROM pgbench_accounts",
		"42P20")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER w FROM pgbench_accounts WINDOW w2 AS (PARTITION BY bid)",
		"42P20")
}

// TestAnalyzeWithOrdinalityNamedColumn is the regression pin for the
// WITH-ORDINALITY 42703 bug: tableFuncColumns never threaded
// rv.TableFunc.WithOrdinality through, so the analyzer's synthetic FROM-item
// table never had an ordinality column, and unnest/regexp_matches fell to
// the generic single-int8-column default — naming either the ordinality
// column or the SRF's own element column explicitly in the outer SELECT hit
// "column does not exist" even though the planner produced the row
// correctly. Root cause: internal/analyzer/analyzer.go's tableFuncColumns
// (not the planner's wrapOrdinality/planFromUnnest, which were always
// correct). See .ralph/working_set.md history for the diagnosis loop.
func TestAnalyzeWithOrdinalityNamedColumn(t *testing.T) {
	cat := analyzerCatalog(t)
	ok := []string{
		"SELECT n, m FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(m, n)",
		"SELECT ord FROM generate_series(1, 3) WITH ORDINALITY AS t(val, ord)",
		"SELECT ord FROM regexp_matches('a1b2', '[0-9]', 'g') WITH ORDINALITY AS t(m, ord)",
		"SELECT a, b, n FROM unnest(ARRAY[1,2], ARRAY[3,4]) WITH ORDINALITY AS t(a, b, n)",
		"SELECT val FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(val, n)",
	}
	for _, sql := range ok {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Errorf("Analyze(%q) expected success, got: %v", sql, err)
		}
	}
	expectAnalyzeCode(t, cat,
		"SELECT bogus FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(m, n)",
		"42703")
}
