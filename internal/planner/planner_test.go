package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// pgbenchCatalog seeds a catalog with the four tables pgbench -i
// creates, so plan-side tests don't have to redefine them per case.
func pgbenchCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}},
		{Name: "filler", Type: catalog.Type{Name: "char", Args: []int64{84}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_history"}, []catalog.Column{
		{Name: "tid", Type: catalog.Type{Name: "int4"}},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "aid", Type: catalog.Type{Name: "int4"}},
		{Name: "delta", Type: catalog.Type{Name: "int4"}},
		{Name: "mtime", Type: catalog.Type{Name: "timestamp"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func parseOne(t *testing.T, sql string) parser.Stmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d stmts", sql, len(stmts))
	}
	return stmts[0]
}

// TestPlanPgbenchSelect: pgbench's --select-only canonical query
// plans into Project(Filter(SeqScan)) and resolves `abalance` to the
// right column ordinal.
func TestPlanPgbenchSelect(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT abalance FROM pgbench_accounts WHERE aid = $1")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if len(proj.Targets) != 1 {
		t.Fatalf("targets=%d", len(proj.Targets))
	}
	if cr, ok := proj.Targets[0].(*ColumnRef); !ok || cr.Index != 2 || cr.Name != "abalance" {
		t.Errorf("target=%+v", proj.Targets[0])
	}
	filt, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("Project.Child=%T want *Filter", proj.Child)
	}
	if _, ok := filt.Child.(*SeqScan); !ok {
		t.Fatalf("Filter.Child=%T want *SeqScan", filt.Child)
	}
	pred, ok := filt.Predicate.(*BinaryOp)
	if !ok || pred.Op != "=" {
		t.Fatalf("predicate=%+v", filt.Predicate)
	}
	if cr, ok := pred.Left.(*ColumnRef); !ok || cr.Index != 0 || cr.Name != "aid" {
		t.Errorf("predicate.Left=%+v", pred.Left)
	}
	if pr, ok := pred.Right.(*ParamRef); !ok || pr.Number != 1 {
		t.Errorf("predicate.Right=%+v", pred.Right)
	}
}

// TestPlanSelectStarExpansion verifies SELECT * expands to the full
// column list and the Project's output schema reflects the table.
func TestPlanSelectStarExpansion(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "SELECT * FROM pgbench_accounts"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj := node.(*Project)
	if len(proj.Targets) != 4 {
		t.Errorf("targets=%d want 4", len(proj.Targets))
	}
	if len(proj.Output()) != 4 || proj.Output()[0].Name != "aid" {
		t.Errorf("output=%+v", proj.Output())
	}
}

// TestPlanInsertResolvesColumns: pgbench's INSERT INTO pgbench_history
// (tid, bid, aid, delta, mtime) ... resolves all five names and
// builds a Values feeding an Insert.
func TestPlanInsertResolvesColumns(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, $5)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := node.(*Insert)
	if !ok {
		t.Fatalf("got %T", node)
	}
	if len(ins.ColumnIndex) != 5 {
		t.Fatalf("colIndex=%+v", ins.ColumnIndex)
	}
	for i, want := range []int{0, 1, 2, 3, 4} {
		if ins.ColumnIndex[i] != want {
			t.Errorf("colIndex[%d]=%d want %d", i, ins.ColumnIndex[i], want)
		}
	}
	values, ok := ins.Source.(*Values)
	if !ok {
		t.Fatalf("ins.Source=%T", ins.Source)
	}
	if len(values.Rows) != 1 || len(values.Rows[0]) != 5 {
		t.Fatalf("values shape=%v", values.Rows)
	}
}

// TestPlanUpdate: pgbench's abalance UPDATE plans into
// Update(Filter(SeqScan)) with Set[2] populated.
func TestPlanUpdate(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2"), cat)
	if err != nil {
		t.Fatal(err)
	}
	upd, ok := node.(*Update)
	if !ok {
		t.Fatalf("got %T", node)
	}
	if len(upd.Set) != 4 {
		t.Fatalf("Set len=%d want 4", len(upd.Set))
	}
	if upd.Set[0] != nil || upd.Set[1] != nil || upd.Set[3] != nil {
		t.Errorf("non-target columns should be nil: %+v", upd.Set)
	}
	if upd.Set[2] == nil {
		t.Fatal("Set[abalance] should be populated")
	}
	if _, ok := upd.Child.(*Filter); !ok {
		t.Fatalf("upd.Child=%T", upd.Child)
	}
}

// TestPlanDelete: simple DELETE plans into Delete(Filter(SeqScan)).
func TestPlanDelete(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "DELETE FROM pgbench_history WHERE aid = $1"), cat)
	if err != nil {
		t.Fatal(err)
	}
	del := node.(*Delete)
	if _, ok := del.Child.(*Filter); !ok {
		t.Errorf("del.Child=%T", del.Child)
	}
}

// TestPlanDDLAndUtilityPassThrough: DDL/utility statements wrap
// without decomposing.
func TestPlanDDLAndUtilityPassThrough(t *testing.T) {
	cat := pgbenchCatalog(t)
	if n, err := Plan(parseOne(t, "BEGIN"), cat); err != nil || n.(*Transaction).Verb != TxBegin {
		t.Errorf("BEGIN: %v / %+v", err, n)
	}
	if n, err := Plan(parseOne(t, "DROP TABLE pgbench_history"), cat); err != nil {
		t.Errorf("DROP: %v / %T", err, n)
	} else if _, ok := n.(*DDL); !ok {
		t.Errorf("DROP got %T", n)
	}
	if n, err := Plan(parseOne(t, "VACUUM ANALYZE"), cat); err != nil {
		t.Errorf("VACUUM: %v", err)
	} else if _, ok := n.(*Utility); !ok {
		t.Errorf("VACUUM got %T", n)
	}
}

// TestPlanResolutionErrors pins SQLSTATE-aligned codes for the
// canonical resolution failures.
func TestPlanResolutionErrors(t *testing.T) {
	cat := pgbenchCatalog(t)
	cases := []struct {
		sql  string
		code string
	}{
		{"SELECT * FROM nope", "42P01"},                                  // undefined_table
		{"SELECT bogus FROM pgbench_accounts", "42703"},                  // undefined_column
		{"INSERT INTO pgbench_history (nope) VALUES (1)", "42703"},       // undefined_column
		{"UPDATE pgbench_accounts SET nope = 1 WHERE aid = $1", "42703"}, // undefined_column
		{"INSERT INTO pgbench_history VALUES (1, 2, 3)", "42601"},        // arity mismatch
	}
	for _, c := range cases {
		_, err := Plan(parseOne(t, c.sql), cat)
		if err == nil {
			t.Errorf("Plan(%q) expected error", c.sql)
			continue
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Errorf("Plan(%q) err type=%T", c.sql, err)
			continue
		}
		if pe.Code != c.code {
			t.Errorf("Plan(%q) code=%s want %s", c.sql, pe.Code, c.code)
		}
	}
}
