package sqlparser

import "testing"

// TestTrimForms pins TRIM. The direction keyword picks the target function and
// trim_list REVERSES the operands — gram.y builds the argument list with
// lappend($3, $1), so the string to trim comes first and the trim characters
// after it — and legacy reproduces that ordering.
//
// The call is built by trimCall, not NewFuncCall: that ctor appends one
// Variadic flag per argument to match legacy's GENERAL function-call path, but
// legacy builds TRIM as a special form and leaves Variadic nil. canonDump
// distinguishes nil from []bool{false,false}, so this was a live parity diff
// even once every TRIM form parsed.
func TestTrimForms(t *testing.T) {
	for _, q := range []string{
		"SELECT TRIM(BOTH FROM f1) FROM t",
		"SELECT TRIM(LEADING FROM f1) FROM t",
		"SELECT TRIM(TRAILING FROM f1) FROM t",
		"SELECT TRIM(BOTH 'x' FROM f1) FROM t",
		"SELECT TRIM(LEADING 'x' FROM f1) FROM t",
		"SELECT TRIM(TRAILING 'x' FROM f1) FROM t",
		"SELECT TRIM('x' FROM f1) FROM t",
		"SELECT f1 FROM text_tbl UNION SELECT TRIM(TRAILING FROM f1) FROM char_tbl",
	} {
		assertParity(t, q)
	}
	// gram.y's third trim_list alternative (a bare expr_list) makes these legal
	// upstream. Legacy rejects both, so they are deliberately not ported —
	// porting them would widen the routed parser past the one it replaces, and
	// costs a conflict besides.
	assertBothReject(t, "SELECT TRIM(f1) FROM t")
	assertBothReject(t, "SELECT TRIM(f1, 'x') FROM t")
}

// TestInheritanceStar pins `person*`, gram.y relation_expr's inheritance-star
// form. It asks for descendants explicitly, which is already the default, so
// both parsers record nothing and RangeVar.Only stays false.
func TestInheritanceStar(t *testing.T) {
	for _, q := range []string{
		"SELECT p.name, p.age FROM person* p",
		"SELECT p.name FROM person*",
		"SELECT name FROM person* AS p",
		"SELECT p.name, p.age FROM person* p ORDER BY age USING >",
		// The neighbouring alias forms must be undisturbed.
		"SELECT * FROM a x, b y",
		"SELECT * FROM a AS x",
		"SELECT a * b FROM t",
	} {
		assertParity(t, q)
	}
}

// TestKeywordCastTargets pins the three type names that are keyword-tokenised
// and so never reached cast_ident's IDENT alternative. Measured against legacy:
// json, xml and path are the ONLY three it accepts in cast position that are
// not plain identifiers.
func TestKeywordCastTargets(t *testing.T) {
	for _, q := range []string{
		"SELECT NULL::json",
		"SELECT NULL::xml",
		"SELECT NULL::path",
		"SELECT satisfies_hash_partition('mchash'::regclass, 4, 0, NULL::int, NULL::text, NULL::json)",
		"SELECT NULL::jsonb, NULL::bytea, NULL::uuid",
	} {
		assertParity(t, q)
	}
}

// TestAlterTableAddPrimaryKeyInclude pins the INCLUDE tail on ADD PRIMARY KEY,
// which lands in AlterTableAction.IncludeColumns.
func TestAlterTableAddPrimaryKeyInclude(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE tbl_include_pk ADD PRIMARY KEY (c1, c2) INCLUDE (c3, c4)",
		"ALTER TABLE t ADD PRIMARY KEY (c1) INCLUDE (c2)",
		"ALTER TABLE t ADD PRIMARY KEY (c1, c2)",
	} {
		assertParity(t, q)
	}
}

// TestSubpartitioning pins a partition that is itself partitioned. gram.y hangs
// OptPartitionSpec off the same CreateStmt as the bound.
func TestSubpartitioning(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE p2 PARTITION OF pt FOR VALUES FROM (12) TO (20) PARTITION BY RANGE (c)",
		"CREATE TABLE p2 PARTITION OF pt FOR VALUES IN (1, 2) PARTITION BY LIST (b)",
		"CREATE TABLE p2 PARTITION OF pt FOR VALUES FROM (12) TO (20) PARTITION BY HASH (c part_test_int4_ops)",
		// Bounds without a subpartition spec must be unchanged.
		"CREATE TABLE p2 PARTITION OF pt FOR VALUES FROM (12) TO (20)",
		"CREATE TABLE p2 PARTITION OF pt DEFAULT",
		"CREATE TABLE p2 PARTITION OF pt FOR VALUES WITH (MODULUS 4, REMAINDER 0)",
	} {
		assertParity(t, q)
	}
}
