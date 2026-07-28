package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// projectOverAggregate returns the Project node sitting directly above the
// first Aggregate in the plan — the node remapSubqueryColumnRefs rewrites when
// the Aggregate is inside a FROM-clause subquery.
func projectOverAggregate(t *testing.T, c catalog.Catalog, sql string) *Project {
	t.Helper()
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	var found *Project
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		switch x := n.(type) {
		case *Project:
			if _, ok := x.Child.(*Aggregate); ok {
				found = x
				return
			}
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *SetOp:
			walk(x.Left)
			walk(x.Right)
		}
	}
	walk(plan)
	if found == nil {
		t.Fatalf("no Project-over-Aggregate in plan for %q", sql)
	}
	return found
}

// targetIndices returns the child-schema slot each bare-ColumnRef target of p
// reads.  A non-ColumnRef target yields -1 (it needs no remapping).
func targetIndices(p *Project) []int {
	out := make([]int, len(p.Targets))
	for i, tg := range p.Targets {
		if cr, ok := tg.(*ColumnRef); ok {
			out[i] = cr.Index
		} else {
			out[i] = -1
		}
	}
	return out
}

// TestSubqueryRemapKeepsSiblingAggregateSlots is the M0125-0010 reproducer.
//
// remapSubqueryColumnRefs rebound EVERY bare-ColumnRef Project target by
// matching cr.Name against the child output schema and taking the first hit
// (`strings.EqualFold(cr.Name, sc.Name)` + `break`).  An Aggregate names its
// output columns after the aggregate *function*, so a child schema of two
// sums is literally [sum, sum] and both targets bound to slot 0.
//
// Measured on the TPC-DS SF=1 clusters (goopg 65436 vs PG 65438):
//
//	select * from (select sum(d_dom) a, sum(d_year) b from date_dim) d;
//	  goopg 1149021|1149021   PG 1149021|146061700
//
// The identical flat query is correct — the pass runs only from
// planSubqueryRangeVar — which is why row-count gates cannot see this class.
// Six TPC-DS queries carried it: Q21 Q28 Q46 Q66 Q68 Q79.
func TestSubqueryRemapKeepsSiblingAggregateSlots(t *testing.T) {
	c := dateDimLikeCatalog(t)
	p := projectOverAggregate(t, c,
		"select * from (select sum(d_dom) a, sum(d_year) b from date_dim) d")
	got := targetIndices(p)
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("sibling sum() targets bind to slots %v, want [0 1]; "+
			"[0 0] is the M0125-0010 collapse — both targets took the first "+
			"child column named \"sum\", so sum(d_dom) is returned twice", got)
	}
}

// TestSubqueryRemapControlMatrix pins the shapes that were ALREADY correct
// before M0125-0010, so a future change to the pass cannot regress them while
// keeping the reproducer green.  Every row was probed against PG 18.3 on the
// SF=1 clusters when the defect was isolated (fix_plan M0125-0010).
func TestSubqueryRemapControlMatrix(t *testing.T) {
	c := dateDimLikeCatalog(t)
	cases := []struct {
		name string
		sql  string
		want []int
	}{
		// Explicit column-alias list on the derived table: renames the
		// OUTER schema only, so the inner Project still sees [sum, sum].
		// This was wrong too — the aliases never reached the inner plan.
		{"explicit column list", "select * from (select sum(d_dom), sum(d_year) from date_dim) d(x, y)", []int{0, 1}},
		// Selecting one column of the pair: the defect returned column a's
		// value for b, so the inner plan — not outer resolution — is at fault.
		{"single column of pair", "select d.b from (select sum(d_dom) a, sum(d_year) b from date_dim) d", []int{0, 1}},
		// Distinct function names produce distinct child names, so the old
		// first-match search happened to be right here.
		{"distinct function names", "select * from (select sum(d_dom) a, avg(d_year) b from date_dim) d", []int{0, 1}},
		{"three distinct functions", "select * from (select sum(d_dom) a, count(*) b, avg(d_year) c from date_dim) d", []int{0, 1, 2}},
		// count(x) vs count(distinct x): DISTINCT does not change the output
		// column name, so these collapsed as well.
		{"count vs count distinct", "select * from (select count(d_dom) a, count(distinct d_dom) b from date_dim) d", []int{0, 1}},
		// GROUP BY inside the derived table: the grouping key and the two
		// aggregates must each keep their own slot.
		{"group by with sibling sums", "select * from (select d_day_name, sum(d_dom) a, sum(d_year) b from date_dim group by d_day_name) d", []int{0, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetIndices(projectOverAggregate(t, c, tc.sql))
			if len(got) != len(tc.want) {
				t.Fatalf("target count = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("target slots = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestSubqueryRemapStillRepairsLeakedIndices is the guard for the reason the
// pass exists at all (M0097-0058): outer resolve-context leakage can leave a
// sub-SELECT's Project referencing a column by its GLOBAL FROM-clause index
// (e.g. 57) instead of the subquery's own output index, which is an
// index-out-of-bounds crash at execution time.
//
// The M0125-0010 fix narrows the pass to demonstrably-broken indices, so this
// test asserts the repair path is still live — both for an out-of-range index
// and for an in-range index that names a different column.
func TestSubqueryRemapStillRepairsLeakedIndices(t *testing.T) {
	child := &Values{schema: Schema{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}}
	p := &Project{
		Child: child,
		Targets: []Expr{
			// Leaked global FROM-clause index — out of range for the child.
			&ColumnRef{Name: "b", Index: 57},
			// In range, but slot 1 is "b" not "a" — also leakage.
			&ColumnRef{Name: "a", Index: 1},
		},
		schema: child.Output(),
	}
	got := targetIndices(remapSubqueryColumnRefs(p).(*Project))
	if len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Errorf("repaired slots = %v, want [1 0]; the pass must still rebuild "+
			"an index that is out of range (57) or that names a different "+
			"column than the ref asks for (M0097-0058 leakage)", got)
	}
}
