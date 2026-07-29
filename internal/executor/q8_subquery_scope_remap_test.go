package executor

// M0125-0012 (TPC-DS Q8): a FROM-subquery's own Project target must keep
// its subquery-scope index when the OUTER join tree is reordered.
//
// Q8's `V1` derived table is `(select ca_zip from (<A> intersect <B>) A2)`.
// Its Project sits directly above a 1-column SetOp, so its only target is
// correctly `ca_zip/0`. The outer query joins V1 to a three-table
// store_sales/date_dim/store MHJ, and the MHJ reorder pass
// (`remapWithBindings` -> `applyJoinTreePosMap`) used to DESCEND into that
// Project and rewrite its target with the OUTER bindings' position map.
// Index 0 falls inside the outer FROM-order binding that also starts at 0
// (`store_sales`), so the target was rewritten to that table's reordered
// MHJ offset — 57 at SF=1. Execution then read column 57 out of the
// SetOp's 1-wide MaterializedSlot and the statement died with
//
//	ERROR: column ref ca_zip/57 out of MaterializedSlot range 1
//
// (contained, not a crash — `9740fce9` had already turned the raw panic
// into an ExecError). The fix stops `applyJoinTreePosMap` at every
// join-tree Project, matching what `buildBindingsPosMap`'s `collect`
// walker has always done on the build side.
//
// ACCEPTANCE NOTE — real Q8 returns 0 rows on PostgreSQL at SF=1, so "0
// rows, no error" is NOT a discriminator: any bug that empties the result
// also "matches PG". This fixture is therefore built to return a NON-EMPTY
// answer, and the expected values below were verified by running the exact
// same DDL + query on PostgreSQL 18.3 (`postgres/local_install`), which
// returns `alpha|5` and `beta|7`. The probe FAILS on pre-fix HEAD with the
// MaterializedSlot error above (verified against a throwaway goopg server
// before the bushy.go change landed).

import (
	"strings"
	"testing"
)

// newQ8ScopeFixture builds Q8's shape at doll-house scale: a three-table
// join that the planner packs into a MultiHashJoin, cross-joined to a
// FROM-subquery whose body is an INTERSECT, with the correlating predicate
// (`substr(s_zip,1,2) = substr(V1.ca_zip,1,2)`) left as the cross join's
// residual Filter. The table widths (3,3,3,2) are what make the defect
// observable: the subquery target's index 0 must land inside the outer
// binding that starts at 0 for the outer posMap to capture it.
func newQ8ScopeFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE q8_dd (d_date_sk int, d_qoy int, d_year int)",
		"CREATE TABLE q8_st (s_store_sk int, s_store_name text, s_zip text)",
		"CREATE TABLE q8_ss (ss_sold_date_sk int, ss_store_sk int, ss_net_profit int)",
		"CREATE TABLE q8_ca (ca_address_sk int, ca_zip text)",
		"INSERT INTO q8_dd VALUES (1, 2, 1998)",
		"INSERT INTO q8_dd VALUES (2, 1, 1998)",
		"INSERT INTO q8_st VALUES (10, 'alpha', '47602')",
		"INSERT INTO q8_st VALUES (11, 'beta', '16704')",
		"INSERT INTO q8_ss VALUES (1, 10, 5)",
		"INSERT INTO q8_ss VALUES (1, 11, 7)",
		// sold on d_date_sk 2, which fails d_qoy = 2: excluded, so the
		// residual Filter has to actually reject something.
		"INSERT INTO q8_ss VALUES (2, 10, 9)",
		"INSERT INTO q8_ca VALUES (1, '47602')",
		"INSERT INTO q8_ca VALUES (2, '16704')",
		// present in the INTERSECT's right arm only: must not survive.
		"INSERT INTO q8_ca VALUES (3, '99999')",
	}
	for _, stmt := range stmts {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// q8ScopeQuery mirrors query8.sql's structure exactly: the IN-list arm
// INTERSECTed with an unrestricted arm, wrapped in a second derived table
// (`A2`) and then in `V1`, joined to the fact/dimension tables with the
// zip-prefix predicate that PG evaluates over the joined row.
const q8ScopeQuery = `select s_store_name, sum(ss_net_profit)
from q8_ss, q8_dd, q8_st,
     (select ca_zip
        from (select ca_zip from q8_ca where ca_zip in ('47602','16704')
              intersect
              select ca_zip from q8_ca) A2) V1
where ss_store_sk = s_store_sk
  and ss_sold_date_sk = d_date_sk
  and d_qoy = 2 and d_year = 1998
  and (substr(s_zip,1,2) = substr(V1.ca_zip,1,2))
group by s_store_name
order by s_store_name`

// TestQ8SubqueryScopeIndexSurvivesJoinReorder is the value discriminator.
// It asserts the exact non-empty result PostgreSQL 18.3 produces for this
// fixture, so neither the MaterializedSlot error nor a silent empty result
// can pass.
func TestQ8SubqueryScopeIndexSurvivesJoinReorder(t *testing.T) {
	ctx, cleanup := newQ8ScopeFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx, q8ScopeQuery)
	if err != nil {
		// The pre-fix signature; name it so a regression is diagnosed
		// from the failure message alone.
		if strings.Contains(err.Error(), "MaterializedSlot range") {
			t.Fatalf("M0125-0012 regression — the outer posMap reached into the "+
				"FROM-subquery's Project scope again: %v", err)
		}
		t.Fatalf("query: %v", err)
	}
	got := renderRows(rows)
	want := []string{"alpha|5", "beta|7"} // verified on PostgreSQL 18.3
	if len(got) != len(want) {
		t.Fatalf("row count: got %d %v, want %d %v (PG 18.3 reference)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q, want %q (PG 18.3 reference)", i, got[i], want[i])
		}
	}
}

// TestQ8SubqueryScopeDefectiveShapeStillReachable guards the guard: the
// value assertion above only discriminates while the planner still
// produces the shape that carried the defect (a reordering MHJ beside a
// SetOp-rooted derived table under a cross join). If a later planner
// change stops producing it, this reports that the probe has gone
// vacuous rather than letting it pass for the wrong reason.
func TestQ8SubqueryScopeDefectiveShapeStillReachable(t *testing.T) {
	ctx, cleanup := newQ8ScopeFixture(t)
	defer cleanup()

	plan := nliResidualExplain(t, ctx, q8ScopeQuery)
	for _, want := range []string{"Multi-Way Hash Join", "Nested Loop", "SetOp"} {
		if !strings.Contains(plan, want) {
			t.Skipf("Q8's defective shape is no longer produced (missing %q); "+
				"TestQ8SubqueryScopeIndexSurvivesJoinReorder no longer discriminates "+
				"the M0125-0012 defect. Plan:\n%s", want, plan)
		}
	}
}
