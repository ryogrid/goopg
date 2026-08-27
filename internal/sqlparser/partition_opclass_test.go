package sqlparser

import "testing"

// TestHashPartitionBoundParity — `FOR VALUES WITH (modulus m, remainder r)`,
// the third mutually exclusive PARTITION OF bound alongside IN and FROM/TO.
// PartitionOfClause has carried Modulus/Remainder/IsHash all along.
//
// MODULUS and REMAINDER are not kwlist keywords in this build, so they arrive
// as plain IDENTs and the action validates the spelling; IsHash is an explicit
// flag rather than inferred from the values, since `modulus 0, remainder 0` is
// a legitimate bound.
func TestHashPartitionBoundParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE p1 PARTITION OF pt FOR VALUES WITH (modulus 4, remainder 0)",
		"CREATE TABLE p1 PARTITION OF pt FOR VALUES WITH (MODULUS 8, REMAINDER 3)",
		// the other bound shapes must stay identical
		"CREATE TABLE c PARTITION OF p FOR VALUES IN (1, 2)",
		"CREATE TABLE c PARTITION OF p FOR VALUES FROM (1) TO (10)",
		"CREATE TABLE c PARTITION OF p DEFAULT",
	} {
		assertParity(t, q)
	}
}

// TestOpClassWithOptionsParity — `int4_ops(foo=1)`. The option list is accepted
// and discarded; legacy records only that the opclass HAD options, as the
// statement-level OpClassWithOptions naming the FIRST such opclass.
func TestOpClassWithOptionsParity(t *testing.T) {
	for _, q := range []string{
		"CREATE INDEX ON t (id int4_ops(foo=1))",
		"CREATE INDEX ON t (id int4_ops)",
		"CREATE INDEX ON t (id)",
		"CREATE INDEX i ON t (a text_pattern_ops, b int4_ops(x=1))",
	} {
		assertParity(t, q)
	}
}
