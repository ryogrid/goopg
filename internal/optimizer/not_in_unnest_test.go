package optimizer

import "testing"

// M0146-0002c: `x NOT IN (subquery)` is never unnested, as in PostgreSQL. It
// is `<> ALL`, an ALL_SUBLINK, and pull_up_sublinks_qual_recurse converts
// ANY_SUBLINK and EXISTS_SUBLINK only (prepjointree.c:665/731), so PG keeps it
// a `(hashed SubPlan)` filter on the scan. goopg's legacy unnest used to build
// a null-aware anti join instead (value-correct — M0146-0002b's NULL probe —
// but placed outside the join search, which cost TPC-H Q16 its plan).
//
// The SJInfo stamping tests for the anti arm (TestInUnnestSJInfoCorrelatedAnti,
// TestInUnnestSJInfoNonCorrelatedAntiNullAware) were removed with the route:
// no planner path builds that join any more, and the unreachable arm goes with
// the legacy-deletion slice.

// assertNotInStaysSubPlan plans sql and asserts the NOT IN survived as a
// sublink: no semi or anti join was built from it.
func assertNotInStaysSubPlan(t *testing.T, sql string) Node {
	t.Helper()
	node, err := Plan(parseOne(t, sql), twoTablesCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if findInExpr(node) == nil {
		t.Fatalf("NOT IN was unnested; PG keeps it a SubPlan:\n%s", planString(node))
	}
	if j := findFirstJoinByType(node, JoinTypeAnti); j != nil {
		t.Fatalf("NOT IN became an ANTI join; PG keeps it a SubPlan:\n%s", planString(node))
	}
	if j := findFirstJoinByType(node, JoinTypeSemi); j != nil {
		t.Fatalf("NOT IN became a SEMI join (its complement):\n%s", planString(node))
	}
	return node
}

func TestNonCorrelatedNotInStaysSubPlan(t *testing.T) {
	assertNotInStaysSubPlan(t, "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE z > 0)")
}
