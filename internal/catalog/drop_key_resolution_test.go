package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDropResolvesRestartKeySpelling: a relation registered under the
// "public.<name>" key (a restart's pg_class reload) is dropped / renamed by
// its bare name, and a bare-keyed one (a live session) by "public.<name>" —
// the same resolution LookupTable / lookupIndexLocked apply (M0148-0001c3b:
// after a restart DROP VIEW / DROP INDEX failed "does not exist" while the
// relation was still listed).
func TestDropResolvesRestartKeySpelling(t *testing.T) {
	c := NewInMemory()
	view := func(schema, name string) *Table {
		return &Table{Schema: schema, Name: name, Columns: []Column{{Name: "a", Type: Type{Name: "int4"}}}, View: &parser.SelectStmt{}}
	}
	c.RegisterTable(view("public", "rv"))
	if _, ok := c.LookupTable(parser.ObjectName{Name: "rv"}); !ok {
		t.Fatal("restart-keyed view not found by bare name")
	}
	if err := c.DropView(parser.ObjectName{Name: "rv"}, false); err != nil {
		t.Fatalf("DropView by bare name: %v", err)
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "rv"}); ok {
		t.Fatal("view still present after DropView")
	}

	c.RegisterTable(view("", "lv"))
	if err := c.DropView(parser.ObjectName{Schema: "public", Name: "lv"}, false); err != nil {
		t.Fatalf("DropView by qualified name: %v", err)
	}

	tbl := &Table{Schema: "public", Name: "rt", Columns: []Column{{Name: "a", Type: Type{Name: "int4"}}}}
	c.RegisterTable(tbl)
	if _, err := c.CreateIndex(parser.ObjectName{Schema: "public", Name: "rt_a"}, tbl, []string{"a"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	if err := c.DropIndex(parser.ObjectName{Name: "rt_a"}); err != nil {
		t.Fatalf("DropIndex by bare name: %v", err)
	}
	if err := c.RenameTable(parser.ObjectName{Name: "rt"}, parser.ObjectName{Name: "rt2"}); err != nil {
		t.Fatalf("RenameTable by bare name: %v", err)
	}
	if err := c.DropTable(parser.ObjectName{Name: "rt2"}); err != nil {
		t.Fatalf("DropTable after rename: %v", err)
	}
}
