package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// S8 Slice 2c-i (0134-0001 P2) unit tests for the index-ordered-grouping-
// input rule. Each test plans a probe against btgIndexOrderCatalog (a
// btg-shaped table: x, y int4 columns covered by a composite btree index,
// plus z, w columns outside it) and inspects the Aggregate node's child /
// Strategy / GroupKeyOrder.

// btgIndexOrderCatalog mirrors the brief's canonical target:
//
//	CREATE TABLE btg AS SELECT i%10 AS x, i%10 AS y, 'abc'||i%10 AS z, i AS w ...
//	CREATE INDEX btg_x_y_idx ON btg(x, y);
func btgIndexOrderCatalog(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Index) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "btg"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "text"}},
		{Name: "w", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "btg_x_y_idx"}, tbl, []string{"x", "y"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	return c, tbl, idx
}

// indexOrderAggPlan digs the Aggregate node out of a plan rooted at Project,
// same traversal as presortedAggPlan (groupagg_presorted_test.go).
func indexOrderAggPlan(t *testing.T, node Node) *Aggregate {
	t.Helper()
	p, ok := node.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", node)
	}
	for c := p.Child; c != nil; {
		if a, ok := c.(*Aggregate); ok {
			return a
		}
		switch x := c.(type) {
		case *Filter:
			c = x.Child
		case *Project:
			c = x.Child
		default:
			t.Fatalf("no Aggregate under Project (stopped at %T)", c)
		}
	}
	t.Fatalf("no Aggregate under Project")
	return nil
}

// TestIndexOrderedGroupingCanonical: the brief's canonical acceptance shape
// — `SET enable_hashagg = off; SELECT count(*) FROM btg GROUP BY y, x` —
// must plan as GroupAggregate (Strategy Sorted) directly over an
// *IndexOnlyScan using btg_x_y_idx, with NO Sort inserted, and
// GroupKeyOrder = [1, 0] (x is GroupExprs[1], y is GroupExprs[0]; the index
// lays them out x, y). The rule requires enable_hashagg off (see
// applyIndexOrderedGroupingRule's doc comment): without a cost model, goopg
// cannot otherwise tell whether the sorted index-driven plan actually beats
// a hash aggregate — aggregates.sql's own btg block runs entirely inside
// `SET enable_hashagg = off`.
func TestIndexOrderedGroupingCanonical(t *testing.T) {
	SetHashAggEnabled(false)
	defer SetHashAggEnabled(true)
	cat, _, idx := btgIndexOrderCatalog(t)
	stmt := parseOne(t, "select count(*) from btg group by y, x")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted", a.Strategy)
	}
	ios, ok := a.Child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want *IndexOnlyScan (no Sort)", a.Child)
	}
	if ios.Index != idx {
		t.Fatalf("IndexOnlyScan.Index = %v, want btg_x_y_idx", ios.Index)
	}
	if ios.Key != nil || ios.Keys != nil || ios.LowKey != nil || ios.HighKey != nil {
		t.Fatalf("IndexOnlyScan probe fields set: want nil (full ascending range scan)")
	}
	// GroupExprs must stay in WRITTEN order: y, x.
	if len(a.GroupExprs) != 2 {
		t.Fatalf("len(GroupExprs) = %d, want 2", len(a.GroupExprs))
	}
	cr0, ok := a.GroupExprs[0].(*ColumnRef)
	if !ok || cr0.Name != "y" {
		t.Fatalf("GroupExprs[0] = %v, want ColumnRef(y) (written order preserved)", a.GroupExprs[0])
	}
	cr1, ok := a.GroupExprs[1].(*ColumnRef)
	if !ok || cr1.Name != "x" {
		t.Fatalf("GroupExprs[1] = %v, want ColumnRef(x) (written order preserved)", a.GroupExprs[1])
	}
	want := []int{1, 0}
	if len(a.GroupKeyOrder) != len(want) {
		t.Fatalf("GroupKeyOrder = %v, want %v", a.GroupKeyOrder, want)
	}
	for i := range want {
		if a.GroupKeyOrder[i] != want[i] {
			t.Fatalf("GroupKeyOrder = %v, want %v", a.GroupKeyOrder, want)
		}
	}
}

// The brief's acceptance criterion 1 (order-independence data test —
// GROUP BY y, x and GROUP BY x, y run end to end and the RESULT ROWS/VALUES
// are asserted, not just the plan text) lives in
// internal/executor/groupagg_indexorder_data_test.go: it needs the full
// executor stack (INSERT + SELECT), which internal/optimizer does not import
// (Plan()-only unit tests live here; end-to-end data tests live in
// internal/executor, which imports optimizer).

// TestIndexOrderedGroupingPartialPrefixNotClaimed: GROUP BY z, y, w, x has
// four keys but the only index (x, y) has just two columns, so no ordering
// of the group keys can be a full leading-prefix match. Slice 2c-ii
// (Incremental Sort) is deferred — the rule must leave the plan for Slice
// 2a/2b's ordinary Sort to claim.
func TestIndexOrderedGroupingPartialPrefixNotClaimed(t *testing.T) {
	SetHashAggEnabled(false)
	defer SetHashAggEnabled(true)
	cat, _, _ := btgIndexOrderCatalog(t)
	stmt := parseOne(t, "select count(*) from btg group by z, y, w, x")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if _, ok := a.Child.(*IndexOnlyScan); ok {
		t.Fatalf("rule fired on a partial-prefix GROUP BY: Aggregate.Child is *IndexOnlyScan")
	}
	if _, ok := a.Child.(*IndexScan); ok {
		t.Fatalf("rule fired on a partial-prefix GROUP BY: Aggregate.Child is *IndexScan")
	}
	if a.GroupKeyOrder != nil {
		t.Fatalf("GroupKeyOrder = %v, want nil (rule did not fire)", a.GroupKeyOrder)
	}
}

// TestIndexOrderedGroupingDescendingIndexNotClaimed: an index with a
// descending leading column cannot satisfy an ascending full-range scan, so
// the rule must not claim it even though the column-name SET matches.
func TestIndexOrderedGroupingDescendingIndexNotClaimed(t *testing.T) {
	SetHashAggEnabled(false)
	defer SetHashAggEnabled(true)
	cat, _, idx := btgIndexOrderCatalog(t)
	idx.ColDescending = []bool{true, false}
	stmt := parseOne(t, "select count(*) from btg group by y, x")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if _, ok := a.Child.(*IndexOnlyScan); ok {
		t.Fatalf("rule fired on a descending-leading-column index: Aggregate.Child is *IndexOnlyScan")
	}
	if a.GroupKeyOrder != nil {
		t.Fatalf("GroupKeyOrder = %v, want nil (rule did not fire)", a.GroupKeyOrder)
	}
}

// TestIndexOrderedGroupingExpressionKeyNotClaimed: GROUP BY x + 0, y has an
// expression group key, which is out of Slice 2c-i's scope (design section:
// "GROUP BY x*x is out of scope") — the rule must not fire.
func TestIndexOrderedGroupingExpressionKeyNotClaimed(t *testing.T) {
	SetHashAggEnabled(false)
	defer SetHashAggEnabled(true)
	cat, _, _ := btgIndexOrderCatalog(t)
	stmt := parseOne(t, "select count(*) from btg group by x + 0, y")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if _, ok := a.Child.(*IndexOnlyScan); ok {
		t.Fatalf("rule fired on an expression group key: Aggregate.Child is *IndexOnlyScan")
	}
	if a.GroupKeyOrder != nil {
		t.Fatalf("GroupKeyOrder = %v, want nil (rule did not fire)", a.GroupKeyOrder)
	}
}

// TestIndexOrderedGroupingNoIndexNotClaimed: a table with no index at all
// must leave the plan untouched.
func TestIndexOrderedGroupingNoIndexNotClaimed(t *testing.T) {
	SetHashAggEnabled(false)
	defer SetHashAggEnabled(true)
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "btg"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, "select count(*) from btg group by y, x")
	node, err := Plan(stmt, c)
	if err != nil {
		t.Fatal(err)
	}
	a := indexOrderAggPlan(t, node)
	if _, ok := a.Child.(*IndexOnlyScan); ok {
		t.Fatalf("rule fired with no index on the table: Aggregate.Child is *IndexOnlyScan")
	}
	if a.GroupKeyOrder != nil {
		t.Fatalf("GroupKeyOrder = %v, want nil (rule did not fire)", a.GroupKeyOrder)
	}
}
