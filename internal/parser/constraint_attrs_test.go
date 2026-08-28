package parser

import "testing"

// TestColumnConstraintAttrParity — `[NOT] DEFERRABLE` / `INITIALLY DEFERRED |
// IMMEDIATE` and `UNIQUE NULLS NOT DISTINCT` on a COLUMN constraint.
//
// Ported the way gram.y does it: ConstraintAttr is a SIBLING alternative of the
// col_constraint loop, not a trailer on each constraint element. That is what
// keeps `NOT NULL` and `NOT DEFERRABLE` separable with one token of lookahead —
// after shifting NOT, NULL_P vs DEFERRABLE picks the arm — so the conflict pin
// does not move. The attrs then attach to whichever constraint the column
// actually declared, as legacy does by threading pointers to the specific flag
// pair. INITIALLY DEFERRED implies DEFERRABLE.
func TestColumnConstraintAttrParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (i integer UNIQUE DEFERRABLE, x text)",
		"CREATE TABLE t (i integer UNIQUE DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TABLE t (i integer UNIQUE INITIALLY DEFERRED)",
		"CREATE TABLE t (i integer UNIQUE DEFERRABLE INITIALLY IMMEDIATE)",
		"CREATE TABLE t (i integer UNIQUE NOT DEFERRABLE)",
		"CREATE TABLE t (i integer UNIQUE NULLS NOT DISTINCT)",
		"CREATE TABLE t (i integer PRIMARY KEY DEFERRABLE)",
		"CREATE TABLE t (i integer PRIMARY KEY DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TABLE t (a int REFERENCES o (id) DEFERRABLE)",
		"CREATE TABLE t (a int REFERENCES o (id) DEFERRABLE INITIALLY DEFERRED)",
		// the NOT NULL sibling must stay unambiguous
		"CREATE TABLE t (i integer NOT NULL UNIQUE)",
		"CREATE TABLE t (i integer NOT NULL)",
		"CREATE TABLE t (i integer NOT NULL DEFAULT 0)",
	} {
		assertParity(t, q)
	}
}

// TestTableConstraintAttrParity — the table-level trailers: INCLUDE (cols),
// NULLS NOT DISTINCT, and the ConstraintAttributeSpec. Every AST field has
// existed since DU-002 (TableUniqueIncludes, TableUniqueDeferrable,
// PrimaryKeyInclude, TableConstraintDef.NullsNotDistinct, …); only the grammar
// was missing.
func TestTableConstraintAttrParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (a int, UNIQUE NULLS NOT DISTINCT (a))",
		"CREATE TABLE t (a int, b int, UNIQUE (a) INCLUDE (b))",
		"CREATE TABLE t (a int, b int, PRIMARY KEY (a) INCLUDE (b))",
		"CREATE TABLE t (a int, UNIQUE (a) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TABLE t (a int, PRIMARY KEY (a) DEFERRABLE)",
		"CREATE TABLE t (a int, CONSTRAINT u UNIQUE (a) DEFERRABLE)",
		"CREATE TABLE t (a int, CONSTRAINT u UNIQUE NULLS NOT DISTINCT (a))",
		"CREATE TABLE t (a int, b int, CONSTRAINT u UNIQUE (a) INCLUDE (b))",
		"CREATE TABLE t (a int, CONSTRAINT pk PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TABLE t (a int, b int, FOREIGN KEY (b) REFERENCES o (id) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TABLE t (a int, b int, CONSTRAINT fk FOREIGN KEY (b) REFERENCES o (id) DEFERRABLE)",
		// the untrailered forms must stay identical
		"CREATE TABLE t (a int, UNIQUE (a))",
		"CREATE TABLE t (a int, PRIMARY KEY (a))",
		"CREATE TABLE t (a int, CONSTRAINT u UNIQUE (a))",
	} {
		assertParity(t, q)
	}
}

// TestIndexAndSetTransactionParity — CREATE/DROP INDEX CONCURRENTLY,
// CREATE INDEX ... INCLUDE (...), and SET [LOCAL] TRANSACTION <modes>.
// All three were missing from the grammar and are used by the upstream
// isolation specs.
func TestIndexAndSetTransactionParity(t *testing.T) {
	for _, q := range []string{
		"CREATE UNIQUE INDEX i ON t (a) INCLUDE (b)",
		"CREATE INDEX i ON t (a) INCLUDE (b, c)",
		"CREATE INDEX CONCURRENTLY i ON t (a)",
		"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)",
		"DROP INDEX CONCURRENTLY i",
		"DROP INDEX CONCURRENTLY IF EXISTS i",
		"CREATE INDEX i ON t (a)",
		"CREATE INDEX IF NOT EXISTS i ON t USING btree (a)",
		"DROP INDEX i",
		"DROP INDEX IF EXISTS a, b CASCADE",
		"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED",
		"SET TRANSACTION READ ONLY",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED, READ ONLY",
		"SET LOCAL TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		// plain SET must stay on its own rule
		"SET x = 1",
		"SET search_path TO a, b",
	} {
		assertParity(t, q)
	}
}
