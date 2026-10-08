package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// The self-join rewrite works on a deep copy: the caller's statement (a
// prepared or cached AST, re-planned later) keeps both references and its
// join clause, while the returned statement has one reference left.
func TestRemoveUselessSelfJoinsLeavesInputUntouched(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "sj"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "sj_a_key"}, tbl, []string{"a"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	stmts, err := parser.Parse("SELECT * FROM sj x, sj y WHERE x.a = y.a AND x.b > 1")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*parser.SelectStmt)
	out := removeUselessSelfJoins(s, cat)
	if out == s || len(out.From) != 1 || len(out.Targets) != 2 {
		t.Fatalf("want one reference and two expanded stars, got From=%d Targets=%d", len(out.From), len(out.Targets))
	}
	if len(s.From) != 2 || len(s.Targets) != 1 {
		t.Fatalf("the input statement was modified: From=%d Targets=%d", len(s.From), len(s.Targets))
	}
	conj := splitParserConjuncts(s.Where, nil)
	if l, r, ok := columnEquality(conj[0]); !ok || l.Table != "x" || r.Table != "y" {
		t.Fatalf("the input's join clause was renamed: %#v", conj[0])
	}
}
