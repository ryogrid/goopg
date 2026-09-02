package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlannerSettingsReachSubqueryScan is the gate for the propagation gap
// recorded in
// docs/design/not_ralph/planner_refactor_take2/impl/FINDING-planner-settings-not-propagated.md.
//
// PlanWithSettings used to stamp its settings at exactly one resolveContext
// while newResolveContext re-defaulted them everywhere else, so a query whose
// work lives inside a subquery planned under the HARD-WIRED defaults no matter
// what the session said. TPC-H Q9 — whose entire join tree sits inside a
// `from ( select ... ) alias` — was planned at the default work_mem, which made
// that constant load-bearing: dropping it from 512MB to PG's 4MB cost the
// corpus 245.7s -> 314.4s with the session GUC still reading 64MB.
//
// The assertion is deliberately NOT a timing A/B and NOT a plan shape. It reads
// the settings back off the context the subquery's scan was resolved under, so
// it fails for the one reason it exists to catch.
func TestPlannerSettingsReachSubqueryScan(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	// A work_mem far from the default, so a context that silently re-defaulted
	// is distinguishable from one that inherited.
	const wantWorkMem = 7 << 20
	ps := DefaultPlannerSettings()
	ps.WorkMem = wantWorkMem

	stmts, err := parser.Parse("select s.a from (select a, b from t) s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanWithSettings(stmts[0], cat, ps); err != nil {
		t.Fatal(err)
	}

	// The FROM-clause path builds the subquery's resolve context; verify that
	// building one through it carries the settings rather than re-defaulting.
	node, ctx, err := planFromClause(stmts[0].(*parser.SelectStmt), cat, ps)
	if err != nil {
		t.Fatal(err)
	}
	if node == nil || ctx == nil {
		t.Fatal("planFromClause returned no node/context")
	}
	if ctx.settings.WorkMem != wantWorkMem {
		t.Errorf("subquery FROM context planned under WorkMem=%d, want %d — "+
			"settings were re-defaulted on the way down",
			ctx.settings.WorkMem, wantWorkMem)
	}
	if got := ctx.settings.costParams().workMem; got == defaultCostParams().workMem {
		t.Errorf("costParams().workMem fell back to the hard-wired default (%d)", got)
	}
}
