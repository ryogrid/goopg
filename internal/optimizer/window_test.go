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
	// B-01c slice (c): the ORDER BY Sort reads ONE column of the WindowAgg's
	// five (`rn` alone — the sort key and the only thing the Project above
	// selects), so the upper narrowing sinks a `*Project` between them and the
	// sort materialises a one-column row. The claim this test makes is
	// unchanged and is now made through that Project: the key resolves to the
	// WINDOW FUNC's output column, not to an input column of the same name.
	narrow, ok := sort.Child.(*Project)
	if !ok {
		t.Fatalf("Sort.Child=%T want the narrowing *Project", sort.Child)
	}
	win, ok := narrow.Child.(*WindowAgg)
	if !ok {
		t.Fatalf("narrowing Project.Child=%T want *WindowAgg", narrow.Child)
	}
	if len(narrow.Targets) != 1 {
		t.Fatalf("narrowing Project targets=%d want 1", len(narrow.Targets))
	}
	nc, ok := narrow.Targets[0].(*ColumnRef)
	if !ok {
		t.Fatalf("narrowing target=%T want *ColumnRef", narrow.Targets[0])
	}
	if nc.Index != len(win.Child.Output()) {
		t.Fatalf("narrowing target index=%d want %d (the window func's output column)", nc.Index, len(win.Child.Output()))
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("sort keys=%d want 1", len(sort.Keys))
	}
	cr, ok := sort.Keys[0].Expr.(*ColumnRef)
	if !ok {
		t.Fatalf("sort key=%T want *ColumnRef", sort.Keys[0].Expr)
	}
	// Re-based onto the narrowed row: position 0 of the one column kept.
	if cr.Index != 0 {
		t.Fatalf("sort key index=%d want 0 after the cut", cr.Index)
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
