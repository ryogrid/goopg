package optimizer

// M0146-0005x: the HAVING arm of PG's subquery_push_qual (allpaths.c).
//
// A leaf-local restriction conjunct on a grouped derived table sinks below
// the SubqueryScan label into a *Filter directly above the leaf's
// *Aggregate — upstream's "attach to subquery->havingQual" — so the label
// itself then strips under trivial_subqueryscan's rules. These tests pin
// the admit/decline matrix: push for plain per-row quals on the leaf's
// output columns, decline for volatile predicates, sublink conjuncts,
// non-aggregate children, non-passthrough projections, and leaves on a
// nullable side of an outer join (where upstream's delay rules keep the
// qual out of baserestrictinfo to begin with). Live shape on PG 18.3
// (tpcds025): `... join (select c_customer_sk a, count(*) c …) u on …
// where u.c between lo and hi` renders `Filter: ((count(*) >= lo) AND
// (count(*) <= hi))` on the aggregate with no `Subquery Scan` line.

import (
	"testing"
)

// findFilterOnAgg returns the first *Filter whose Child is an *Aggregate
// (DFS), the plan shape a native or pushed HAVING takes.
func findFilterOnAgg(n Node) *Filter {
	if n == nil {
		return nil
	}
	if f, ok := n.(*Filter); ok {
		if _, isAgg := f.Child.(*Aggregate); isAgg {
			return f
		}
	}
	kids, ok := planChildNodes(n)
	if !ok {
		return nil
	}
	for _, k := range kids {
		if f := findFilterOnAgg(k); f != nil {
			return f
		}
	}
	return nil
}

// subqueryScanFilterParent reports whether the leaf's immediate parent is
// a *Filter — the "kept leaf-local qual" shape.
func subqueryScanFilterParent(root, want Node) bool {
	found := false
	var visit func(n Node)
	visit = func(n Node) {
		if n == nil || found {
			return
		}
		if f, ok := n.(*Filter); ok && f.Child == want {
			found = true
			return
		}
		if kids, ok := planChildNodes(n); ok {
			for _, k := range kids {
				visit(k)
			}
		}
	}
	visit(root)
	return found
}

// TestSubqueryPushQualGroupedUnderJoin pins the Q34/Q73 shape: a
// leaf-local range qual on the aggregate's output column moves inside the
// wrapper as Filter{Aggregate}, and with no leaf qual left the triviality
// strip removes the label under the join (physical-tlist regime).
func TestSubqueryPushQualGroupedUnderJoin(t *testing.T) {
	cat := subqueryScanFixture(t)
	plan := planSQL(t, cat,
		"select u.a, u.c, t1.b from t1, (select a, count(a) c from t1 group by a) u "+
			"where t1.a = u.a and u.c between 1 and 5")
	if sq := findSubqueryScan(plan); sq != nil {
		t.Errorf("SubqueryScan kept on a fully-pushed leaf (alias %q) — PG strips it once "+
			"the qual moves to the subquery's havingQual", sq.Alias)
	}
	if findFilterOnAgg(plan) == nil {
		t.Error("no Filter{Aggregate} in plan — the leaf-local qual did not reach the subquery's aggregate")
	}
}

// TestSubqueryPushQualSubsetConsumption: even when the strip declines
// (subset consumption at top level — pathtarget regime), the qual still
// moves inside the wrapper onto the aggregate, matching upstream where the
// surviving SubqueryScan renders the pushed qual on its subplan.
func TestSubqueryPushQualSubsetConsumption(t *testing.T) {
	cat := subqueryScanFixture(t)
	plan := planSQL(t, cat,
		"select u.q from (select a, count(a) c from t1 group by a) u(p, q) where u.q > 0")
	sq := findSubqueryScan(plan)
	if sq == nil {
		t.Fatal("wrapper stripped on subset consumption at top level — PG keeps it (pathtarget tlist)")
	}
	if subqueryScanFilterParent(plan, sq) {
		t.Error("leaf Filter kept above the wrapper — the qual should have sunk into the aggregate")
	}
	if findFilterOnAgg(sq) == nil {
		t.Error("no Filter{Aggregate} inside the wrapper — pushed HAVING arm did not fire")
	}
}

// TestSubqueryPushQualDeclines — every unsafe shape must keep the qual
// leaf-local (over-keep is the correct direction; PG pushes a subset of
// these but none of them changes semantics when kept).
func TestSubqueryPushQualDeclines(t *testing.T) {
	cat := subqueryScanFixture(t)
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"volatile predicate",
			"select u.a, t1.b from t1, (select a, count(a) c from t1 group by a) u " +
				"where t1.a = u.a and u.c > random()"},
		{"non-aggregate child (set op)",
			"select u.a, t1.b from t1, (select a from t1 union select a from t1) u " +
				"where t1.a = u.a and u.a > 0"},
		{"non-passthrough targetlist",
			"select u.a, t1.b from t1, (select a, a + 1 s, count(a) c from t1 group by a) u " +
				"where t1.a = u.a and u.s > 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planSQL(t, cat, tc.sql)
			sq := findSubqueryScan(plan)
			if sq == nil {
				t.Error("wrapper stripped — an unpushed leaf qual must keep `Subquery Scan` (upstream non-trivial)")
				return
			}
			if !subqueryScanFilterParent(plan, sq) {
				t.Error("no Filter above the wrapper — a declined conjunct must stay leaf-local, not be dropped")
			}
		})
	}
}

// TestSubqueryPushQualFullJoin — a qual on the nullable side of a FULL
// join must evaluate on null-extended rows, so it lives as a join-level
// Filter above the join; sinking it into the subquery's aggregate would
// apply it before null-extension and change the result. (A strict
// nullable-side qual under a LEFT join degenerates to inner before the
// seam runs — reduce_outer_joins parity — and pushes legitimately; the
// FULL arm is the one that exercises the decline.)
func TestSubqueryPushQualFullJoin(t *testing.T) {
	cat := subqueryScanFixture(t)
	plan := planSQL(t, cat,
		"select t1.a from t1 full join (select a, count(a) c from t1 group by a) u "+
			"on t1.a = u.a where u.c > 0")
	if findFilterOnAgg(plan) != nil {
		t.Error("nullable-side qual sank into the subquery's aggregate — " +
			"it must evaluate post-null-extension at the join level")
	}
}
