package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func TestIndexScanEndToEndConstantKey(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := runDDL(t, ctx, "CREATE INDEX idx_items_id ON items (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}

	plan := planOne(t, "SELECT id, label FROM items WHERE id = 2", cat)
	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	if _, ok := proj.Child.(*planner.IndexScan); !ok {
		t.Fatalf("child=%T want *planner.IndexScan", proj.Child)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 2 || rows[0][1].StringValue() != "beta" {
		t.Fatalf("row=%+v want [2,beta]", rows[0])
	}
}

func TestIndexScanEndToEndParamKey(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := runDDL(t, ctx, "CREATE INDEX idx_items_id ON items (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}

	plan := planOne(t, "SELECT id, label FROM items WHERE id = $1", cat)
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Params = []Datum{{Kind: KindInt, Int: 3}}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 3 || rows[0][1].StringValue() != "gamma" {
		t.Fatalf("row=%+v want [3,gamma]", rows[0])
	}
}
