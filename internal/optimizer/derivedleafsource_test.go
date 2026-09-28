package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0029 — an unlabelled derived leaf publishes its columns at
// SourceTableIdx 0. Before, its root Project leaked the INNER scope's ids
// (restarted at 1 per query level); an outer `ss1.x` reference (outer binding
// id 1) then matched a sibling leg's `x` that happened to carry inner id 1,
// and the by-(Name, SourceTableIdx) re-resolver moved it — regress join.sql's
// "variable-free join alias" query panicked in
// assertSearchedTreeNeedsNoReconcile.
func TestDerivedLeafVariableFreeJoinAliasPlans(t *testing.T) {
	stmts, err := parser.Parse("SELECT * FROM a a0 LEFT JOIN ((SELECT ax, ay + 0 AS x FROM a a1) s1 " +
		"LEFT JOIN (SELECT cx, cy AS x FROM c) s2 USING (x)) s0 ON (a0.ax = s0.ax) ORDER BY a0.ax, x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("planning panicked: %v", r)
		}
	}()
	if _, err := Plan(stmts[0], scopeTestCatalog(t)); err != nil {
		t.Fatalf("plan: %v", err)
	}
}

func TestProjectWithUnknownSources(t *testing.T) {
	p := &Project{schema: Schema{{Name: "f1", SourceTableIdx: 1}, {Name: "x"}}}
	got, ok := projectWithUnknownSources(p).(*Project)
	if !ok || got == p {
		t.Fatal("a Project publishing inner ids must be copied, not mutated")
	}
	for _, c := range got.Output() {
		if c.SourceTableIdx != 0 {
			t.Fatalf("column %s still carries inner id %d", c.Name, c.SourceTableIdx)
		}
	}
	if p.schema[0].SourceTableIdx != 1 {
		t.Fatal("the original (possibly shared) root was mutated")
	}
	clean := &Project{schema: Schema{{Name: "x"}}}
	if projectWithUnknownSources(clean) != Node(clean) {
		t.Fatal("a root with no inner ids needs no copy")
	}
}
