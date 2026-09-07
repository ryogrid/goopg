package optimizer

// E-21 Cut 1b — the `isSimpleSingle` re-route
// (docs/design/planner-e20-e21-parallel-path-search/DESIGN.md §4 Cut 1b).
//
// Closing E-21 means replacing `planIndexScanFromWhere` with `add_path`
// for every single-table statement, not changing a floor. The two
// `isSimpleSingle` diversions in planSelect (the FROM arm and the WHERE
// arm) fall through to the generic Filter+search machinery under
// `GOOPG_ONEREL_SEARCH` (default OFF). These pins assert the ROUTING at
// planSelect level — the seam-level admission is Cut 1's pins' subject —
// and that the OFF arm is byte-for-byte the historical branch:
//
//  1. knob ON: a single-table WHERE statement's final plan contains a
//     searched subtree (the search ran);
//  2. knob OFF: the same statement contains none (the rule chooser ran);
//  3. knob ON: a WHERE-less single-table statement still plans (the FROM
//     fallthrough does not break the no-Filter shape).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// oneRelRoutedCatalog is one table with statistics, so the search's
// size gates have something to read. Without Stats the relsize fallback
// answers, but the pin should not depend on which fallback is compiled.
func oneRelRoutedCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int8"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, cols)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 10_000_000, Pages: 20_000, Analyzed: true}
	return cat
}

func planRouted(t *testing.T, cat catalog.Catalog, sql string) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return node
}

func treeHasSearched(n Node) bool {
	found := false
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || found {
			return
		}
		if isSearchedTree(cur) {
			found = true
			return
		}
		switch x := cur.(type) {
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		}
	}
	walk(n)
	return found
}

// (1) Knob ON routes the single-table statement through the search.
func TestOneRelRerouteSearchesSingleTableStatement(t *testing.T) {
	withPGShapedDP(t)
	t.Cleanup(setOneRelSearchForTest(true))
	cat := oneRelRoutedCatalog(t)
	node := planRouted(t, cat, "SELECT a FROM t WHERE a > 5")
	if !treeHasSearched(node) {
		t.Fatalf("knob on: no searched subtree in %T — the statement did not reach the search", node)
	}
}

// (2) Knob OFF is the historical branch: no search.
func TestOneRelRerouteIsInertWithTheKnobOff(t *testing.T) {
	withPGShapedDP(t)
	t.Cleanup(setOneRelSearchForTest(false))
	cat := oneRelRoutedCatalog(t)
	node := planRouted(t, cat, "SELECT a FROM t WHERE a > 5")
	if treeHasSearched(node) {
		t.Fatal("knob off: searched subtree present — the rule chooser's branch was disturbed")
	}
}

// (3) The FROM fallthrough does not break WHERE-less statements.
func TestOneRelReroutePlansWhereLessSingleTable(t *testing.T) {
	withPGShapedDP(t)
	t.Cleanup(setOneRelSearchForTest(true))
	cat := oneRelRoutedCatalog(t)
	node := planRouted(t, cat, "SELECT a FROM t")
	if node == nil {
		t.Fatal("knob on: WHERE-less single-table statement failed to plan")
	}
}
