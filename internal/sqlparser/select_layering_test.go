package sqlparser

import "testing"

// TestParenthesisedSetOpOperands pins the SELECT core's three-tier layering
// (SelectStmt / select_with_parens / select_no_parens, gram.y :12823). It was
// the larger of the two structural blockers: 14 must-pass regress fragments
// (select_distinct, union) need a parenthesised LEFT operand, and every flat
// spelling of it produced reduce/reduce conflicts on ')' that rule order broke
// the wrong way.
//
// The legacy shapes reproduced here (parseParenthesisedSelectStmt): `(S)`
// alone is S stamped Parenthesized; anything written after the ')' — a set
// operation, ORDER BY, LIMIT, OFFSET — hangs off a fresh WRAPPER node whose
// SetOpOperand is S, with ORDER BY / LIMIT / OFFSET lifted off a bare right
// branch exactly as foldSetOps lifts them.
func TestParenthesisedSetOpOperands(t *testing.T) {
	for _, q := range []string{
		"(SELECT 1)",
		"((SELECT 1))",
		"(SELECT 1) UNION (SELECT 2)",
		"(SELECT 1) UNION SELECT 2",
		"(SELECT 1 UNION SELECT 2) UNION SELECT 3",
		"SELECT 1 UNION (SELECT 2 UNION SELECT 3)",
		"(SELECT 1) EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
		"(SELECT a FROM t1 EXCEPT SELECT a FROM t2) UNION ALL (SELECT a FROM t3)",
		"(SELECT * FROM distinct_hash_1 EXCEPT SELECT * FROM distinct_group_1) UNION ALL (SELECT * FROM distinct_group_1 EXCEPT SELECT * FROM distinct_hash_1)",
		// Trailing clauses on the wrapper, and the lift off a bare right branch.
		"(SELECT 1) ORDER BY 1",
		"(SELECT 1 ORDER BY 1) LIMIT 1",
		"(SELECT 1) LIMIT 2 OFFSET 1",
		"(SELECT 1) OFFSET 3",
		"(SELECT 1) UNION (SELECT 2) ORDER BY 1",
		"(SELECT 1) UNION SELECT 2 ORDER BY 1 LIMIT 1",
		// A parenthesised RIGHT operand keeps its own clauses.
		"SELECT 1 UNION (SELECT 2 ORDER BY 1)",
		"SELECT 1 UNION (SELECT 2)",
		"SELECT 1 UNION ((SELECT 2))",
		// The unparenthesised chain is unchanged: right-recursive, no
		// INTERSECT-over-UNION precedence, trailing clauses lifted to the base.
		"SELECT 1 UNION SELECT 2 UNION SELECT 3",
		"SELECT 1 INTERSECT SELECT 2 UNION SELECT 3",
		"SELECT 1 UNION SELECT 2 ORDER BY 1 LIMIT 1 OFFSET 2",
		"SELECT 1 UNION SELECT 2 FOR UPDATE",
		"SELECT 1 ORDER BY 1 UNION SELECT 2",
		"WITH c AS (SELECT 1) SELECT 2 UNION SELECT 3",
	} {
		assertParity(t, q)
	}
	// Legacy's trailingClauseFollowsParens admits exactly UNION / INTERSECT /
	// EXCEPT / ORDER / LIMIT / OFFSET, in that order, with plain expressions.
	// Widening past it would accept statements the legacy parser rejects.
	for _, q := range []string{
		"(SELECT 1) FOR UPDATE",
		"(SELECT 1) FETCH FIRST 1 ROWS ONLY",
		"(SELECT 1) OFFSET 1 LIMIT 2",
		"(SELECT 1) LIMIT ALL",
		"WITH c AS (SELECT 1) (SELECT 2)",
		// select_bare: a view body or CTAS source may not START with '('.
		"CREATE VIEW v AS (SELECT 1)",
		"CREATE TABLE t AS (SELECT 1)",
	} {
		assertBothReject(t, q)
	}
}

// TestSubqueryParenStamping pins WHICH consumers stamp Parenthesized on a
// single pair of parens. select_with_parens is shared by all of them, so the
// stamp has to be the consumer's decision, and legacy is not uniform: a
// derived table and a set-op right operand stamp; a scalar subquery, EXISTS,
// IN, ANY and a CTE body do NOT (their parser calls parseSelect inside its own
// parens), and only a NESTED pair marks those. The old scalar-subquery action
// stamped unconditionally, so every `SELECT (SELECT ...)` was a parity diff.
func TestSubqueryParenStamping(t *testing.T) {
	for _, q := range []string{
		"SELECT (SELECT 1)",
		"SELECT ((SELECT 1))",
		"SELECT f((SELECT 1))",
		"SELECT f(((SELECT 1)))",
		"SELECT ((SELECT 1) UNION SELECT 2)",
		"SELECT (SELECT 1) UNION SELECT 2",
		"SELECT * FROM (SELECT 1) x",
		"SELECT * FROM ((SELECT 1)) x",
		"SELECT * FROM LATERAL (SELECT 1) x",
		"SELECT * FROM (SELECT 1) x, (SELECT 2) y",
		"SELECT * FROM t JOIN (SELECT 1) x ON true",
		"SELECT * FROM (TABLE int2_tbl) AS s (a, b)",
		"SELECT * FROM (VALUES (1)) v(a)",
		"SELECT EXISTS (SELECT 1)",
		"SELECT EXISTS ((SELECT 1))",
		"SELECT a IN (SELECT 1) FROM t",
		"SELECT a IN ((SELECT 1)) FROM t",
		"SELECT a NOT IN (SELECT 1) FROM t",
		"SELECT a > ALL (SELECT 1) FROM t",
		"SELECT ARRAY(SELECT 1)",
		"WITH c AS (SELECT 1) SELECT 2",
		"WITH c AS ((SELECT 1)) SELECT 2",
		"INSERT INTO t (SELECT 1)",
		"INSERT INTO t ((SELECT 1))",
		"INSERT INTO t (a) (SELECT 1)",
		"INSERT INTO t (a, b) SELECT 1, 2",
		"SELECT * INTO t FROM (SELECT 1) x",
	} {
		assertParity(t, q)
	}
}

// TestQuantifiedAnyDesugar pins legacy's `= ANY` / `<> ANY` / SOME desugaring
// on SUBQUERIES: `= ANY` is IN (AnyOp stays OpUnknown) and `<> ANY` is the IN
// shape flagged NotEqualAny — an OR of inequalities, deliberately not NOT IN.
// The list path already did this; the subquery path carried the raw operator,
// and SOME was not accepted with a subquery at all. `= ALL` is the documented
// known-diff (difftest_known_diffs.md) and is deliberately absent here.
func TestQuantifiedAnyDesugar(t *testing.T) {
	for _, q := range []string{
		"SELECT a = ANY (SELECT 1) FROM t",
		"SELECT a <> ANY (SELECT 1) FROM t",
		"SELECT a != ANY (SELECT 1) FROM t",
		"SELECT a > ANY (SELECT 1) FROM t",
		"SELECT a = SOME (SELECT 1) FROM t",
		"SELECT a < SOME (SELECT 1) FROM t",
		// The list path: a direct ARRAY[...] is spliced into List and its
		// trailing casts dropped, exactly as parseAnyTail does; a parenthesised
		// one is an ordinary expression and stays wrapped.
		"SELECT a = ANY (ARRAY[1]) FROM t",
		"SELECT a = ANY (ARRAY[1, 2]) FROM t",
		"SELECT a <> ANY (ARRAY[1]) FROM t",
		"SELECT a = SOME (ARRAY[1]) FROM t",
		"SELECT a > ANY (ARRAY[1]) FROM t",
		"SELECT a > ALL (ARRAY[1]) FROM t",
		"SELECT a = ANY (ARRAY[1]::int[]) FROM t",
		"SELECT a = ANY ((ARRAY[1])) FROM t",
		"SELECT a = ANY ('{1}') FROM t",
		"SELECT a = ANY (b) FROM t",
	} {
		assertParity(t, q)
	}
}
