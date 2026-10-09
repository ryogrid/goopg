package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestMergeJoinCostMaterializeElection pins final_cost_mergejoin's
// materialize_inner arms (costsize.c:3986-4040, M0146-0005bd) and its price:
// a presorted inner that cannot mark/restore (a CTE Scan) is materialized at
// cpu_operator_cost per fetched tuple; a btree index scan inner is not; a
// unique inner whose join clauses are all merge clauses skips mark/restore
// and is never materialized; duplicate rescans make buffering cheaper than
// re-running. Figures are PG 18.3's for
// `WITH v AS MATERIALIZED (... 200 rows ...) SELECT * FROM v x JOIN v y ON x.a = y.a`:
// Merge Join run 11.50 over two 4.00 CTE scans.
func TestMergeJoinCostMaterializeElection(t *testing.T) {
	cp := defaultCostParams()
	scan := Cost{Startup: 0, Total: 4}
	cte := mergeInner{qualOps: 1}
	cost, mat := mergeJoinCost(cp, scan, scan, 200, 200, 200, 1, 1, cte)
	if !mat {
		t.Fatalf("a presorted CTE Scan inner must be materialized")
	}
	if math.Abs(cost.Total-11.50) > 1e-9 {
		t.Errorf("merge cost %.4f, want PG's 11.50", cost.Total)
	}
	idx := mergeInner{qualOps: 1, markRestore: true}
	if _, mat := mergeJoinCost(cp, scan, scan, 200, 200, 200, 1, 1, idx); mat {
		t.Errorf("an index scan inner can mark/restore and must not be materialized")
	}
	uniq := mergeInner{qualOps: 1, skipMarkRestore: true}
	if _, mat := mergeJoinCost(cp, scan, scan, 200, 200, 2000, 1, 1, uniq); mat {
		t.Errorf("skip_mark_restore must never materialize")
	}
	// 2000 merged tuples over a 200-row inner: 1800 rescanned tuples make
	// re-running a 40.00 inner (x10) dearer than buffering it.
	dear := Cost{Startup: 0, Total: 40}
	if _, mat := mergeJoinCost(cp, scan, dear, 200, 200, 2000, 1, 1, idx); !mat {
		t.Errorf("heavy rescans of an expensive inner must elect the Material")
	}
}

// TestGroupedLeafDistinctFor pins the GROUP BY arm of query_is_distinct_for
// (M0146-0005bd): a grouped leaf is distinct for a clause set equating every
// grouping column — through a renaming Project — and not for a subset.
func TestGroupedLeafDistinctFor(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	scan := &Values{schema: Schema{{Name: "a", Type: int4}, {Name: "b", Type: int4}}}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "a", Type: int4}, &ColumnRef{Index: 1, Name: "b", Type: int4}},
		schema:     Schema{{Name: "a", Type: int4}, {Name: "b", Type: int4}, {Name: "count", Type: int4}},
	}
	proj := &Project{Child: agg,
		Targets: []Expr{&ColumnRef{Index: 1, Name: "b", Type: int4}, &ColumnRef{Index: 0, Name: "a", Type: int4}, &ColumnRef{Index: 2, Name: "count", Type: int4}},
		schema:  Schema{{Name: "y", Type: int4}, {Name: "x", Type: int4}, {Name: "c", Type: int4}}}
	pair := func(col string) joinKeyPair { return joinKeyPair{rel: [2]int{0, 1}, col: [2]string{"k", col}, usable: true} }
	if !groupedLeafDistinctFor(proj, 1, []joinKeyPair{pair("x"), pair("y")}) {
		t.Errorf("both grouping columns equated through the Project: want distinct")
	}
	if groupedLeafDistinctFor(proj, 1, []joinKeyPair{pair("x")}) {
		t.Errorf("one of two grouping columns equated: must not be distinct")
	}
	if groupedLeafDistinctFor(proj, 1, []joinKeyPair{pair("c")}) {
		t.Errorf("an aggregate output is no grouping column")
	}
}
