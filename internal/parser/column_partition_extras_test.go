package parser

import "testing"

// TestAttachPartitionBounds pins ATTACH PARTITION over the SAME bound spec as
// CREATE TABLE ... PARTITION OF. Three hand-spelled copies had left the hash
// bound unreachable here — 78 fragments in the full regress corpus.
func TestAttachPartitionBounds(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE hash_parted ATTACH PARTITION fail_part FOR VALUES WITH (MODULUS 8, REMAINDER 4)",
		"ALTER TABLE p ATTACH PARTITION c FOR VALUES IN (1)",
		"ALTER TABLE p ATTACH PARTITION c FOR VALUES FROM (1) TO (2)",
		"ALTER TABLE p ATTACH PARTITION c DEFAULT",
	} {
		assertParity(t, q)
	}
}

// TestPartitionOfElementList pins `CREATE TABLE c PARTITION OF p ( ... )`.
// Legacy keeps the elements on the PartitionOfClause, not as columns: a NAMED
// check becomes CheckConstraints with its expression stored as a TOKEN JOIN
// (lower-cased, string literals unquoted — `upper ( a ) = X`), an anonymous
// check is accepted and dropped, and NOT NULL / UNIQUE / DEFAULT / GENERATED
// become per-column overrides.
func TestPartitionOfElementList(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE q1 PARTITION OF q ( CONSTRAINT check_1 CHECK (a IS NOT NULL AND a = 1) ) FOR VALUES IN ('b')",
		"CREATE TABLE q1 PARTITION OF q ( CONSTRAINT check_1 CHECK (A IS NOT NULL AND Upper(a) = 'X') ) DEFAULT",
		"CREATE TABLE q1 PARTITION OF q (a NOT NULL, b DEFAULT 1) FOR VALUES IN ('b')",
		"CREATE TABLE q1 PARTITION OF q (a WITH OPTIONS UNIQUE, CHECK (a > 0)) FOR VALUES IN ('b')",
		"CREATE TABLE p2 PARTITION OF p (d WITH OPTIONS GENERATED ALWAYS AS (a + b) STORED) FOR VALUES FROM (1) TO (2)",
		"CREATE TABLE p2 PARTITION OF p (a NOT NULL) FOR VALUES FROM (1) TO (2) PARTITION BY LIST (b)",
		"CREATE TABLE p2 PARTITION OF p FOR VALUES FROM (1) TO (2)",
	} {
		assertParity(t, q)
	}
}

// TestColumnQualifierExtras pins the column qualifiers legacy accepts that the
// col_constraint loop lacked: COLLATE, COMPRESSION, a `CONSTRAINT name`
// prefix (kept only for NOT NULL / UNIQUE / CHECK — legacy DROPS it on
// PRIMARY KEY and REFERENCES, and drops a named DEFAULT entirely), NOT NULL
// NO INHERIT and CHECK ... NO INHERIT. Both CREATE TABLE and ALTER TABLE ADD
// COLUMN go through copyColConstraints so the siblings cannot drift.
func TestColumnQualifierExtras(t *testing.T) {
	for _, q := range []string{
		`CREATE TABLE parent (a float8, c text collate "C")`,
		"CREATE TABLE cmdata(f1 text COMPRESSION pglz)",
		"CREATE TABLE t (a text COMPRESSION lz4 NOT NULL)",
		"CREATE TABLE t (b int CONSTRAINT nn NOT NULL, c int CONSTRAINT u UNIQUE, d int CONSTRAINT pk PRIMARY KEY, e int CONSTRAINT df DEFAULT 1)",
		"CREATE TABLE t (a int CONSTRAINT con1 CHECK (a > 0), b int, c int)",
		"CREATE TABLE t (e int CONSTRAINT ck CHECK (e > 0) NO INHERIT)",
		"CREATE TABLE t (e int CHECK (e > 0) NO INHERIT)",
		"CREATE TABLE t (e int CONSTRAINT fk REFERENCES u (x))",
		"CREATE TABLE part_fail (a int NOT NULL NO INHERIT, b int)",
		// DEFAULT's expression stays greedy: this COLLATE is part of the default.
		`CREATE TABLE t (c text DEFAULT 'x' COLLATE "C")`,
		`ALTER TABLE t ADD COLUMN c text COLLATE "C" CONSTRAINT nn NOT NULL`,
		"ALTER TABLE t ADD COLUMN c int NOT NULL NO INHERIT",
	} {
		assertParity(t, q)
	}
}

// TestTableCheckNoInherit pins table-level CHECK ... NO INHERIT (named and
// anonymous) and the one NOT VALID spelling legacy accepts on CREATE TABLE —
// only BEHIND NO INHERIT, and dropped. One check_body rule serves every
// spelling: two alternatives sharing the prefix with their own mid-rule
// markSpanStart() are two distinct empty nonterminals reducible at the same
// point, which is what produced 1329 reduce/reduce on the first cut.
func TestTableCheckNoInherit(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE nv_parent (d date, check (false) no inherit)",
		"CREATE TABLE nv_parent (d date, check (false) no inherit not valid)",
		"CREATE TABLE t (a int, CONSTRAINT c CHECK (a > 0) NO INHERIT)",
		"CREATE TABLE t (a int, CHECK (a > 0))",
		"CREATE TABLE t (a int, CONSTRAINT c CHECK (a > 0))",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "CREATE TABLE t (a int, CHECK (a > 0) NOT VALID)")
}

// TestCreateTableTrailers pins the trailing clauses: TABLESPACE (kept, on
// both CREATE TABLE and CREATE INDEX), USING <access method> and WITHOUT OIDS
// (parsed and dropped by legacy — no AST field), and the typed-table form
// `CREATE TABLE t OF type [( col WITH OPTIONS ... )]`.
func TestCreateTableTrailers(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE testschema.foo (i int) TABLESPACE regress_tblspace",
		"CREATE TABLE t (a int) WITH (fillfactor = 10) TABLESPACE ts",
		"CREATE INDEX foo_idx on foo(i) TABLESPACE regress_tblspace",
		"CREATE INDEX foo_idx on foo(i) TABLESPACE ts WHERE i > 0",
		"CREATE TABLE tableam_tbl_heap2(f1 int) USING heap2",
		"CREATE TABLE t (a int) WITHOUT OIDS",
		"CREATE TABLE test_tbl2 OF test_type2",
		"CREATE TABLE t OF ty (a WITH OPTIONS NOT NULL, b WITH OPTIONS DEFAULT 1)",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "CREATE TABLE t (a int) WITH OIDS")
	// `(a int) AS SELECT` was a dead opt_ct_tail alternative; legacy rejects it.
	assertBothReject(t, "CREATE TABLE t (a int) AS SELECT 1")
}
