package parser

import "testing"

// TestSetConstraints pins `SET CONSTRAINTS { ALL | name [, ...] }
// { DEFERRED | IMMEDIATE }`. This one missing statement form accounted for
// eight testport failures on its own (TestPort_SetConstraints*Deferral and
// the TestPort_PartitionedDeferrable* family), all of which reported
// `syntax error at or near "all"` from their setup.
func TestSetConstraints(t *testing.T) {
	for _, q := range []string{
		"SET CONSTRAINTS ALL DEFERRED",
		"SET CONSTRAINTS ALL IMMEDIATE",
		"SET CONSTRAINTS a DEFERRED",
		"SET CONSTRAINTS a, b DEFERRED",
		"SET CONSTRAINTS a, b, c IMMEDIATE",
		// A dotted name keeps only its LAST component — legacy's
		// parseQualifiedConstraintName overwrites instead of appending.
		"SET CONSTRAINTS s.a IMMEDIATE",
		"SET CONSTRAINTS s.a, t.b DEFERRED",
	} {
		assertParity(t, q)
	}
}
