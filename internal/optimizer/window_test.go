package optimizer

import "testing"

func TestPlanWindowAggInjected(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT row_number() OVER (ORDER BY aid) AS rn FROM pgbench_accounts"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	win, ok := proj.Child.(*WindowAgg)
	if !ok {
		t.Fatalf("Project.Child=%T want *WindowAgg", proj.Child)
	}
	if len(win.Funcs) != 1 || win.Funcs[0].Name != "row_number" {
		t.Fatalf("WindowAgg funcs=%+v", win.Funcs)
	}
	if len(win.OrderBy) != 1 {
		t.Fatalf("WindowAgg order keys=%d want 1", len(win.OrderBy))
	}
	cr, ok := proj.Targets[0].(*ColumnRef)
	if !ok {
		t.Fatalf("target=%T want *ColumnRef", proj.Targets[0])
	}
	if cr.Index != len(win.Child.Output()) {
		t.Fatalf("target index=%d want %d", cr.Index, len(win.Child.Output()))
	}
}

func TestPlanWindowOrderByAliasUsesWindowOutput(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT row_number() OVER (ORDER BY aid) AS rn FROM pgbench_accounts ORDER BY rn"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project.Child=%T want *Sort", proj.Child)
	}
	win, ok := sort.Child.(*WindowAgg)
	if !ok {
		t.Fatalf("Sort.Child=%T want *WindowAgg", sort.Child)
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("sort keys=%d want 1", len(sort.Keys))
	}
	cr, ok := sort.Keys[0].Expr.(*ColumnRef)
	if !ok {
		t.Fatalf("sort key=%T want *ColumnRef", sort.Keys[0].Expr)
	}
	if cr.Index != len(win.Child.Output()) {
		t.Fatalf("sort key index=%d want %d", cr.Index, len(win.Child.Output()))
	}
}

func TestPlanWindowMultipleSpecs(t *testing.T) {
	cat := pgbenchCatalog(t)
	plan, err := Plan(parseOne(t,
		"SELECT row_number() OVER (ORDER BY aid), rank() OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts"), cat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}
