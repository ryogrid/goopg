package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestKeyMatchesTableColumnExpression pins the expression half of the
// partitions_are_ordered arm's key matching (M0148-0001c3a5 (a) slice 2):
// an ORDER BY key `abs(b)`, rewritten onto a member's columns, matches a
// partition key / index column whose stored expression is `abs(b)` resolved
// against that member, and not `abs(a)` or the plain column b. The arm's
// end-to-end use waits on expression index keys holding NULL-keyed entries
// (indexUnboundKeysNotNull refuses expression columns today).
func TestKeyMatchesTableColumnExpression(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl := &catalog.Table{Name: "m1", Columns: []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}}
	parse := func(src string) parser.Expr {
		st, err := parser.Parse("create index on m1 (" + src + ")")
		if err != nil {
			t.Fatal(err)
		}
		return st[0].(*parser.CreateIndexStmt).ColExprs[0]
	}
	key := &FuncCall{Name: "abs", Args: []Expr{&ColumnRef{Index: 1, Name: "b"}}}
	absB, absA := parse("abs(b)"), parse("abs(a)")
	if !keyMatchesTableColumn(cat, tbl, key, "", &absB) {
		t.Error("abs(b) key does not match the abs(b) expression column")
	}
	if keyMatchesTableColumn(cat, tbl, key, "", &absA) {
		t.Error("abs(b) key matches the abs(a) expression column")
	}
	if keyMatchesTableColumn(cat, tbl, key, "b", nil) {
		t.Error("abs(b) key matches the plain column b")
	}
	if !keyMatchesTableColumn(cat, tbl, &ColumnRef{Index: 1, Name: "b"}, "b", nil) {
		t.Error("column b key does not match the plain column b")
	}
	if keyMatchesTableColumn(cat, tbl, &ColumnRef{Index: 0, Name: "a"}, "b", nil) {
		t.Error("column a key matches the plain column b")
	}
}
