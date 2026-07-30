package planner

// M0125-0012 (TPC-DS Q8): the join-reorder position map must not be
// applied inside a FROM-subquery's own Project scope.
//
// `buildBindingsPosMap`'s `collect` walker stops at every join-tree
// Project — it advances `off` by the projected output width and does not
// descend, because the scans below belong to a separate planning scope.
// The map it returns is therefore only defined over the OUTER bindings.
// `applyJoinTreePosMap` used to descend into those same Projects (it
// stopped only at `IsolatedScope` view-rename wrappers) and rewrite their
// targets, applying the map outside its domain.
//
// For Q8 that turned the V1 derived table's correct `ca_zip/0` — correct
// against its 1-column SetOp child — into the MHJ offset of whichever
// outer table starts at FROM-position 0, and execution then indexed a
// 1-wide slot with 57. This test pins the structural invariant directly,
// so a regression is caught in the planner without needing to execute.
//
// The end-to-end value discriminator (with the expected non-empty result
// cross-checked on PostgreSQL 18.3) lives in
// internal/executor/q8_subquery_scope_remap_test.go.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

const q8ScopeSQL = `select s_store_name, sum(ss_net_profit)
from ss, dd, st,
     (select ca_zip
        from (select ca_zip from ca where ca_zip in ('47602','16704')
              intersect
              select ca_zip from ca) A2) V1
where ss_store_sk = s_store_sk
  and ss_sold_date_sk = d_date_sk
  and d_qoy = 2 and d_year = 1998
  and (substr(s_zip,1,2) = substr(V1.ca_zip,1,2))
group by s_store_name
order by s_store_name`

func TestQ8SubqueryProjectTargetsStayInChildScope(t *testing.T) {
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column) {
		t.Helper()
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	create("dd", []catalog.Column{
		{Name: "d_date_sk", Type: catalog.Type{Name: "int4"}},
		{Name: "d_qoy", Type: catalog.Type{Name: "int4"}},
		{Name: "d_year", Type: catalog.Type{Name: "int4"}},
	})
	create("st", []catalog.Column{
		{Name: "s_store_sk", Type: catalog.Type{Name: "int4"}},
		{Name: "s_store_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_zip", Type: catalog.Type{Name: "text"}},
	})
	create("ss", []catalog.Column{
		{Name: "ss_sold_date_sk", Type: catalog.Type{Name: "int4"}},
		{Name: "ss_store_sk", Type: catalog.Type{Name: "int4"}},
		{Name: "ss_net_profit", Type: catalog.Type{Name: "int4"}},
	})
	create("ca", []catalog.Column{
		{Name: "ca_address_sk", Type: catalog.Type{Name: "int4"}},
		{Name: "ca_zip", Type: catalog.Type{Name: "text"}},
	})

	stmts, err := parser.Parse(q8ScopeSQL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Every Project target that is a bare ColumnRef must address its own
	// child's output. This is scope-independent: it holds for the top
	// projection, for derived tables, and for the INTERSECT arms alike,
	// so it does not over-pin any particular plan shape.
	var checked int
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if p, ok := n.(*Project); ok {
			w := len(p.Child.Output())
			for i, tg := range p.Targets {
				cr, ok := tg.(*ColumnRef)
				if !ok {
					continue
				}
				checked++
				if cr.Index < 0 || cr.Index >= w {
					t.Errorf("M0125-0012: Project target %d (%s/%d) is outside its child's %d-column output — "+
						"an outer-scope index leaked into a subquery Project (the "+
						"`column ref %s/%d out of MaterializedSlot range %d` signature)",
						i, cr.Name, cr.Index, w, cr.Name, cr.Index, w)
				}
			}
		}
		for _, c := range planChildren(n) {
			walk(c)
		}
	}
	walk(plan)
	if checked == 0 {
		t.Fatal("no Project ColumnRef targets were checked — the fixture no longer builds Q8's shape")
	}
}

// planChildren returns the child nodes this test needs to traverse. Kept
// local and explicit rather than reaching for a generic walker: the test
// must keep working even if the shared walkers change their skip rules,
// since those rules are exactly what this test is guarding.
func planChildren(n Node) []Node {
	switch x := n.(type) {
	case *Project:
		return []Node{x.Child}
	case *Filter:
		return []Node{x.Child}
	case *Sort:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Distinct:
		return []Node{x.Child}
	case *Join:
		return []Node{x.Left, x.Right}
	case *SetOp:
		return []Node{x.Left, x.Right}
	case *MultiHashJoin:
		return x.Tables
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	}
	return nil
}
