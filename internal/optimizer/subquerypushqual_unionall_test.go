package optimizer

import "testing"

// unionPushPlanFacts reports whether the plan holds a SetOp, a Filter
// directly above one (the restriction left on the Append), and how many
// filtered scans there are.
func unionPushPlanFacts(n Node) (setOps, filterOnSetOp int) {
	var walk func(n, parent Node)
	walk = func(n, parent Node) {
		if n == nil {
			return
		}
		if _, ok := n.(*SetOp); ok {
			setOps++
			if _, isFilter := parent.(*Filter); isFilter {
				filterOnSetOp++
			}
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			walk(k, n)
		}
	}
	walk(n, nil)
	return
}

// TestUnionAllRestrictionPushdown pins M0146-0094 against PG 18.3
// (analysis/m0146/m0146-0094/): set_append_rel_size pushes a restriction on
// a UNION ALL appendrel into every member with the member's expression
// substituted; a copy folding to TRUE adds nothing, one folding to FALSE
// removes the member, and a single remaining member is no Append at all.
// No restriction is left as a Filter on the Append.
func TestUnionAllRestrictionPushdown(t *testing.T) {
	cat := subqueryScanFixture(t)
	const u = "(select a x, 1 src from t1 union all select a, 2 from t2) u"
	for _, tc := range []struct {
		sql    string
		setOps int
	}{
		{"select x from " + u + " where src > 0", 1},
		{"select x from " + u + " where src = 2", 0},
		{"select x from " + u + " where x > 5", 1},
		{"select x from " + u + " where src = 1 and x > 5", 0},
		{"select count(*) from " + u + ", t2 where t2.a = u.x and u.src = 1", 0},
	} {
		plan := planSQL(t, cat, tc.sql)
		setOps, onSetOp := unionPushPlanFacts(plan)
		if setOps != tc.setOps || onSetOp != 0 {
			t.Errorf("%s: %d set-ops (want %d), %d Filters left on the Append", tc.sql, setOps, tc.setOps, onSetOp)
		}
		// Pushed quals are not the members' own WHERE: no "*SELECT* n"
		// wrapper (is_safe_append_member is decided before the push).
		if findSubqueryScanAlias(plan, "*SELECT* 1") != nil || findSubqueryScanAlias(plan, "*SELECT* 2") != nil {
			t.Errorf("%s: a pushed qual made a member look unsafe", tc.sql)
		}
	}
	// Declined: members of different types (char(5) and text make bpchar,
	// tlist_same_datatypes fails, PG builds no appendrel) keep the Filter.
	plan0 := planSQL(t, cat, "select c from (select 'x'::char(5) c from t1 union all select 'x '::text from t2) u where c = 'x'")
	if setOps, onSetOp := unionPushPlanFacts(plan0); setOps != 1 || onSetOp != 1 {
		t.Errorf("mixed types: want the Filter kept on the Append, got %d set-ops, %d Filters", setOps, onSetOp)
	}
	// Declined: a grouped member is a subquery RTE in PG; a qual on its
	// aggregate output cannot become a WHERE qual of the member.
	plan1 := planSQL(t, cat, "select x from (select a x, count(*) c from t1 group by a union all select a, 1 from t2) u where c > 1")
	if setOps, onSetOp := unionPushPlanFacts(plan1); setOps != 1 || onSetOp != 1 {
		t.Errorf("grouped member: want the Filter kept on the Append, got %d set-ops, %d Filters", setOps, onSetOp)
	}
	// Declined: a conjunct reading another FROM item stays above.
	plan := planSQL(t, cat, "select x from "+u+", t2 where u.x > t2.a")
	if setOps, _ := unionPushPlanFacts(plan); setOps != 1 {
		t.Errorf("join qual: want the union kept, got %d set-ops", setOps)
	}
}
