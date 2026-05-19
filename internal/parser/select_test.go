package parser

import (
	"testing"
)

// TestParseSelectConstant pins the simplest form: SELECT 1, mirroring
// the libpq smoke-test query.
func TestParseSelectConstant(t *testing.T) {
	stmts, err := Parse("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(s.Targets) != 1 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	ic, ok := s.Targets[0].Expr.(*IntegerConst)
	if !ok || ic.Value != 1 {
		t.Errorf("target=%+v", s.Targets[0].Expr)
	}
	if s.From != nil || s.Where != nil {
		t.Errorf("expected no FROM/WHERE, got %+v / %+v", s.From, s.Where)
	}
	if s.FromExprs != nil {
		t.Errorf("expected no FROM exprs, got %+v", s.FromExprs)
	}
}

// TestParseSelectPgbenchSelectOnly: the canonical query the
// pgbench `--select-only` script issues.
func TestParseSelectPgbenchSelectOnly(t *testing.T) {
	stmts, err := Parse("SELECT abalance FROM pgbench_accounts WHERE aid = $1")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Targets) != 1 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	cr, ok := s.Targets[0].Expr.(*ColumnRef)
	if !ok || cr.Column != "abalance" {
		t.Fatalf("target=%+v", s.Targets[0].Expr)
	}
	if len(s.From) != 1 || s.From[0].Name != "pgbench_accounts" {
		t.Fatalf("from=%+v", s.From)
	}
	bo, ok := s.Where.(*BinaryOp)
	if !ok || bo.Op != OpEq {
		t.Fatalf("where=%+v", s.Where)
	}
	if l, ok := bo.Left.(*ColumnRef); !ok || l.Column != "aid" {
		t.Errorf("where.Left=%+v", bo.Left)
	}
	if pr, ok := bo.Right.(*ParamRef); !ok || pr.Number != 1 {
		t.Errorf("where.Right=%+v", bo.Right)
	}
}

// TestParseSelectStarTargetAndAlias: SELECT *, qualified star, AS alias.
func TestParseSelectStarTargetAndAlias(t *testing.T) {
	stmts, err := Parse("SELECT a.*, b.x AS bx FROM t1 AS a, t2 b")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Targets) != 2 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	star, ok := s.Targets[0].Expr.(*StarExpr)
	if !ok || star.Table != "a" {
		t.Fatalf("targets[0]=%+v", s.Targets[0].Expr)
	}
	if s.Targets[1].Alias != "bx" {
		t.Errorf("targets[1].Alias=%q", s.Targets[1].Alias)
	}
	if len(s.From) != 2 || s.From[0].Alias != "a" || s.From[1].Alias != "b" {
		t.Errorf("from=%+v", s.From)
	}
}

// TestParseSelectExpressionPrecedence verifies that operator
// precedence binds AND tighter than OR, and arithmetic tighter than
// comparisons. Tree shape is checked with a small string formatter so
// the assertion is compact.
func TestParseSelectExpressionPrecedence(t *testing.T) {
	stmts, err := Parse("SELECT 1 WHERE a = 1 + 2 * 3 OR b > 4 AND c < 5")
	if err != nil {
		t.Fatal(err)
	}
	got := exprString(stmts[0].(*SelectStmt).Where)
	want := "((a = (1 + (2 * 3))) OR ((b > 4) AND (c < 5)))"
	if got != want {
		t.Errorf("expr = %s\nwant   %s", got, want)
	}
}

// TestParseSelectOrderLimitOffset: trailing clauses parse as the right
// node types and integers attach to LIMIT/OFFSET.
func TestParseSelectOrderLimitOffset(t *testing.T) {
	stmts, err := Parse("SELECT x FROM t ORDER BY x DESC, y LIMIT 10 OFFSET 5")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.OrderBy) != 2 || !s.OrderBy[0].Desc || s.OrderBy[1].Desc {
		t.Errorf("orderby=%+v", s.OrderBy)
	}
	if li, ok := s.Limit.(*IntegerConst); !ok || li.Value != 10 {
		t.Errorf("limit=%+v", s.Limit)
	}
	if oi, ok := s.Offset.(*IntegerConst); !ok || oi.Value != 5 {
		t.Errorf("offset=%+v", s.Offset)
	}
}

func TestParseSelectJoins(t *testing.T) {
	in := "SELECT a.id FROM t1 a INNER JOIN t2 b ON a.id = b.id LEFT JOIN t3 c USING (id) CROSS JOIN t4 d"
	stmts, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.FromExprs) != 1 {
		t.Fatalf("fromExprs=%d", len(s.FromExprs))
	}
	if len(s.From) != 4 {
		t.Fatalf("from(flat)=%d", len(s.From))
	}
	joins := s.FromExprs[0].Joins
	if len(joins) != 3 {
		t.Fatalf("joins=%d", len(joins))
	}
	if joins[0].Type != JoinInner || joins[0].On == nil {
		t.Errorf("joins[0]=%+v", joins[0])
	}
	if joins[1].Type != JoinLeft || len(joins[1].Using) != 1 || joins[1].Using[0] != "id" {
		t.Errorf("joins[1]=%+v", joins[1])
	}
	if joins[2].Type != JoinCross || joins[2].On != nil || len(joins[2].Using) != 0 {
		t.Errorf("joins[2]=%+v", joins[2])
	}
}

func TestParseSelectGroupByHaving(t *testing.T) {
	stmts, err := Parse("SELECT a, sum(b) FROM t GROUP BY a HAVING sum(b) > 10")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.GroupBy) != 1 {
		t.Fatalf("groupBy=%d", len(s.GroupBy))
	}
	if c, ok := s.GroupBy[0].(*ColumnRef); !ok || c.Column != "a" {
		t.Errorf("groupBy[0]=%+v", s.GroupBy[0])
	}
	h, ok := s.Having.(*BinaryOp)
	if !ok || h.Op != OpGt {
		t.Fatalf("having=%+v", s.Having)
	}
}

func TestParseSelectSetOps(t *testing.T) {
	stmts, err := Parse("SELECT 1 UNION ALL SELECT 2 INTERSECT SELECT 3")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.SetOp == nil || s.SetOp.Type != SetOpUnion || !s.SetOp.All {
		t.Fatalf("setop=%+v", s.SetOp)
	}
	rhs := s.SetOp.Right
	if rhs == nil || rhs.SetOp == nil || rhs.SetOp.Type != SetOpIntersect || rhs.SetOp.All {
		t.Fatalf("rhs setop=%+v", rhs)
	}
}

func TestParseSelectNaturalJoin(t *testing.T) {
	stmts, err := Parse("SELECT * FROM a NATURAL JOIN b")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.FromExprs) != 1 || len(s.FromExprs[0].Joins) != 1 {
		t.Fatalf("fromExprs=%+v", s.FromExprs)
	}
	j := s.FromExprs[0].Joins[0]
	if !j.Natural || j.Type != JoinInner || j.On != nil || len(j.Using) != 0 {
		t.Errorf("join=%+v", j)
	}
}

// TestParseSelectFunctionCall: count(*) and sum(distinct x) shapes.
func TestParseSelectFunctionCall(t *testing.T) {
	stmts, err := Parse("SELECT count(*), sum(DISTINCT x) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	c, ok := s.Targets[0].Expr.(*FuncCall)
	if !ok || c.Name.Name != "count" || !c.Star {
		t.Fatalf("count target=%+v", s.Targets[0].Expr)
	}
	d, ok := s.Targets[1].Expr.(*FuncCall)
	if !ok || d.Name.Name != "sum" || !d.Distinct || len(d.Args) != 1 {
		t.Fatalf("sum target=%+v", s.Targets[1].Expr)
	}
}


// TestParseFuncCallVariadicArgument pins parser acceptance of the VARIADIC
// keyword as a prefix on a function-call argument. libpqrcv's
// fetch_table_list probe emits this shape against
// `pg_get_publication_tables`; the previous parser rejected it with a
// "syntax error at or near 'variadic'" before reaching the planner.
// M0103-0008.
func TestParseFuncCallVariadicArgument(t *testing.T) {
	stmts, err := Parse("SELECT pg_get_publication_tables(VARIADIC array_agg(x))")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmts[0].(*SelectStmt)
	fc, ok := s.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("expected FuncCall, got %T", s.Targets[0].Expr)
	}
	if fc.Name.Name != "pg_get_publication_tables" {
		t.Fatalf("function name = %q, want pg_get_publication_tables", fc.Name.Name)
	}
	if len(fc.Args) != 1 || len(fc.Variadic) != 1 {
		t.Fatalf("args=%d variadic=%d, want 1/1", len(fc.Args), len(fc.Variadic))
	}
	if !fc.Variadic[0] {
		t.Fatalf("expected VARIADIC flag on arg 0, got false")
	}
}


// TestParseLateralPgCatalogQualifiedSRF pins M0103-0008 rung 13: the
// parser's FROM-clause TVF dispatch must accept both unqualified and
// `pg_catalog`-qualified spellings of the v0 SRF whitelist. Without
// this, PG's `fetch_table_list_from_publisher` probe (which uses
// `LATERAL pg_catalog.pg_get_publication_tables(...)`) hits
// "expected ')' after subquery in FROM (got ()" at the function's
// opening paren and CREATE SUBSCRIPTION registers zero tables in
// `pg_subscription_rel`, which causes the apply worker to silently
// skip every Insert/Update/Delete via `should_apply_changes_for_rel`.
// See docs/design/0103-0019-lateral-pg-catalog-qualified-srf.md.
func TestParseLateralPgCatalogQualifiedSRF(t *testing.T) {
	// Canonical libpqwalreceiver shape: cross-FROM with LATERAL +
	// schema-qualified SRF. The arg references the left FROM item's
	// column so the planner threads the lateralCtx through (closed
	// in rung 6/7). The parser side only needs to accept the
	// pg_catalog.<name>(...) spelling as an SRF instead of falling
	// into the derived-subquery branch.
	stmts, err := Parse(`SELECT gpt.relid FROM pg_publication t, LATERAL pg_catalog.pg_get_publication_tables(t.pubname) AS gpt`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("expected *SelectStmt, got %T", stmts[0])
	}
	if len(sel.From) != 2 {
		t.Fatalf("expected 2 FROM items (t, gpt), got %d", len(sel.From))
	}
	// The lateral SRF should land as a RangeVar whose TableFunc is
	// the `pg_get_publication_tables` SRF — the `pg_catalog.` prefix
	// is discarded once dispatch fires.
	gpt := sel.From[1]
	if gpt.TableFunc == nil {
		t.Fatalf("expected From[1] to be a TableFuncRef, got Schema=%q Name=%q Subquery=%v",
			gpt.Schema, gpt.Name, gpt.Subquery != nil)
	}
	if gpt.TableFunc.Name != "pg_get_publication_tables" {
		t.Fatalf("From[1].TableFunc.Name = %q, want pg_get_publication_tables",
			gpt.TableFunc.Name)
	}
	if gpt.Alias != "gpt" {
		t.Fatalf("From[1].Alias = %q, want gpt", gpt.Alias)
	}
}

// TestParseLateralPgCatalogQualifiedSRFCaseInsensitive checks that
// the `pg_catalog` schema-qualifier match is case-insensitive (the
// upstream probe always uses lowercase, but goopg's identifier
// downcasing happens earlier; this test pins the EqualFold guard
// against accidental tightening).
func TestParseLateralPgCatalogQualifiedSRFCaseInsensitive(t *testing.T) {
	stmts, err := Parse(`SELECT 1 FROM pg_publication t, LATERAL PG_CATALOG.pg_get_publication_tables(t.pubname) AS gpt`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	gpt := sel.From[1]
	if gpt.TableFunc == nil || gpt.TableFunc.Name != "pg_get_publication_tables" {
		t.Fatalf("PG_CATALOG-prefix did not dispatch SRF; From[1].TableFunc=%v", gpt.TableFunc)
	}
}


// TestParseRangeVarBareAliasWithColumnList pins the
// `tablename alias (col1, col2, ...)` shape — column-alias list
// after a bare (no-AS) alias.  Used by upstream's MERGE JOIN
// isolation spec for `INSERT INTO src SELECT x, x*10 FROM
// generate_series(1,3) g(x);` and by the long-tail of regression
// tests that omit AS.
func TestParseRangeVarBareAliasWithColumnList(t *testing.T) {
	stmts, err := Parse(`SELECT g.x FROM generate_series(1,3) g(x)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	if len(sel.From) != 1 {
		t.Fatalf("expected 1 FROM item, got %d", len(sel.From))
	}
	rv := sel.From[0]
	if rv.TableFunc == nil || rv.TableFunc.Name != "generate_series" {
		t.Fatalf("expected generate_series TableFunc, got %+v", rv)
	}
	if rv.Alias != "g" {
		t.Fatalf("Alias=%q, want g", rv.Alias)
	}
	if len(rv.Columns) != 1 || rv.Columns[0] != "x" {
		t.Fatalf("Columns=%v, want [x]", rv.Columns)
	}
}

// TestParseRangeVarBareAliasMultiColumnList covers a multi-column
// alias list on a bare alias (no AS keyword).
func TestParseRangeVarBareAliasMultiColumnList(t *testing.T) {
	stmts, err := Parse(`SELECT t.a, t.b FROM mytable t (a, b)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	rv := sel.From[0]
	if rv.Name != "mytable" {
		t.Fatalf("Name=%q, want mytable", rv.Name)
	}
	if rv.Alias != "t" {
		t.Fatalf("Alias=%q, want t", rv.Alias)
	}
	if len(rv.Columns) != 2 || rv.Columns[0] != "a" || rv.Columns[1] != "b" {
		t.Fatalf("Columns=%v, want [a b]", rv.Columns)
	}
}


// TestParseIndirectionStarFuncCall — `(srf(args)).*` in target list emits
// an IndirectionStar AST node wrapping the inner FuncCall. M0103-0008
// probe-survival foundation.
func TestParseIndirectionStarFuncCall(t *testing.T) {
	// parseSelect runs RewriteIndirectionStarTargets at the end of the
	// parse so non-aggregate (srf(consts)).* shapes are turned into a
	// FROM-clause SRF reference plus a qualified `__irs_0.*` target.
	// The IndirectionStar AST node only persists in the parse tree
	// when the SRF arguments contain aggregates (aggregate-arg path is
	// rejected by the planner pending ProjectSet support).
	stmts, err := Parse("SELECT (pg_get_publication_tables('p')).*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmts[0].(*SelectStmt)
	star, ok := s.Targets[0].Expr.(*StarExpr)
	if !ok {
		t.Fatalf("after rewrite, target = %T, want *StarExpr", s.Targets[0].Expr)
	}
	if star.Table != "__irs_0" {
		t.Fatalf("after rewrite, target qualifier = %q, want __irs_0", star.Table)
	}
	if len(s.From) != 1 {
		t.Fatalf("after rewrite, len(From) = %d, want 1", len(s.From))
	}
	tf := s.From[0].TableFunc
	if tf == nil {
		t.Fatalf("after rewrite, From[0].TableFunc is nil")
	}
	if tf.Name != "pg_get_publication_tables" {
		t.Fatalf("rewritten SRF name = %q", tf.Name)
	}
	if s.From[0].Alias != "__irs_0" {
		t.Fatalf("rewritten SRF alias = %q, want __irs_0", s.From[0].Alias)
	}
}

// TestParseIndirectionStarFetchTableList — pins parse of the upstream
// libpqrcv fetch_table_list shape end-to-end (subquery with VARIADIC
// array_agg + composite expansion). M0103-0008 probe-survival.
func TestParseIndirectionStarFetchTableList(t *testing.T) {
	q := `SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ('p') ) AS gpt
    ON gpt.relid = c.oid`
	if _, err := Parse(q); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// TestParseFuncCallVariadicMixed pins parser acceptance of VARIADIC on the
// trailing argument of a multi-argument call. Non-VARIADIC arguments must
// retain Variadic[i]=false.
func TestParseFuncCallVariadicMixed(t *testing.T) {
	stmts, err := Parse("SELECT f(1, VARIADIC x)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmts[0].(*SelectStmt)
	fc, ok := s.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("expected FuncCall, got %T", s.Targets[0].Expr)
	}
	if len(fc.Args) != 2 || len(fc.Variadic) != 2 {
		t.Fatalf("args=%d variadic=%d, want 2/2", len(fc.Args), len(fc.Variadic))
	}
	if fc.Variadic[0] {
		t.Fatalf("arg 0 unexpectedly marked VARIADIC")
	}
	if !fc.Variadic[1] {
		t.Fatalf("arg 1 expected VARIADIC, got false")
	}
}

// TestParseSelectSyntaxErrors pins error positions for the canonical
// "missing piece" cases.
func TestParseSelectSyntaxErrors(t *testing.T) {
	cases := []string{
		// "SELECT" is now valid — returns 1 empty row matching PG behaviour.
		"SELECT 1 FROM",     // no table after FROM
		"SELECT 1 WHERE",    // no expression after WHERE
		"SELECT 1 ORDER BY", // no expression after ORDER BY
		"SELECT 1 FROM t JOIN u",
		"SELECT 1 FROM t NATURAL",
		"SELECT 1 GROUP",
		"SELECT 1 UNION",
	}
	for _, in := range cases {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) expected error", in)
			continue
		}
		if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) err type=%T", in, err)
		}
	}
}

// exprString is a tiny helper for the precedence test — it prints the
// expression tree fully parenthesised so the test asserts shape, not
// string formatting choices.
func exprString(e Expr) string {
	switch x := e.(type) {
	case *IntegerConst:
		return itoa(x.Value)
	case *StringConst:
		return "'" + x.Value + "'"
	case *NullConst:
		return "NULL"
	case *BooleanConst:
		if x.Value {
			return "TRUE"
		}
		return "FALSE"
	case *ParamRef:
		return "$" + itoa(int64(x.Number))
	case *ColumnRef:
		s := ""
		if x.Schema != "" {
			s += x.Schema + "."
		}
		if x.Table != "" {
			s += x.Table + "."
		}
		return s + x.Column
	case *BinaryOp:
		return "(" + exprString(x.Left) + " " + x.Op.String() + " " + exprString(x.Right) + ")"
	case *UnaryOp:
		return "(" + x.Op.String() + " " + exprString(x.Operand) + ")"
	case *FuncCall:
		args := ""
		if x.Star {
			args = "*"
		} else {
			for i, a := range x.Args {
				if i > 0 {
					args += ", "
				}
				args += exprString(a)
			}
		}
		return x.Name.String() + "(" + args + ")"
	}
	return "?"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
