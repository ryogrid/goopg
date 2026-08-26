package sqlparser

import "testing"

// TestBatchCShapesParity — five more constructs the upstream isolation specs
// use that had no grammar, all on routed statement classes.
//
//   - `CREATE TABLE c ()` — the column list was mandatory, but an empty one is
//     legal and pairs with INHERITS.
//   - `VALUES (1, DEFAULT)` — the DEFAULT placeholder is not an a_expr, so the
//     row list needs its own item rule.
//   - `CREATE INDEX ... WHERE pred` — partial indexes.
//   - `ON CONFLICT (lower(k))` — arbiter items may be EXPRESSIONS. Parsed as
//     a_expr and classified afterwards (a bare unqualified ColumnRef is the
//     column form) rather than as a ColId-or-a_expr alternative, which would
//     be ambiguous.
//   - `text 'x'` / `json '{}'` — gram.y allows any Typename before a string.
//     The fold was gated on IDENT/TIME/TIMESTAMP terminals, so it caught
//     `date '...'` but not the type names that are col_name_keywords.
func TestBatchCShapesParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE c1 ()",
		"CREATE TABLE c1 () INHERITS (p)",
		"INSERT INTO t VALUES (DEFAULT)",
		"INSERT INTO t VALUES (1, DEFAULT)",
		"INSERT INTO t VALUES (1, DEFAULT), (2, 3)",
		"CREATE INDEX i ON t (a) WHERE a > 1",
		"CREATE UNIQUE INDEX i ON t (a) WHERE a IS NOT NULL",
		"CREATE INDEX CONCURRENTLY i ON t (a) WHERE a > 1",
		"INSERT INTO t(k) VALUES('x') ON CONFLICT (lower(k)) DO NOTHING",
		"INSERT INTO t(k) VALUES('x') ON CONFLICT (k) DO NOTHING",
		"INSERT INTO t(k) VALUES('x') ON CONFLICT (k, lower(v)) DO NOTHING",
		"SELECT text 'newValue'",
		"SELECT json '{}'",
		"SELECT jsonb '{}'",
		"SELECT date '2020-01-01'",
		"SELECT timestamp '2020-01-01 00:00:00'",
		// the plain forms must stay identical
		"CREATE TABLE t (a int)",
		"CREATE INDEX i ON t (a)",
		"INSERT INTO t VALUES (1)",
		"SELECT a FROM t",
	} {
		assertParity(t, q)
	}
}
