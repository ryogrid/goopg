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
	cases := []string{
		"SELECT DISTINCT aid FROM pgbench_accounts",
		"SELECT 1 UNION SELECT 2",
	}
	for _, sql := range cases {
		expectAnalyzeCode(t, cat, sql, "0A000")
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
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "INSERT INTO pgbench_accounts (aid) VALUES ('x')", "42804")
	expectAnalyzeCode(t, cat, "UPDATE pgbench_accounts SET aid = 'x'", "42804")
	expectAnalyzeCode(t, cat, "DELETE FROM pgbench_accounts RETURNING aid", "0A000")
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
