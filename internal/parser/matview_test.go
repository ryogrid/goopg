package parser

import "testing"

// TestMatViewParity pins REFRESH / DROP MATERIALIZED VIEW (P5.1) against the
// legacy parser. DROP MATERIALIZED VIEW deliberately produces a DropCompatStmt
// with the two-word ObjType "materialized view", not a DropViewStmt — legacy
// takes that path at internal/parser/ddl.go:6329-6369, and canonDump
// distinguishes nil from empty for the unused ArgTypes/CastTypes fields.
func TestMatViewParity(t *testing.T) {
	for _, q := range []string{
		"REFRESH MATERIALIZED VIEW mv",
		"REFRESH MATERIALIZED VIEW s.mv",
		"REFRESH MATERIALIZED VIEW CONCURRENTLY mv",
		"REFRESH MATERIALIZED VIEW mv WITH DATA",
		"REFRESH MATERIALIZED VIEW mv WITH NO DATA",
		"REFRESH MATERIALIZED VIEW CONCURRENTLY s.mv WITH NO DATA",
		"DROP MATERIALIZED VIEW mv",
		"DROP MATERIALIZED VIEW IF EXISTS mv",
		"DROP MATERIALIZED VIEW mv1, mv2 CASCADE",
		"DROP MATERIALIZED VIEW IF EXISTS s.mv RESTRICT",
		// the CREATE side stays green alongside it
		"CREATE MATERIALIZED VIEW mv AS SELECT 1",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS s.mv (a, b) AS SELECT 1, 2 WITH NO DATA",
	} {
		assertParity(t, q)
	}
}
