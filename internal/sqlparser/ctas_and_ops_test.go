package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
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
		if _, err := parser.Parse(q); err != nil {
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
