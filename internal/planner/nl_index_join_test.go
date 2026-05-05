package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestNLIRulePromotesEquiJoinOnIndexedInner asserts the M0054-0006c
// rewrite fires for the canonical shape: a binary equi-join whose
// inner table has a single-column B-tree index on the join key.
func TestNLIRulePromotesEquiJoinOnIndexedInner(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, parts,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stmt := parseOne(t, `SELECT p_name, l_quantity FROM lineitem, part WHERE l_partkey = p_partkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !findNLI(node) {
		t.Fatalf("expected NestedLoopIndexJoin in plan; got: %s", describePlanTree(node))
	}
}

// TestNLIRuleSkipsWhenInnerHasNoIndex asserts the rewrite is a
// no-op when the inner side's join column has no B-tree index.
// The cost-gate path is also exercised — outer is small but no
// index → no NLI.
func TestNLIRuleSkipsWhenInnerHasNoIndex(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "a"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "b"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM a, b WHERE a.x = b.y`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findNLI(node) {
		t.Fatalf("did not expect NLI when no index exists; tree: %s", describePlanTree(node))
	}
}

// TestNLIRuleRespectsKillSwitch confirms `SetNLIEnabled(false)`
// short-circuits the rewrite — the rollback path described in the
// M0054-0006e design.
func TestNLIRuleRespectsKillSwitch(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, parts,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM lineitem, part WHERE l_partkey = p_partkey`)

	SetNLIEnabled(false)
	defer SetNLIEnabled(true)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findNLI(node) {
		t.Fatalf("kill-switch off: did not expect NLI; tree: %s", describePlanTree(node))
	}
}

// findNLI returns true when the plan tree contains a
// `*NestedLoopIndexJoin` anywhere.
func findNLI(n Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		return true
	case *Project:
		return findNLI(x.Child)
	case *Filter:
		return findNLI(x.Child)
	case *Sort:
		return findNLI(x.Child)
	case *Limit:
		return findNLI(x.Child)
	case *Aggregate:
		return findNLI(x.Child)
	case *WindowAgg:
		return findNLI(x.Child)
	case *Join:
		return findNLI(x.Left) || findNLI(x.Right)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			if findNLI(t) {
				return true
			}
		}
	}
	return false
}

// describePlanTree returns a short string summary used only for
// failure messages.
func describePlanTree(n Node) string {
	if n == nil {
		return "nil"
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		return "NestedLoopIndexJoin{Outer:" + describePlanTree(x.Outer) + ", Inner:" + describePlanTree(x.Inner) + "}"
	case *Project:
		return "Project(" + describePlanTree(x.Child) + ")"
	case *Filter:
		return "Filter(" + describePlanTree(x.Child) + ")"
	case *Join:
		return "Join{algo:" + joinAlgoName(x.Algo) + "}"
	case *MultiHashJoin:
		return "MHJ"
	case *SeqScan:
		return "Seq(" + x.Table.Name + ")"
	case *IndexScan:
		return "Idx(" + x.Table.Name + "/" + x.Index.Name + ")"
	}
	return "?"
}

func joinAlgoName(a JoinAlgo) string {
	switch a {
	case JoinAlgoNestedLoop:
		return "NL"
	case JoinAlgoHash:
		return "Hash"
	case JoinAlgoMerge:
		return "Merge"
	}
	return "?"
}
