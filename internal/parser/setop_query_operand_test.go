package parser

// setop_query_operand_test.go — M0125-0018.
//
// The IN / EXISTS / ANY-SOME-ALL operand parsers must choose between two
// gram.y productions that both begin with '(':
//
//	in_expr:  select_with_parens | '(' expr_list ')'
//	sub_type: … select_with_parens | … '(' a_expr ')'
//	select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'
//
// They used to decide by looking at ONE token past the '(', so any nested '('
// — the shape every parenthesised set-op chain has — chose the expression
// alternative. `selectWithParensAhead` makes the decision properly, and these
// tests pin the AST SHAPE it produces, which the by-value executor tests in
// internal/executor/setop_query_operand_test.go cannot distinguish for the
// cases where both readings happen to agree numerically.
//
// The `wantSubquery: false` rows are the ones that matter most: `((SELECT 1),
// (SELECT 2))` and `((SELECT 1)::int)` are expressions in PostgreSQL 18.3
// (verified on the oracle, port 65438) even though they start with the same
// two tokens as a parenthesised query.

import "testing"

// inExprOfWhere digs the WHERE clause of a single-statement SELECT out as an
// *InExpr, which is the node both IN and ANY/SOME/ALL desugar to.
func inExprOfWhere(t *testing.T, sql string) *InExpr {
	t.Helper()
	sel := parseOneSelect(t, sql)
	in, ok := sel.Where.(*InExpr)
	if !ok {
		t.Fatalf("Parse(%q): WHERE is %T, want *InExpr", sql, sel.Where)
	}
	return in
}

// TestQueryOperandVsExpressionList pins which gram.y production each operand
// shape resolves to.
func TestQueryOperandVsExpressionList(t *testing.T) {
	cases := []struct {
		name         string
		sql          string
		wantSubquery bool
		// wantChain is the number of SelectStmts in the operand's set-op
		// chain; checked only when wantSubquery.
		wantChain int
	}{
		{
			name:         "in_parenthesised_except_chain",
			sql:          "SELECT 1 WHERE x IN ((SELECT a FROM t) EXCEPT (SELECT a FROM u) EXCEPT (SELECT a FROM v))",
			wantSubquery: true, wantChain: 3,
		},
		{
			// The quiet case: one row of parens, still a query.
			name:         "in_bare_nested_parens",
			sql:          "SELECT 1 WHERE x IN ((SELECT a FROM t))",
			wantSubquery: true, wantChain: 1,
		},
		{
			name:         "in_double_parenthesised_chain",
			sql:          "SELECT 1 WHERE x IN (((SELECT a FROM t) EXCEPT (SELECT a FROM u)))",
			wantSubquery: true, wantChain: 2,
		},
		{
			name:         "in_nested_parens_values",
			sql:          "SELECT 1 WHERE x IN ((VALUES (1),(2)))",
			wantSubquery: true, wantChain: 1,
		},
		{
			name:         "any_parenthesised_union",
			sql:          "SELECT 1 WHERE x = ANY ((SELECT a FROM t) UNION (SELECT a FROM u))",
			wantSubquery: true, wantChain: 2,
		},
		{
			name:         "all_parenthesised_except",
			sql:          "SELECT 1 WHERE x <> ALL ((SELECT a FROM t) EXCEPT (SELECT a FROM u))",
			wantSubquery: true, wantChain: 2,
		},
		{
			name:         "in_unparenthesised_chain_unchanged",
			sql:          "SELECT 1 WHERE x IN (SELECT a FROM t EXCEPT SELECT a FROM u)",
			wantSubquery: true, wantChain: 2,
		},

		// --- the expression alternative -----------------------------------
		{
			// A `,` after the first group's ')' means expr_list.
			name:         "in_expr_list_of_scalar_subqueries",
			sql:          "SELECT 1 WHERE x IN ((SELECT a FROM t),(SELECT a FROM u))",
			wantSubquery: false,
		},
		{
			// A cast after the ')' likewise: PG answers `t` to
			// `select 1 in ((select 1)::int)`.
			name:         "in_cast_of_scalar_subquery",
			sql:          "SELECT 1 WHERE x IN ((SELECT a FROM t)::int)",
			wantSubquery: false,
		},
		{
			name:         "in_arithmetic_on_scalar_subquery",
			sql:          "SELECT 1 WHERE x IN ((SELECT a FROM t) + 1)",
			wantSubquery: false,
		},
		{
			name:         "in_parenthesised_value_list",
			sql:          "SELECT 1 WHERE x IN ((1),(2))",
			wantSubquery: false,
		},
		{
			name:         "in_plain_value_list",
			sql:          "SELECT 1 WHERE x IN (1,2)",
			wantSubquery: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := inExprOfWhere(t, c.sql)
			if c.wantSubquery {
				if in.Subquery == nil {
					t.Fatalf("%s\nparsed as expression list (%d elems), want select_with_parens",
						c.sql, len(in.List))
				}
				if got := len(chainOf(in.Subquery)); got != c.wantChain {
					t.Errorf("%s\n chain length=%d, want %d", c.sql, got, c.wantChain)
				}
			} else if in.Subquery != nil {
				t.Fatalf("%s\nparsed as select_with_parens, want an expression list", c.sql)
			}
		})
	}
}

// TestExistsAcceptsParenthesisedChain covers EXISTS, whose operand is a plain
// *SelectStmt rather than an *InExpr.
func TestExistsAcceptsParenthesisedChain(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		wantChain int
	}{
		{"parenthesised_except_chain",
			"SELECT 1 WHERE EXISTS ((SELECT a FROM t) EXCEPT (SELECT a FROM u))", 2},
		{"bare_nested_parens",
			"SELECT 1 WHERE EXISTS ((SELECT a FROM t))", 1},
		{"chain_with_order_by_limit",
			"SELECT 1 WHERE EXISTS ((SELECT a FROM t) EXCEPT (SELECT a FROM u) ORDER BY 1 LIMIT 1)", 2},
		{"unparenthesised_chain_unchanged",
			"SELECT 1 WHERE EXISTS (SELECT a FROM t UNION SELECT a FROM u)", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := parseOneSelect(t, c.sql)
			ex, ok := sel.Where.(*ExistsExpr)
			if !ok {
				t.Fatalf("Parse(%q): WHERE is %T, want *ExistsExpr", c.sql, sel.Where)
			}
			if got := len(chainOf(ex.Subquery)); got != c.wantChain {
				t.Errorf("%s\n chain length=%d, want %d", c.sql, got, c.wantChain)
			}
		})
	}
}

// TestExistsStillRejectsNonQueryParens keeps EXISTS' operand restricted to
// select_with_parens: gram.y offers it no expression alternative, so
// `EXISTS ((1))` must stay a syntax error rather than becoming a row test on a
// constant.
func TestExistsStillRejectsNonQueryParens(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 WHERE EXISTS ((1))",
		"SELECT 1 WHERE EXISTS (1)",
	} {
		if _, err := Parse(sql); err == nil {
			t.Errorf("Parse(%q): want syntax error, got none", sql)
		}
	}
}
