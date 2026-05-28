package planner_test

import (
	"testing"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/catalog"
)

func TestPlanRowIn(t *testing.T) {
	cat := catalog.NewInMemory()
	
	stmts, err := parser.Parse("select * from (select 1 as a, 2 as b) t where (a,b) in (values (1,1), (2,2))")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	
	node, err := planner.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}
	t.Logf("plan OK: %T", node)
	_ = node
}

func TestPlanLateralValuesNocols(t *testing.T) {
	cat := catalog.NewInMemory()
	
	// Create a no-columns table
	cat.RegisterTable(&catalog.Table{Name: "nocols", Columns: []catalog.Column{}})
	
	stmts, err := parser.Parse("SELECT * FROM nocols n, LATERAL (VALUES(n.*)) v")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	
	node, err := planner.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}
	t.Logf("plan OK: %T, schema=%v", node, node.Output())
	_ = node
}
