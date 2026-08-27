package parser

import "testing"

// TestDataModifyingCTEParity — `WITH x AS (INSERT|UPDATE|DELETE ... RETURNING
// ...) ...`. CommonTableExpr.DMLBody has existed on the AST all along; the
// grammar restricted the CTE body to SelectStmt, so these were syntax errors on
// the routed path. INSERT/UPDATE/DELETE start with distinct reserved keywords,
// so the extra alternative costs no conflict.
func TestDataModifyingCTEParity(t *testing.T) {
	for _, q := range []string{
		"WITH u AS (UPDATE t SET a = 1 RETURNING a) INSERT INTO s SELECT a FROM u",
		"WITH t AS (INSERT INTO c VALUES (1) RETURNING k) SELECT * FROM t",
		"WITH t AS (DELETE FROM c RETURNING k) SELECT * FROM t",
		"WITH t AS (INSERT INTO c VALUES (1) ON CONFLICT (k) DO UPDATE SET v = 2 RETURNING k) SELECT * FROM t",
		"WITH a AS (SELECT 1), b AS (INSERT INTO t VALUES (1) RETURNING x) SELECT * FROM b",
		// plain SELECT CTEs must stay identical
		"WITH a AS (SELECT 1) SELECT * FROM a",
		"WITH RECURSIVE a AS (SELECT 1) SELECT * FROM a",
		"WITH a AS MATERIALIZED (SELECT 1) SELECT * FROM a",
		"WITH a (x) AS (SELECT 1) SELECT * FROM a",
	} {
		assertParity(t, q)
	}
}

// TestMultiWordTypedLiteralParity — `TIMESTAMP WITH TIME ZONE '...'`.
// The lexer's TYPEDLIT fold requires the SCONST to follow the type name
// immediately, so multi-word datetime types cannot fold and need real
// productions. WITH/WITHOUT arrive as WITH_LA/WITHOUT_LA because base_yylex
// substitutes them when TIME follows.
func TestMultiWordTypedLiteralParity(t *testing.T) {
	for _, q := range []string{
		"SELECT TIMESTAMP WITH TIME ZONE '2010-04-01 10:00'",
		"SELECT TIMESTAMP WITHOUT TIME ZONE '2010-04-01 10:00'",
		"SELECT TIME WITH TIME ZONE '10:00'",
		"SELECT TIME WITHOUT TIME ZONE '10:00'",
		"INSERT INTO r VALUES ('101', TIMESTAMP WITH TIME ZONE '2010-04-01 10:00', 'Bob')",
		"SELECT * FROM r WHERE t < TIMESTAMP WITH TIME ZONE '2010-04-01 14:00'",
		// single-word fold and the cast position must stay identical
		"SELECT TIMESTAMP '2010-04-01 10:00'",
		"CREATE TABLE r (a timestamp with time zone NOT NULL)",
		"SELECT a::timestamp with time zone FROM t",
	} {
		assertParity(t, q)
	}
}

// TestMiscRoutedShapesParity — the remaining small forms the isolation specs
// use: DEFAULT on the right of an UPDATE SET, transaction modes WITHOUT
// commas (gram.y's transaction_mode_list makes the comma optional), an omitted
// CREATE INDEX name, and SET [LOCAL] ROLE.
func TestMiscRoutedShapesParity(t *testing.T) {
	for _, q := range []string{
		"UPDATE pktab SET data = DEFAULT",
		"UPDATE t SET a = DEFAULT, b = 1",
		"BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE",
		"BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY",
		"START TRANSACTION READ ONLY DEFERRABLE",
		"CREATE INDEX ON t (a)",
		"CREATE INDEX CONCURRENTLY ON cic_test (a)",
		"CREATE UNIQUE INDEX ON t (a)",
		"SET ROLE r1",
		"SET ROLE NONE",
		"SET LOCAL ROLE r1",
		"RESET ROLE",
		// guards
		"CREATE INDEX i ON t (a)",
		"UPDATE t SET a = 1",
		"BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY",
	} {
		assertParity(t, q)
	}
}
