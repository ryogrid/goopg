package executor

import (
	"fmt"
	"testing"
)

// M0125-0008 — `EXISTS (…) AND NOT EXISTS (…)` over ONE outer relation
// returned a NON-SUBSET result: adding a conjunct GREW the answer.
//
// Root cause (internal/planner/plan.go, Join.Output): a Semi/Anti join
// emits the OUTER (Left) row only, and every construction site sets its
// cached `schema` to a *copy* of Left.Output(). `rewriteMultiWayChain`
// then re-sorts the subtree below the pinned semi/anti spine IN PLACE
// (>= 3 base tables ⇒ a MultiHashJoin whose columns are OID-sorted),
// which turns that copy into a stale PERMUTATION of the real layout.
// `reresolveJoinByName` re-resolves the ancestor join's keys by NAME
// against `Left.Output()` — which for a Semi/Anti child was the phantom
// layout — so the upper join's key landed on the wrong column, matched
// nothing, and its conjunct silently stopped filtering.
//
// The shape is TPC-DS Q16 / Q94. This fixture is the same shape at four
// rows so it runs in-process:
//
//	ord 1 — two warehouses (EXISTS true), absent from r (NOT EXISTS true)  → KEEP
//	ord 2 — two warehouses (EXISTS true), present in r                     → drop
//	ord 3 — one warehouse  (EXISTS false), absent from r                   → drop
//	ord 4 — one warehouse  (EXISTS false), present in r                    → drop
//
// Every expected value below was taken from PostgreSQL 18.3 on the same
// data (oracle cluster :65438), not from goopg.
func TestSemiAntiConjunctionOverOneOuterRel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
//
// M0127-P6.2 note: the MultiHashJoin node named below was deleted, so this
// shape now plans as the left-deep binary hash cascade PG builds. The test is
// kept unchanged — its assertions are about the RESULT, so it now guards the
// cascade against the same defect the packed node once carried.

	for _, ddl := range []string{
		"CREATE TABLE zz_sa_o (ord int, wh int, dsk int, ask int, ssk int, cost int, profit int)",
		"CREATE TABLE zz_sa_d (dsk int, dt int)",
		"CREATE TABLE zz_sa_ca (ask int, st text)",
		"CREATE TABLE zz_sa_ws (ssk int, comp text)",
		"CREATE TABLE zz_sa_r (ord int)",
		"INSERT INTO zz_sa_d VALUES (1,10),(2,20)",
		"INSERT INTO zz_sa_ca VALUES (1,'IL'),(2,'CA')",
		"INSERT INTO zz_sa_ws VALUES (1,'pri'),(2,'other')",
		"INSERT INTO zz_sa_o VALUES (1,1,1,1,1,10,5),(1,2,1,1,1,10,5)," +
			"(2,1,1,1,1,20,6),(2,2,1,1,1,20,6),(3,1,1,1,1,30,7),(4,1,1,1,1,40,8)",
		"INSERT INTO zz_sa_r VALUES (2),(4)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	const exists = ` AND EXISTS (SELECT 1 FROM zz_sa_o o2
	                             WHERE o1.ord = o2.ord AND o1.wh <> o2.wh)`
	const notExists = ` AND NOT EXISTS (SELECT 1 FROM zz_sa_r wr1
	                                    WHERE o1.ord = wr1.ord)`

	// The three arms differ only in how many dimension tables the outer
	// relation is joined to. The defect needed >= 3 (that is what makes
	// rewriteMultiWayChain pack a MultiHashJoin and re-sort the layout), so
	// the 2-table arm is the control that was ALREADY correct — keeping it
	// pins the boundary rather than just the symptom.
	from := map[string]string{
		"2tbl": `FROM zz_sa_o o1, zz_sa_d
		          WHERE o1.dsk = zz_sa_d.dsk AND dt = 10`,
		"3tbl": `FROM zz_sa_o o1, zz_sa_d, zz_sa_ca
		          WHERE o1.dsk = zz_sa_d.dsk AND dt = 10
		            AND o1.ask = zz_sa_ca.ask AND st = 'IL'`,
		"4tbl": `FROM zz_sa_o o1, zz_sa_d, zz_sa_ca, zz_sa_ws
		          WHERE o1.dsk = zz_sa_d.dsk AND dt = 10
		            AND o1.ask = zz_sa_ca.ask AND st = 'IL'
		            AND o1.ssk = zz_sa_ws.ssk AND comp = 'pri'`,
	}

	// PG 18.3 values. Identical for every base width — the dimension joins
	// are all 1:1 and non-filtering here, so widening the base must not
	// change a single number.
	type want struct{ rows, distinct, cost, profit int64 }
	oracle := map[string]want{
		"base":      {6, 4, 130, 37},
		"exists":    {4, 2, 60, 22},
		"notexists": {3, 2, 50, 17},
		"both":      {2, 1, 20, 10},
	}

	for _, width := range []string{"2tbl", "3tbl", "4tbl"} {
		conj := map[string]string{
			"base":      "",
			"exists":    exists,
			"notexists": notExists,
			"both":      exists + notExists,
		}
		got := map[string]want{}
		for _, arm := range []string{"base", "exists", "notexists", "both"} {
			q := fmt.Sprintf(
				`SELECT count(*), count(DISTINCT o1.ord), sum(o1.cost), sum(o1.profit) %s%s`,
				from[width], conj[arm])
			rows := runQuery(t, ctx, q)
			if len(rows) != 1 {
				t.Fatalf("%s/%s: got %d rows, want 1", width, arm, len(rows))
			}
			g := want{rows[0][0].Int, rows[0][1].Int, rows[0][2].Int, rows[0][3].Int}
			got[arm] = g
			if w := oracle[arm]; g != w {
				t.Errorf("%s/%s = %+v, want (PG 18.3) %+v", width, arm, g, w)
			}
		}

		// The invariant that named this defect, asserted directly rather
		// than inferred from the numbers above: ANDing a conjunct can only
		// ever REMOVE rows, so `base AND p AND q` must be a subset of both
		// `base AND p` and `base AND q`. goopg returned 4 rows for `both`
		// where `notexists` alone admits only 3 — 4 is not a subset of 3.
		for _, single := range []string{"exists", "notexists"} {
			if got["both"].rows > got[single].rows {
				t.Errorf("%s: monotonicity violated — both=%d rows > %s alone=%d rows; "+
					"adding a conjunct must never grow the result",
					width, got["both"].rows, single, got[single].rows)
			}
		}
	}
}
