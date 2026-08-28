package parser

import "testing"

// Pins for the batch ranked from the full regress corpus (36k fragments):
// legacy-accepts / yacc-rejects went 625 -> 299 across these forms.

// TestSqlStandardFunctionForms — SUBSTRING / OVERLAY / POSITION keyword
// spellings, which legacy rewrites into ordinary calls built as SPECIAL FORMS
// (position's operands reversed, Variadic nil). The comma spellings are the
// same special form there, so the keyword rules take them too.
func TestSqlStandardFunctionForms(t *testing.T) {
	for _, q := range []string{
		"SELECT SUBSTRING(b FROM 2 FOR 4)",
		"SELECT SUBSTRING(b FROM 6)",
		"SELECT SUBSTRING(b, 1, 2)",
		"SELECT substring(b, 1)",
		"SELECT OVERLAY('abcdef' PLACING '45' FROM 4)",
		"SELECT OVERLAY('abcdef' PLACING '45' FROM 4 FOR 2)",
		"SELECT POSITION('b' IN 'abc')",
		"SELECT position('b', 'abc')",
		"SELECT POSITION(B'111010110' IN B'0111010110')",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "SELECT SUBSTRING(b FOR 3)")
}

// TestBitStringLiterals — B'...' and X'...' become plain StringConsts, a hex
// body expanded to four bits per digit (legacy decodeBitStringLit).
func TestBitStringLiterals(t *testing.T) {
	for _, q := range []string{
		"SELECT B'1010'",
		"SELECT X'FF'",
		"SELECT x'20000' | x'40000'",
		"SELECT lo_open(loid, CAST(x'20000' | x'40000' AS integer))",
	} {
		assertParity(t, q)
	}
}

// TestArrayAndCallForms — nested ARRAY[[..],[..]] (inner brackets without the
// keyword), an EMPTY ARRAY[] carrying a nil element list, DISTINCT + ORDER BY
// inside a call, and `name := value` / `name => value` arguments in both
// expression-position and FROM-clause calls (legacy drops the name).
func TestArrayAndCallForms(t *testing.T) {
	for _, q := range []string{
		"SELECT array[[1,2],[3,4]]",
		"SELECT array[[[1]]]",
		"SELECT array[1,2]",
		"SELECT array[]::int[]",
		"SELECT array_agg(distinct a order by a) FROM t",
		"SELECT aggfstr(distinct a,b,c order by b) FROM t",
		"SELECT count(distinct a) FROM t",
		"SELECT array_agg(a order by a) FROM t",
		"SELECT * FROM dfunc(a := 10, b := 20)",
		"SELECT * FROM dfunc(a => 10)",
		"SELECT dfunc(a := 10)",
	} {
		assertParity(t, q)
	}
}

// TestCreateTableComposableTail — the trailing clauses compose in any order
// legacy takes (`PARTITION BY ... WITH (...)` is written 65 times in the
// corpus), a PARTITION OF item may carry an element list, USING <am> is a
// tail item, and reloption names may be dotted.
func TestCreateTableComposableTail(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE p (a int) PARTITION BY LIST (a) WITH (fillfactor=100)",
		"CREATE TABLE p2 PARTITION OF p FOR VALUES IN (1) PARTITION BY LIST (b)",
		"CREATE TABLE p2 PARTITION OF p FOR VALUES IN (1) WITH (fillfactor=10)",
		"CREATE TABLE q1 PARTITION OF q (a NOT NULL, b DEFAULT 1) FOR VALUES IN ('b')",
		"CREATE TABLE t (a int) INHERITS (p) WITH (fillfactor=10)",
		"CREATE TABLE t (a int) USING heap2 WITH (fillfactor=10)",
		"ALTER TABLE t SET (toast.autovacuum_enabled = off)",
		"ALTER TABLE t SET (fillfactor = 10)",
		"CREATE TABLE tas WITH (fillfactor = 10) AS SELECT 1 a",
		"CREATE TABLE tas AS SELECT 1",
		"CREATE TABLE t (a, b) AS SELECT 1, 2",
	} {
		assertParity(t, q)
	}
	// Legacy's own ordering limits, kept.
	// KNOWN WIDENING, pinned rather than fixed. Upstream's CreateStmt fixes
	// the tail ORDER — `')' OptInherit OptPartitionSpec
	// table_access_method_clause OptWith` (gram.y:3633) — so PARTITION BY
	// must precede WITH and PG rejects this spelling. goopg's ct_tail_list is
	// deliberately order-free (it has to accept a repeated WITH, and CREATE
	// INDEX's tail is order-free in ddl.go too), so the grammar accepts it.
	// Accepting MORE than upstream is the lax direction; the golden pins the
	// shape so it cannot drift further. Ledger: P7.2 known widening.
	assertParity(t, "CREATE TABLE p (a int) WITH (fillfactor=100) PARTITION BY LIST (a)")
	// PG ACCEPTS this: create_as_target (gram.y:4838) is
	// `qualified_name opt_column_list table_access_method_clause OptWith …`,
	// so USING precedes AS. Legacy rejected it.
	assertParity(t, "CREATE TABLE tas USING heap2 AS SELECT 1")
}

// TestViewOptionsAndMatviewAm — CREATE VIEW's pre-AS WITH (...): security_
// barrier / security_invoker become *bool exactly as legacy's
// parseViewOptions decides them; CREATE MATERIALIZED VIEW ... USING <am> is
// parsed and dropped.
func TestViewOptionsAndMatviewAm(t *testing.T) {
	for _, q := range []string{
		"CREATE VIEW v WITH (security_barrier) AS SELECT 1",
		"CREATE VIEW v WITH (security_barrier = false, security_invoker = on) AS SELECT 1",
		"CREATE VIEW v AS SELECT 1",
		"CREATE MATERIALIZED VIEW mv USING heap2 AS SELECT 1",
		"CREATE MATERIALIZED VIEW mv AS SELECT 1 WITH NO DATA",
	} {
		assertParity(t, q)
	}
}

// TestInsertAliasAndConflictWhere — `INSERT INTO t AS alias`, with legacy's
// quirk that a column list after the alias lands on the RangeVar, and the
// arbiter's WHERE predicate.
func TestInsertAliasAndConflictWhere(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO inhpar AS i VALUES (3) ON CONFLICT (f1) DO UPDATE SET f2 = i.f2",
		"INSERT INTO t AS i (a) VALUES (1)",
		"INSERT INTO t AS i SELECT 1",
		"INSERT INTO t VALUES (1) ON CONFLICT (key) WHERE fruit LIKE '%b' DO UPDATE SET fruit = 1",
		"INSERT INTO t VALUES (1) ON CONFLICT (key) DO NOTHING",
	} {
		assertParity(t, q)
	}
}

// TestSetLocalSessionAuthorization — LOCAL scope plus the SESSION that is part
// of the GUC's own spelling.
func TestSetLocalSessionAuthorization(t *testing.T) {
	for _, q := range []string{
		"SET LOCAL SESSION AUTHORIZATION regress_seq_user",
		"SET SESSION AUTHORIZATION x",
		"SET LOCAL x = 1",
	} {
		assertParity(t, q)
	}
}

// TestLegacyConstraintQuirks — two legacy readings reproduced bug for bug
// because the AST is the migration's contract: `UNIQUE (c WITHOUT OVERLAPS)`
// records the two keywords as two more column names, and a PARENTHESISED
// exclusion element records every identifier inside as a column and the first
// operator token as the element's operator.
func TestLegacyConstraintQuirks(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (valid_at daterange, CONSTRAINT u UNIQUE (valid_at WITHOUT OVERLAPS))",
		"CREATE TABLE t (a int, UNIQUE (a, b))",
		"CREATE TABLE c (c1 circle, EXCLUDE USING gist (c1 WITH &&, (c2::circle) WITH &&) WHERE (c1 <> '(0,0)'))",
		"CREATE TABLE c (c1 circle, EXCLUDE USING gist ((c2::circle) WITH &&))",
	} {
		assertParity(t, q)
	}
}

// TestQuantifiedLikeAndNamedWindows — LIKE / ILIKE / NOT LIKE quantified over
// a list fold into the IN shape with the pattern operator as AnyOp, and an
// existing window name may carry its own frame or ORDER BY inside OVER (...).
func TestQuantifiedLikeAndNamedWindows(t *testing.T) {
	for _, q := range []string{
		"SELECT 'foo' LIKE ANY (array['%a'])",
		"SELECT 'foo' NOT LIKE ANY (array['%a'])",
		"SELECT 'foo' ILIKE ANY (array['%a'])",
		"SELECT 'foo' LIKE ALL (array['%a'])",
		"SELECT sum(a) OVER (w RANGE BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) FROM t WINDOW w AS (ORDER BY a)",
		"SELECT sum(a) OVER (w ORDER BY b) FROM t WINDOW w AS (PARTITION BY c)",
		"SELECT sum(a) OVER w FROM t WINDOW w AS (ORDER BY a)",
		"SELECT sum(a) OVER (PARTITION BY b) FROM t",
	} {
		assertParity(t, q)
	}
}
