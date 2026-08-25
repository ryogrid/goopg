package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// Performance baselines for the parser-rewrite project
// (docs/design/not_ralph/04-testing-and-gates.md §3): recorded at P0 by
//
//	go test ./internal/sqlparser/ -bench . -benchmem -run '^$'
//
// Every wave flip re-runs this suite and compares against the numbers in
// docs/design/not_ralph/PERF-BASELINE.md; >2x regression on any input class
// stops the flip for investigation.
//
// Input classes mirror the gate matrix:
//   - select-heavy: a representative TPC-H-shaped query
//   - ddl-heavy:    a CREATE TABLE with several constraints
//   - expr-heavy:   a deeply nested arithmetic/boolean expression

const benchSelect = `SELECT l.l_orderkey, sum(l.extendedprice * (1 - l.discount)) AS revenue
FROM lineitem l INNER JOIN orders o ON l.l_orderkey = o.o_orderkey
WHERE o.o_orderdate >= '1994-01-01' AND l.discount BETWEEN 0.05 AND 0.07
GROUP BY l.l_orderkey HAVING sum(l.extendedprice) > 1000
ORDER BY revenue DESC LIMIT 10`

const benchDDL = `CREATE TABLE bench_ddl_t (
	id bigint NOT NULL PRIMARY KEY,
	name varchar(64) DEFAULT 'unknown',
	score numeric(10,2) CHECK (score >= 0),
	parent_id bigint REFERENCES bench_ddl_t (id) ON DELETE CASCADE,
	UNIQUE (name, parent_id)
)`

const benchExpr = "SELECT ((1 + 2) * (3 - 4) / 5 % 6 = 7 AND 8 <> 9 OR 10 <= 11) IS TRUE, CASE WHEN a > b THEN c WHEN d < e THEN f ELSE g END FROM t WHERE x IN (1, 2, 3) AND y NOT LIKE '%z%'"

func benchTokens(b *testing.B, sql string) []parser.Token {
	b.Helper()
	toks, err := parser.Lex(sql)
	if err != nil {
		b.Fatal(err)
	}
	return toks
}

func BenchmarkSkeletonParseOneSelectHeavy(b *testing.B) {
	toks := benchTokens(b, benchSelect)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseOne(toks, 0); err == nil {
			b.Fatal("expected skeleton syntax error")
		}
	}
}

func BenchmarkSkeletonParseOneDDLHeavy(b *testing.B) {
	toks := benchTokens(b, benchDDL)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseOne(toks, 0); err == nil {
			b.Fatal("expected skeleton syntax error")
		}
	}
}

func BenchmarkSkeletonParseOneExprHeavy(b *testing.B) {
	toks := benchTokens(b, benchExpr)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseOne(toks, 0); err == nil {
			b.Fatal("expected skeleton syntax error")
		}
	}
}

// Legacy-side comparison points (the flip gates compare NEW parser totals,
// including its own lexing, against these):
// go test ./internal/parser/ -bench 'BenchmarkParse' -benchmem -run '^$'
// — none existed at P0 time; added here so the baseline file can cite real
// numbers instead of guesses.
func BenchmarkLegacyParseSelectHeavy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parser.Parse(benchSelect); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLegacyParseDDLHeavy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parser.Parse(benchDDL); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLegacyParseExprHeavy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parser.Parse(benchExpr); err != nil {
			b.Fatal(err)
		}
	}
}
