package sqlparser

import "testing"

// TestExclusionConstraintParity — `EXCLUDE [USING method] (col WITH op, ...)
// [INCLUDE (...)] [WHERE (pred)] [constraint attrs]`.
//
// A table constraint kind that was not ported at all. Legacy emits a
// TableConstraintDef with IsExclusion set, routing anonymous ones to
// TableExclusions and named ones to NamedConstraints, keeping the columns as a
// list but only ONE ExclusionOp (the first), and defaulting Method to "btree"
// when USING is omitted.
func TestExclusionConstraintParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (c1 int, EXCLUDE USING btree (c1 WITH =))",
		"CREATE TABLE t (c1 int, EXCLUDE (c1 WITH =))",
		"CREATE TABLE t (c1 int, c3 int, EXCLUDE USING btree (c1 WITH =) INCLUDE (c3))",
		"CREATE TABLE t (c1 int, CONSTRAINT x EXCLUDE USING gist (c1 WITH &&))",
		"CREATE TABLE t (c1 int, EXCLUDE (c1 WITH =) WHERE (c1 > 0))",
		"CREATE TABLE t (a int, b int, EXCLUDE USING gist (a WITH =, b WITH &&))",
		"CREATE TABLE t (c1 int, EXCLUDE (c1 WITH =) DEFERRABLE INITIALLY DEFERRED)",
		// the other table constraints must stay identical
		"CREATE TABLE t (a int, UNIQUE (a))",
		"CREATE TABLE t (a int, CONSTRAINT u UNIQUE (a))",
		"CREATE TABLE t (a int, PRIMARY KEY (a))",
	} {
		assertParity(t, q)
	}
}
