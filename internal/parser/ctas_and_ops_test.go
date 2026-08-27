package parser

import (
	"testing"

)

// TestCreateTableAsShapes pins the two CTAS tails that were missing: an
// explicit COLUMN-ALIAS list and WITH [NO] DATA. The aliases carry no type, so
// the parenthesised opt_table_element_list rule cannot take them — they land
// in ColumnAliases, and `CREATE TABLE t (a int) AS SELECT` stays a legacy
// REJECT (a typed element list and AS SELECT are mutually exclusive).
func TestCreateTableAsShapes(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t AS SELECT 1",
		"CREATE TABLE t AS SELECT 1 WITH DATA",
		"CREATE TABLE t AS SELECT 1 WITH NO DATA",
		"CREATE TABLE IF NOT EXISTS t AS SELECT 1 WITH NO DATA",
		"CREATE TABLE t (a) AS SELECT generate_series(1,3) WITH DATA",
		"CREATE TABLE t (a, b) AS SELECT 1, 2",
		"CREATE TABLE t (a, b) AS SELECT 1, 2 WITH NO DATA",
		"CREATE TEMP TABLE t (a) AS SELECT 1",
		"CREATE TABLE s.t (a) AS SELECT 1",
		// The typed element list must not have been disturbed.
		"CREATE TABLE t (a int, b int)",
		"CREATE TABLE t (a int PRIMARY KEY)",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "CREATE TABLE t (a int) AS SELECT 1")
}

// TestResetSessionAuthorization pins gram.y's dedicated VariableResetStmt
// alternative. AUTHORIZATION is reserved, so `RESET ColId` cannot reach it;
// legacy normalises the pair to the GUC's real name.
func TestResetSessionAuthorization(t *testing.T) {
	for _, q := range []string{
		"RESET SESSION AUTHORIZATION",
		"RESET ALL",
		"RESET work_mem",
	} {
		assertParity(t, q)
	}
}

// TestPrefixOperator pins `~x`. Legacy's prefix set is exactly {-, +, NOT, ~}
// (internal/parser/select.go:3005-3031); '-', '+' and NOT arrive as their own
// terminals, so '~' is the only spelling that reaches the generic `Op a_expr`
// alternative. prefixOp rejects anything else rather than widening past
// legacy — a widening the differential harness cannot see.
func TestPrefixOperator(t *testing.T) {
	for _, q := range []string{
		"SELECT ~q1 FROM t",
		`SELECT q1 & q2 AS "and", q1 | q2 AS "or", q1 # q2 AS "xor", ~q1 AS "not" FROM int8_tbl`,
		"SELECT ~(a + b) FROM t",
		"SELECT a - 1 FROM t",
		"SELECT a || b FROM t",
	} {
		assertParity(t, q)
	}
}

// TestBExprSignedBounds pins BETWEEN's signed bounds. BETWEEN's operands are
// b_expr, which had no unary-sign alternative at all, so every signed bound
// was a hard 42601.
//
// Parity is asserted on the PARSE, not the AST: `-1e6` folds into the constant
// on the yacc side and stays a UnaryOp on the legacy side — the pre-existing,
// deliberate divergence that difftest_known_diffs.md rules "(b)-inverted" and
// TestKnownDiffUnaryMinusFold pins. b_expr inherits it from a_expr, which is
// consistent rather than new.
func TestBExprSignedBounds(t *testing.T) {
	for _, q := range []string{
		"SELECT f1 FROM t WHERE f1 BETWEEN -1e6 AND 1e6",
		"SELECT f1 FROM t WHERE f1 BETWEEN -1 AND 1",
		"SELECT f1 FROM t WHERE f1 BETWEEN +1 AND 2",
		"SELECT f1 FROM t WHERE f1 NOT BETWEEN -1e6 AND 1e6",
		"SELECT f1 FROM t WHERE f1 BETWEEN SYMMETRIC -1 AND 1",
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("legacy rejects %q: %v", q, err)
			continue
		}
		if _, _, err := diffParse(q); err != nil {
			t.Errorf("yacc rejects %q: %v", q, err)
		}
	}
}

// TestPartitionKeyElements pins PARTITION BY's full key element (gram.y
// part_elem): expression keys, operator classes and COLLATE, none of which
// colid_list could express. It also pins MethodPos and KeyColPos, which
// M0134-0016b errposition reporting reads and which were zero — or, once the
// element rule landed, one token early — until the positions started coming
// from $<p>N instead of lastConsumedPos().
func TestPartitionKeyElements(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE p (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE p (a int, b int) PARTITION BY RANGE (a, b)",
		"CREATE TABLE p (a int) PARTITION BY LIST (a)",
		"CREATE TABLE p (a int, b int) PARTITION BY RANGE (a, ((a+b)/2))",
		"CREATE TABLE p (a int) PARTITION BY RANGE (lower(a))",
		"CREATE TABLE p (a int, b text) PARTITION BY HASH (a part_test_int4_ops, b part_test_text_ops)",
		"CREATE TABLE pt (i int) PARTITION BY hash (i part_test_int4_ops_bad)",
		`CREATE TABLE p (a text) PARTITION BY RANGE (a COLLATE "C")`,
	} {
		assertParity(t, q)
	}
}

// TestCtasFromPreparedStatement pins `CREATE TABLE ... AS EXECUTE name
// [(params)]`. gram.y keeps it in a separate rule; goopg's AST carries it on
// CreateTableStmt.ExecuteSource, so one ctas_source nonterminal serves both the
// plain and the column-alias spellings instead of doubling them.
func TestCtasFromPreparedStatement(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t AS EXECUTE p",
		"CREATE TABLE t (a) AS EXECUTE p WITH DATA",
		"CREATE TABLE t AS EXECUTE p(1, 'x') WITH NO DATA",
		"CREATE TABLE selinto_schema.tbl_withdata3 (a) AS EXECUTE data_sel WITH DATA",
	} {
		assertParity(t, q)
	}
}

// TestCountedDatetimeTypes pins `time(2) with time zone` and friends. The
// precision sits BEFORE the tz mark, so col_type_name's trailing typmod suffix
// could not express them; castType now carries an inline typmod, and both cast
// spellings read it.
func TestCountedDatetimeTypes(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE timetz_tbl (f1 time(2) with time zone)",
		"CREATE TABLE tt (f1 timestamp(3) with time zone)",
		"CREATE TABLE tt (f1 timestamp(3) without time zone)",
		// Uncounted and trailing-typmod spellings must be unchanged.
		"CREATE TABLE tt (f1 time with time zone)",
		"CREATE TABLE tt (f1 time(2))",
		"CREATE TABLE tt (f1 timestamp(3))",
		"SELECT CAST(a AS timestamp(3)) FROM t",
		"SELECT a::timestamp(3) FROM t",
		"SELECT a::decimal(15,4) FROM t",
		"SELECT CAST(a AS decimal(15,4)) FROM t",
		"SELECT a::char FROM t",
	} {
		assertParity(t, q)
	}
}

// TestKeywordFunctionNames pins calls whose NAME is a type_func_name_keyword —
// gram.y's func_name is type_function_name, which admits them, while ColId does
// not, so `left('ahoj', 2)` was a hard 42601.
//
// Only the CALL form is routed here: legacy reads a BARE `left` as a column
// reference, so admitting the bare spelling would change its node rather than
// fix an error.
func TestKeywordFunctionNames(t *testing.T) {
	for _, q := range []string{
		"SELECT left('ahoj', 2), right('ahoj', 2)",
		"SELECT binary('x')",
		"SELECT collation('x')",
		// current_schema stays on the sql_value_func_name path.
		"SELECT current_schema()",
		"SELECT current_schema",
		// The JOIN keywords must still be JOIN keywords.
		"SELECT a FROM t LEFT JOIN u ON a = b",
		"SELECT a FROM t NATURAL JOIN u",
		"SELECT a FROM t CROSS JOIN u",
		"SELECT a FROM t FULL OUTER JOIN u ON a = b",
		"SELECT a IS NULL FROM t",
		"SELECT a LIKE 'x' FROM t",
		"SELECT a NOTNULL FROM t",
		"SELECT a ISNULL FROM t",
	} {
		assertParity(t, q)
	}
}

// TestSelectInto pins `SELECT ... INTO [TABLE] name`, which legacy turns into a
// CreateTableStmt with the query as SelectSource. Legacy takes ONLY that form —
// `SELECT a INTO TEMP x` is a syntax error there — so gram.y's TEMP / UNLOGGED
// / TABLESPACE variants stay out.
//
// The target is recorded against the simple_select's SelectStmt and the wrap
// happens one level up, at the SelectStmt rule, so the captured query already
// carries ORDER BY / LIMIT. It is keyed by POINTER because the INTO clause is
// parsed BEFORE the FROM list: an outer select's target is already recorded
// when an inner subquery reduces, and a single slot would misattribute it.
func TestSelectInto(t *testing.T) {
	for _, q := range []string{
		"SELECT * INTO sitmp1 FROM onek",
		"SELECT * INTO TABLE sitmp1 FROM onek WHERE onek.unique1 < 2",
		"SELECT a, b INTO s.t FROM onek ORDER BY a LIMIT 5",
		"SELECT * INTO t FROM (SELECT 1) x",
		// No-INTO selects must be untouched, nesting included.
		"SELECT * FROM (SELECT 1) x",
		"SELECT a FROM t UNION SELECT b FROM u",
		"SELECT 1",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "SELECT a INTO TEMP sitmp1 FROM onek")
}

// TestFetchFirstParenValue pins `FETCH FIRST (NULL+1) ROWS WITH TIES`.
// Upstream reaches a parenthesised value because gram.y's c_expr owns
// `'(' a_expr ')'`; this grammar hangs that alternative off a_expr instead, so
// select_fetch_first_value's c_expr could not start with '('.
func TestFetchFirstParenValue(t *testing.T) {
	for _, q := range []string{
		"SELECT a FROM t ORDER BY a FETCH FIRST (NULL+1) ROWS WITH TIES",
		"SELECT a FROM t ORDER BY a FETCH FIRST (2) ROWS ONLY",
		"SELECT a FROM t ORDER BY a FETCH FIRST NULL ROWS WITH TIES",
		"SELECT a FROM t ORDER BY a FETCH FIRST 2 ROWS WITH TIES",
	} {
		assertParity(t, q)
	}
}
