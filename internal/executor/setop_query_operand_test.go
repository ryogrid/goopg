package executor

// setop_query_operand_test.go — M0125-0018, accepted BY VALUE.
//
// PostgreSQL's grammar makes a parenthesised query a first-class operand of the
// three sublink-bearing constructs (postgres/src/backend/parser/gram.y):
//
//	a_expr IN_P in_expr        in_expr:  select_with_parens | '(' expr_list ')'
//	EXISTS select_with_parens
//	a_expr subquery_Op sub_type select_with_parens
//	select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'
//
// goopg's three operand parsers each decided "query or expression?" by asking
// whether the very next token after `(` was SELECT/VALUES. A parenthesised
// set-op chain starts with a NESTED `(`, so every one of them fell through to
// the expression alternative. That produced two different failures:
//
//   - `x IN ((A) EXCEPT (B))` and `EXISTS ((A) EXCEPT (B))` raised a syntax
//     error ("expected ')' to close IN list (got except)" / "EXISTS requires a
//     parenthesised SELECT (got (") — a loud failure; and
//   - `x IN ((SELECT …))` silently parsed as a value list holding ONE scalar
//     subquery, so a multi-row inner query raised 21000 "more than one row
//     returned by a subquery used as an expression" where PostgreSQL simply
//     answers the IN. That one is the quiet, wrong-answer half.
//
// Every `want` below was captured from PostgreSQL 18.3 (the read-only oracle,
// port 65438) running the identical statement against the identical fixture,
// not derived from goopg. Fixture and helpers are shared with
// setop_paren_assoc_test.go:
//
//	a: 1,2,3   b: 2   c: 3   d: 1,3   e: 2,4   f: 4   g: 2,3   h: 9

import "testing"

// TestSetOpChainAsQueryOperand is the acceptance matrix: a parenthesised
// set-op chain (and the bare nested-paren form that shares the code path) used
// as the operand of IN, EXISTS and ANY/ALL, plus the expression-alternative
// controls that must NOT be re-routed to the query production.
func TestSetOpChainAsQueryOperand(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		// --- IN: the reported syntax errors ------------------------------
		{
			// {1,2,3} EXCEPT {2} = {1,3}; a IN that = {1,3}.
			name: "in_parenthesised_except",
			sql:  "SELECT x FROM a WHERE x IN ((SELECT x FROM a) EXCEPT (SELECT x FROM b))",
			want: []int64{1, 3},
		},
		{
			// Left-associative: (({1,2,3} EXCEPT {2}) EXCEPT {3}) = {1}.
			name: "in_parenthesised_except_chain",
			sql: "SELECT x FROM a WHERE x IN ((SELECT x FROM a) EXCEPT (SELECT x FROM b) " +
				"EXCEPT (SELECT x FROM c))",
			want: []int64{1},
		},
		{
			name: "not_in_parenthesised_except",
			sql:  "SELECT x FROM a WHERE x NOT IN ((SELECT x FROM a) EXCEPT (SELECT x FROM b))",
			want: []int64{2},
		},
		{
			// select_with_parens nests, so an extra wrap changes nothing.
			name: "in_double_parenthesised_chain",
			sql:  "SELECT x FROM a WHERE x IN (((SELECT x FROM a) EXCEPT (SELECT x FROM b)))",
			want: []int64{1, 3},
		},

		// --- IN: the QUIET half — `((SELECT …))` is a query, not a scalar --
		{
			// goopg used to read this as a one-element value list holding a
			// scalar subquery and raise 21000 on {1,3}.
			name: "in_bare_nested_parens_multirow",
			sql:  "SELECT x FROM a WHERE x IN ((SELECT x FROM d))",
			want: []int64{1, 3},
		},
		{
			// Same shape with the set-op INSIDE the inner parens. PG answers
			// `t` to `select 1 in ((select 1 union select 2))`, which the
			// scalar reading cannot do.
			name: "in_bare_nested_parens_union",
			sql:  "SELECT x FROM a WHERE x IN ((SELECT x FROM a UNION SELECT x FROM b))",
			want: []int64{1, 2, 3},
		},
		{
			name: "in_bare_nested_parens_values",
			sql:  "SELECT x FROM a WHERE x IN ((VALUES (1)))",
			want: []int64{1},
		},

		// --- EXISTS ------------------------------------------------------
		{
			// {2} EXCEPT {3} = {2}: non-empty, so every row of a survives.
			name: "exists_parenthesised_except",
			sql:  "SELECT x FROM a WHERE EXISTS ((SELECT x FROM b) EXCEPT (SELECT x FROM c))",
			want: []int64{1, 2, 3},
		},
		{
			name: "exists_bare_nested_parens",
			sql:  "SELECT x FROM a WHERE EXISTS ((SELECT x FROM b))",
			want: []int64{1, 2, 3},
		},
		{
			// {2} EXCEPT {2} = {} — NOT EXISTS is therefore true for all rows.
			// Pins that the chain is really evaluated rather than assumed
			// non-empty because it parsed.
			name: "not_exists_empty_parenthesised_except",
			sql:  "SELECT x FROM a WHERE NOT EXISTS ((SELECT x FROM b) EXCEPT (SELECT x FROM b))",
			want: []int64{1, 2, 3},
		},
		{
			// select_no_parens' trailing clauses inside the EXISTS parens.
			name: "exists_chain_with_order_by_limit",
			sql: "SELECT x FROM a WHERE EXISTS ((SELECT x FROM b) EXCEPT (SELECT x FROM c) " +
				"ORDER BY 1 LIMIT 1)",
			want: []int64{1, 2, 3},
		},

		// --- ANY / SOME / ALL — the third sibling (Hard-won Rule #2) ------
		{
			// {2} UNION {3} = {2,3}.
			name: "any_parenthesised_union",
			sql:  "SELECT x FROM a WHERE x = ANY ((SELECT x FROM b) UNION (SELECT x FROM c))",
			want: []int64{2, 3},
		},
		{
			// {2,3} EXCEPT {3} = {2}; x <> ALL {2} keeps {1,3}.
			name: "all_parenthesised_except",
			sql:  "SELECT x FROM a WHERE x <> ALL ((SELECT x FROM g) EXCEPT (SELECT x FROM c))",
			want: []int64{1, 3},
		},
		{
			name: "any_bare_nested_parens_multirow",
			sql:  "SELECT x FROM a WHERE x = ANY ((SELECT x FROM d))",
			want: []int64{1, 3},
		},

		// --- composes with M0125-0017 (head-branch ORDER BY / LIMIT) ------
		{
			// ({1,2,3} ORDER BY 1 LIMIT 2) UNION ALL {3} = {1,2,3}. If the
			// LIMIT escaped to the union the operand would be {1,2} and row 3
			// would drop out.
			name: "in_head_branch_limit_then_union_all",
			sql: "SELECT x FROM a WHERE x IN ((SELECT x FROM a ORDER BY 1 LIMIT 2) " +
				"UNION ALL (SELECT x FROM c))",
			want: []int64{1, 2, 3},
		},

		// --- controls: the EXPRESSION alternative must not be re-routed ---
		{
			// `'(' expr_list ')'` of two scalar subqueries — the `,` after the
			// first group's `)` is what distinguishes it.
			name: "control_in_expr_list_of_scalar_subqueries",
			sql:  "SELECT x FROM a WHERE x IN ((SELECT x FROM c),(SELECT x FROM b))",
			want: []int64{2, 3},
		},
		{
			name: "control_in_parenthesised_value_list",
			sql:  "SELECT x FROM a WHERE x IN ((1),(2))",
			want: []int64{1, 2},
		},
		{
			name: "control_in_plain_value_list",
			sql:  "SELECT x FROM a WHERE x IN (1,2)",
			want: []int64{1, 2},
		},
		{
			name: "control_in_unparenthesised_chain",
			sql:  "SELECT x FROM a WHERE x IN (SELECT x FROM a EXCEPT SELECT x FROM b)",
			want: []int64{1, 3},
		},
		{
			name: "control_exists_unparenthesised_chain",
			sql:  "SELECT x FROM a WHERE EXISTS (SELECT x FROM b UNION SELECT x FROM c)",
			want: []int64{1, 2, 3},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortedInts(t, runQuery(t, ctx, c.sql))
			if !eqInts(got, c.want) {
				t.Errorf("%s\n got=%v\nwant=%v (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}
