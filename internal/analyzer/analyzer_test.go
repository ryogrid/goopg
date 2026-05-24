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
	expectAnalyzeCode(t, cat, "SELECT aid || abalance FROM pgbench_accounts", "42804")
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts LIMIT 'x'", "42804")
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
