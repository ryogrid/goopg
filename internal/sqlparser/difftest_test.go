package sqlparser

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// Differential AST harness (docs/design/not_ralph/04-testing-and-gates.md
// §2): run the SAME input through the legacy recursive-descent parser and
// the goyacc parser, render both trees canonically (positions stripped), and
// compare. A mismatch means either a porting bug or a documented behavior
// delta — never silence.

// canonDump renders any AST value deterministically. Rules:
//   - field named pos/Pos is skipped (byte positions legitimately differ in
//     fidelity details; content equality is the gate),
//   - pointers dereference, nil prints as "∅",
//   - interface values unwrap,
//   - structs print as Type{field=value,...} so node identity is explicit,
//   - slices print as [a,b,c], maps sorted (unused today).
func canonDump(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return "∅"
		}
		return canonDump(v.Elem())
	case reflect.Struct:
		t := v.Type()
		var b strings.Builder
		fmt.Fprintf(&b, "%s{", t.Name())
		first := true
		for i := 0; i < t.NumField(); i++ {
			name := t.Field(i).Name
			if name == "pos" || name == "Pos" {
				continue // position-stripped canonical form
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			fmt.Fprintf(&b, "%s=%s", name, canonDump(v.Field(i)))
		}
		b.WriteByte('}')
		return b.String()
	case reflect.Slice:
		if v.IsNil() {
			return "∅"
		}
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = canonDump(v.Index(i))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case reflect.String:
		return fmt.Sprintf("%q", v.String())
	case reflect.Bool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%d", v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return fmt.Sprintf("%d", v.Uint())
	case reflect.Float32, reflect.Float64:
		return fmt.Sprintf("%v", v.Float())
	default:
		return fmt.Sprintf("<%s>", v.Kind())
	}
}

func dumpStmts(stmts []parser.Stmt) string {
	val := reflect.ValueOf(stmts)
	if !val.IsValid() || val.IsNil() {
		return "(nil)"
	}
	parts := make([]string, len(stmts))
	for i, s := range stmts {
		parts[i] = canonDump(reflect.ValueOf(s))
	}
	return strings.Join(parts, "; ")
}

// diffParse runs both parsers over sql and returns their canonical dumps.
// Legacy errors are expected for inputs outside its surface; new-parser
// errors likewise — the corpus only feeds inputs both must accept.
func diffParse(sql string) (legacy, yacc string, err error) {
	lstmts, err := parser.Parse(sql)
	if err != nil {
		return "", "", fmt.Errorf("legacy: %w", err)
	}
	toks, err := parser.Lex(sql)
	if err != nil {
		return "", "", fmt.Errorf("lex: %w", err)
	}
	nstmts, err := ParseOne(toks, 0)
	if err != nil {
		return "", "", fmt.Errorf("yacc: %w", err)
	}
	return dumpStmts(lstmts), dumpStmts(nstmts), nil
}

// TestDifferentialSelectCore is the P1.1 gate corpus (04-testing §3):
// SELECT core — targets incl. arithmetic/precedence, column refs
// (qualified), FROM with aliases (incl. bare alias), WHERE, DISTINCT.
func TestDifferentialSelectCore(t *testing.T) {
	cases := []string{
		"SELECT 1",
		"SELECT 1+2*3-4/5",
		"SELECT 'hello'",
		"SELECT true, false, null",
		"SELECT $1",
		"SELECT a FROM t",
		"SELECT a, b.c FROM t",
		"SELECT * FROM s.t AS alias",
		"SELECT t.* FROM public.t t",
		"SELECT x > 1 AND y = 2 OR NOT z FROM t",
		"SELECT a FROM t WHERE x > 1",
		"SELECT DISTINCT a, b FROM t WHERE c <> 3",
		"SELECT (1+2)*3 AS nine FROM t",
		"SELECT a FROM t ORDER BY b",
		"SELECT a FROM t ORDER BY b DESC",
		"SELECT a FROM t ORDER BY b ASC NULLS FIRST",
		"SELECT a FROM t ORDER BY b DESC NULLS LAST",
		"SELECT a FROM t ORDER BY b, c DESC, 1",
		"SELECT a FROM t LIMIT 10",
		"SELECT a FROM t LIMIT 10 OFFSET 5",
		"SELECT a FROM t OFFSET 5 ROWS",
		"SELECT a FROM t OFFSET 5 LIMIT 2",
		"SELECT a FROM t FETCH FIRST 10 ROWS ONLY",
		"SELECT a FROM t FETCH NEXT ROWS ONLY",
		"SELECT a FROM t FETCH FIRST 10 ROWS WITH TIES",
		"SELECT a FROM t WHERE x > 0 ORDER BY b LIMIT 3 OFFSET 1",
		"SELECT * FROM t1 JOIN t2 ON t1.id = t2.id",
		"SELECT * FROM t1 INNER JOIN t2 ON t1.id = t2.id",
		"SELECT * FROM t1 LEFT OUTER JOIN t2 ON t1.id = t2.id",
		"SELECT * FROM t1 RIGHT JOIN t2 USING (id)",
		"SELECT * FROM t1 FULL JOIN t2 ON t1.a = t2.b AND t1.c > 5",
		"SELECT * FROM t1 CROSS JOIN t2",
		"SELECT * FROM t1 NATURAL JOIN t2",
		"SELECT * FROM a NATURAL LEFT JOIN b JOIN c ON a.x = c.x",
		"SELECT * FROM t1 JOIN t2 ON t1.a = t2.a JOIN t3 ON t2.b = t3.b",
		"SELECT * FROM t1, t2, t3 WHERE t1.a = t2.a",
		"SELECT * FROM ONLY parent_tab",
		"SELECT * FROM (SELECT id, name FROM users) AS u",
		"SELECT * FROM (SELECT id FROM users) u (user_id)",
		"SELECT * FROM LATERAL (SELECT x FROM gen_tab) AS g",
		"SELECT o.orderkey FROM orders o JOIN lineitem l ON o.orderkey = l.l_orderkey WHERE o.totalprice > 1000 ORDER BY o.orderdate DESC LIMIT 5",
		"SELECT * FROM (t1 JOIN t2 ON t1.a = t2.a) AS x",
		"SELECT * FROM (t1 CROSS JOIN t2) AS xj (c1, c2)",
		"SELECT * FROM generate_series(1, 3)",
		"SELECT * FROM generate_series(1,3) WITH ORDINALITY AS g(n)",
		"SELECT * FROM pg_catalog.pg_get_publication_tables(NULL) pub",
		"SELECT * FROM ROWS FROM(generate_series(1, 2)) WITH ORDINALITY",
		"SELECT * FROM LATERAL generate_series(1, 2) g",
		"SELECT * FROM LATERAL (SELECT x FROM gen_tab) AS g",
		"SELECT a, b FROM t GROUP BY a, b",
		"SELECT a FROM t GROUP BY a HAVING a > 5",
		"SELECT dept, sal FROM emp GROUP BY dept HAVING sal > 100 AND dept <> 0 ORDER BY dept LIMIT 4",
		"SELECT DISTINCT ON (a, b) c FROM t ORDER BY a, b",
		"SELECT a FROM t1 UNION SELECT b FROM t2",
		"SELECT a FROM t1 UNION ALL SELECT b FROM t2",
		"SELECT a FROM t1 INTERSECT SELECT b FROM t2",
		"SELECT a FROM t1 EXCEPT ALL SELECT b FROM t2",
		"SELECT 1 UNION SELECT 2 UNION SELECT 3",
		"SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 ORDER BY 1 LIMIT 5",
		"SELECT a FROM t WHERE x > 0 GROUP BY a HAVING a > 2 ORDER BY a LIMIT 3",
		"WITH cte AS (SELECT a FROM t) SELECT * FROM cte",
		"WITH cte AS (SELECT a FROM t), cte2 AS (SELECT b FROM u) SELECT * FROM cte, cte2",
		"WITH cte (x, y) AS (SELECT a, b FROM t) SELECT x FROM cte",
		"WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n FROM t WHERE n < 5) SELECT n FROM t",
		"WITH cte AS MATERIALIZED (SELECT a FROM t) SELECT * FROM cte",
		"WITH cte AS NOT MATERIALIZED (SELECT a FROM t) SELECT * FROM cte",
		"VALUES (1, 2)",
		"VALUES (1, 'a'), (2, 'b')",
		"VALUES (1) ORDER BY column1 DESC LIMIT 2",
		"VALUES (1) UNION SELECT 2 ORDER BY 1 LIMIT 1",
		"TABLE t",
		"TABLE s.t",
	}
	for _, sql := range cases {
		legacy, yacc, err := diffParse(sql)
		if err != nil {
			t.Errorf("%q: %v", sql, err)
			continue
		}
		if legacy != yacc {
			t.Errorf("%q AST mismatch:\n  legacy: %s\n    yacc: %s", sql, legacy, yacc)
		}
	}
}

// Known-difference pins — every row of difftest_known_diffs.md needs a test
// asserting BOTH sides so the table can never silently rot.

// TestKnownDiffUnaryMinusFold pins the unary-minus row: the yacc parser
// folds `-literal` into a negative constant (upstream doNegate semantics,
// gram.y :10874) while the legacy parser builds UnaryOp{OpUnaryNeg}.
func TestKnownDiffUnaryMinusFold(t *testing.T) {
	toks, _ := parser.Lex("SELECT -5")
	nstmts, err := ParseOne(toks, 0)
	if err != nil {
		t.Fatal(err)
	}
	sel := nstmts[0].(*parser.SelectStmt)
	expr, ok := sel.Targets[0].Expr.(*parser.IntegerConst)
	if !ok || expr.Value != -5 {
		t.Fatalf("yacc SELECT -5 target = %#v, want folded IntegerConst{-5}", sel.Targets[0].Expr)
	}

	lstmts, err := parser.Parse("SELECT -5")
	if err != nil {
		t.Fatal(err)
	}
	lsel := lstmts[0].(*parser.SelectStmt)
	if _, ok := lsel.Targets[0].Expr.(*parser.UnaryOp); !ok {
		t.Fatalf("legacy SELECT -5 target = %#v, want UnaryOp (documents the divergence)", lsel.Targets[0].Expr)
	}
}

// TestKnownDiffSelectAll pins the SELECT ALL row: accepted upstream and by
// the yacc parser; the legacy parser rejects it.
func TestKnownDiffSelectAll(t *testing.T) {
	toks, _ := parser.Lex("SELECT ALL a FROM t")
	if _, err := ParseOne(toks, 0); err != nil {
		t.Fatalf("yacc rejected SELECT ALL: %v", err)
	}
	if _, err := parser.Parse("SELECT ALL a FROM t"); err == nil {
		t.Fatal("legacy unexpectedly accepts SELECT ALL; update the known-diffs table")
	}
}
