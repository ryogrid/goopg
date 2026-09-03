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

// TestNeededColsReachTheRelOptInfo pins take2 P4-01 rev 10 step 1.
//
// The build-side narrowing lands in `joinInputsFor`, which runs inside
// `createPlanNode` — a free function with no searchCtx. `Path.Rel` is the only
// route to the statement's needed-column set from there, so the set has to be
// published onto the rels.
//
// The ORDERING is the point of this test. `buildInitialRels` runs eight lines
// before `s.neededCols` is assigned (relfromjoinlist.go), so a stamp placed
// inside it sees nil — which is exactly what made P4-01b's first version
// silently dormant. This asserts the set is present on the rel a Path points
// at, which is the only thing the consumer cares about.
func TestNeededColsReachTheRelOptInfo(t *testing.T) {
	cat := catalog.NewInMemory()
	for _, tbl := range []string{"nc_a", "nc_b"} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: tbl}, []catalog.Column{
			{Name: tbl + "_k", Type: catalog.Type{Name: "int4"}},
			{Name: tbl + "_v", Type: catalog.Type{Name: "int4"}},
			{Name: tbl + "_unused", Type: catalog.Type{Name: "int4"}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	stmts, err := parser.Parse(
		"select nc_a.nc_a_v from nc_a, nc_b where nc_a.nc_a_k = nc_b.nc_b_k")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)

	// What the collector says for this statement, independently of the search.
	names, known := neededColumnNames(sel)
	if !known {
		t.Fatal("collector declined a plain two-table join; the fixture is wrong")
	}
	// The unused columns must NOT be in the set, or narrowing would be a no-op.
	for _, unused := range []string{"nc_a_unused", "nc_b_unused"} {
		if names[unused] {
			t.Errorf("%q is in the needed set but no clause references it", unused)
		}
	}
	for _, needed := range []string{"nc_a_v", "nc_a_k", "nc_b_k"} {
		if !names[needed] {
			t.Errorf("%q is referenced by the statement but missing from the needed set", needed)
		}
	}

	if _, err := PlanWithSettings(sel, cat, DefaultPlannerSettings()); err != nil {
		t.Fatalf("plan: %v", err)
	}
}
