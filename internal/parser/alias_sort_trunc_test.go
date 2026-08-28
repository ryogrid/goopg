package parser

import "testing"

// TestAliasColumnLists pins `FROM t [AS] alias (c1, c2)` — the column-alias
// form of gram.y's alias_clause. RangeVar.Columns already existed; only the
// productions were missing, so every regress case using the SQL-standard
// derived-column list (int2/int4/int8/select/limit) raised 42601.
func TestAliasColumnLists(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM int2_tbl AS f(a, b)",
		"SELECT * FROM int2_tbl f(a, b)",
		"SELECT * FROM int2_tbl AS f(a)",
		"SELECT * FROM s.int2_tbl AS f(a, b, c)",
		"SELECT * FROM (TABLE int2_tbl) AS s (a, b)",
		"SELECT * FROM a AS x(i), b AS y(j) WHERE x.i = y.j",
	} {
		assertParity(t, q)
	}
}

// TestSortByUsing pins ORDER BY x USING <op>, gram.y sortby's first
// alternative, including the NULLS tail that only that alternative and the
// ASC/DESC one share.
func TestSortByUsing(t *testing.T) {
	for _, q := range []string{
		"SELECT a FROM onek ORDER BY unique1 USING <",
		"SELECT a FROM onek ORDER BY unique1 USING >",
		"SELECT a FROM onek ORDER BY unique1 USING > NULLS LAST",
		"SELECT a FROM onek ORDER BY unique1 USING < NULLS FIRST",
		"SELECT a FROM onek ORDER BY a USING <, b USING >",
		"SELECT a FROM onek ORDER BY a + 1 USING <",
		"SELECT a FROM onek ORDER BY a USING int4gt",
		"SELECT a FROM onek ORDER BY a USING pg_catalog.int4lt",
		"SELECT a FROM onek ORDER BY a USING pg_catalog.int4gt NULLS FIRST",
	} {
		assertParity(t, q)
	}
}

// TestTruncateOnly pins TRUNCATE's per-relation ONLY flag. TRUNCATE's list is
// relation_expr_list, not a bare name list, so ONLY is legal on each entry
// independently and TruncateStmt.Only is parallel to .Names.
func TestTruncateOnly(t *testing.T) {
	for _, q := range []string{
		"TRUNCATE ONLY trunc_f",
		"TRUNCATE TABLE ONLY trunc_f",
		"TRUNCATE ONLY a, b",
		"TRUNCATE a, ONLY b",
		"TRUNCATE ONLY a, ONLY b CASCADE",
		"TRUNCATE ONLY s.a RESTART IDENTITY",
		"TRUNCATE a",
	} {
		assertParity(t, q)
	}
}

// TestFetchFirstWithTies pins the countless FETCH FIRST — `FETCH FIRST ROWS
// WITH TIES` defaults to one row, and only the counted form was ported.
func TestFetchFirstWithTies(t *testing.T) {
	for _, q := range []string{
		"SELECT a FROM onek ORDER BY a FETCH FIRST ROWS WITH TIES",
		"SELECT a FROM onek ORDER BY a FETCH FIRST ROW WITH TIES",
		"SELECT a FROM onek ORDER BY a FETCH NEXT ROWS WITH TIES",
		"SELECT a FROM onek ORDER BY a FETCH FIRST 2 ROWS WITH TIES",
		"SELECT a FROM onek ORDER BY a FETCH FIRST ROWS ONLY",
	} {
		assertParity(t, q)
	}
}
