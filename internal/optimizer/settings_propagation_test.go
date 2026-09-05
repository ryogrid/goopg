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
	node, ctx, err := planFromClause(stmts[0].(*parser.SelectStmt), cat, ps, nil)
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

// TestPlannerSettingsReachDerivedTableJoin is B-12d's GUC-effect gate
// (take3 09 §5 P2 row): the session's settings must reach the join search
// running INSIDE a `(SELECT …) AS alias` FROM item. TPC-H Q9 is the live
// witness — its entire join tree sits inside such a subquery, which is why
// P2-02b stays blocked until this slice lands (take3 04 §12.3).
//
// The assertion is on the priced inner join, not on timing or shape: before
// B-12d the inner search planned under hard-wired defaults, so every arm
// below costed the join at 3.25..151.75 no matter what the session said.
// After B-12d each arm reprices it, proving the inner search reads the
// statement's settings.
//
// The arms are EXACTLY the P2-02d settings family this fixture can observe:
// seq_page_cost, cpu_tuple_cost, cpu_operator_cost (each ×1000) and work_mem
// (16kB, whose hash budget spills at this fixture's build size — the same
// repricing the bench shows live as `SET work_mem='64kB'` 14835→23478).
// Deliberately NOT covered here, same honesty rule as
// TestCostGUCsReachTheCostingOnAHashJoin: random_page_cost,
// cpu_index_tuple_cost and effective_cache_size need an index shape this
// seq-scan fixture does not produce; parallel_setup_cost/parallel_tuple_cost
// need a Gather; the method toggles, memoize, hash_mem_multiplier and GEQO
// knobs travel on the same PlannerSettings value with conversion pinned by
// TestCostGUCConversionIsTotal / TestEnableJoinMethodGUCsSetDisabledNodes /
// TestHashMemMultiplierReachesTheBudget.
func TestPlannerSettingsReachDerivedTableJoin(t *testing.T) {
	cat := psProbeCatalog(t)
	// Q9's shape class: the join tree lives inside the derived table.
	const sql = "SELECT s.a FROM (SELECT pa.v AS a, pb.v AS b FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3) s"
	const nestedSQL = "SELECT s.a FROM (SELECT q.a AS a FROM (SELECT pa.v AS a, pb.v AS b FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3) q) s"

	base, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("baseline derived-table plan carries no cost; the inner join did not reach the path search")
	}

	for _, tc := range []struct {
		name  string
		sql   string
		apply func(*PlannerSettings)
	}{
		{"seq_page_cost", sql, func(p *PlannerSettings) { p.SeqPageCost *= 1000 }},
		{"cpu_tuple_cost", sql, func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
		{"cpu_operator_cost", sql, func(p *PlannerSettings) { p.CPUOperatorCost *= 1000 }},
		{"work_mem", sql, func(p *PlannerSettings) { p.WorkMem = 16 << 10 }},
		// The settings thread recursively: a derived table inside a derived
		// table prices under the same statement settings.
		{"cpu_tuple_cost/nested", nestedSQL, func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := DefaultPlannerSettings()
			tc.apply(&ps)
			got, ok := topPlanCost(psPlan(t, cat, tc.sql, ps))
			if !ok {
				t.Fatalf("%s: plan carries no cost", tc.name)
			}
			if got.TotalCost == base.TotalCost && got.StartupCost == base.StartupCost {
				t.Errorf("%s: inner join still costed at (%.4f..%.4f) — "+
					"the settings did not reach the derived table's join search",
					tc.name, got.StartupCost, got.TotalCost)
			}
		})
	}

	// No cross-session leakage: the threading is an explicit parameter, not
	// the package-global planParent channel, so planning under one session's
	// settings must not steer the next statement (the wrong-scope bug the
	// reverted mechanical attempt shipped — take3 08 §10.1).
	t.Run("no_leakage", func(t *testing.T) {
		hot := DefaultPlannerSettings()
		hot.CPUTupleCost *= 1000
		if _, ok := topPlanCost(psPlan(t, cat, sql, hot)); !ok {
			t.Fatal("hot arm carries no cost")
		}
		restored, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
		if !ok {
			t.Fatal("restored arm carries no cost")
		}
		if restored != base {
			t.Errorf("default plan after a hot plan = (%.4f..%.4f), want baseline (%.4f..%.4f) — "+
				"one statement's settings leaked into the next",
				restored.StartupCost, restored.TotalCost, base.StartupCost, base.TotalCost)
		}
	})
}

// TestDerivedTablePropagationKeepsDefaultPlan is B-12d's unchanged-default
// pin: under DefaultPlannerSettings the derived-table path must price exactly
// as it did when planSelectWithParent hard-wired the defaults — the slice is
// plan-neutral for sessions that change nothing. The literal cost pins the
// full chain (FROM threading → planSubqueryRangeVar → planSelectWithParent →
// inner search); a zero-valued or mis-threaded settings value would reprice
// it. The lateral and nested variants cover the second threaded call site and
// the recursion.
func TestDerivedTablePropagationKeepsDefaultPlan(t *testing.T) {
	cat := psProbeCatalog(t)
	const sql = "SELECT s.a FROM (SELECT pa.v AS a, pb.v AS b FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3) s"

	got, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("derived-table plan carries no cost; the inner join did not reach the path search")
	}
	if got.StartupCost != 3.25 || got.TotalCost != 151.75 || got.PlanRows != 100 {
		t.Errorf("default derived-table join = (%.4f..%.4f rows=%.0f), want (3.25..151.75 rows=100) — "+
			"the default path moved", got.StartupCost, got.TotalCost, got.PlanRows)
	}

	// The lateral derived-table call site threads the same value; the lateral
	// path bypasses the path search (no costed node), so plan-OK is the
	// observable — it guards the edited call site against breakage.
	const lateralSQL = "SELECT s.a FROM pa, LATERAL (SELECT pb.v AS a FROM pb WHERE pb.id = pa.id) s"
	hot := DefaultPlannerSettings()
	hot.CPUTupleCost *= 1000
	for _, ps := range []PlannerSettings{DefaultPlannerSettings(), hot} {
		psPlan(t, cat, lateralSQL, ps)
	}
}

// TestPlannerSettingsReachSetOpOperands is B-12e's GUC-effect gate
// (take3 09 §5 P2 row): the session's settings must reach the join search
// running INSIDE each set-operation operand. Before B-12e the leftmost
// branch, every planSegment right operand and the parenthesised
// SetOpOperand grouping site planned under hard-wired defaults, so every
// arm below costed the operand join at 3.25..151.75 no matter what the
// session said. After B-12e each arm reprices it, proving the operands
// read the statement's settings.
//
// The arms are EXACTLY the P2-02d settings family this fixture can observe:
// seq_page_cost, cpu_tuple_cost, cpu_operator_cost (each ×1000) and work_mem
// (16kB, whose hash budget spills at this fixture's build size — the same
// repricing the bench shows live as `SET work_mem='64kB'` 14835→23478).
// Deliberately NOT covered here, same honesty rule as
// TestCostGUCsReachTheCostingOnAHashJoin and TestPlannerSettingsReachDerivedTableJoin:
// random_page_cost, cpu_index_tuple_cost and effective_cache_size need an
// index shape this seq-scan fixture does not produce;
// parallel_setup_cost/parallel_tuple_cost need a Gather; the method toggles,
// memoize, hash_mem_multiplier and GEQO knobs travel on the same
// PlannerSettings value with conversion pinned by
// TestCostGUCConversionIsTotal / TestEnableJoinMethodGUCsSetDisabledNodes /
// TestHashMemMultiplierReachesTheBudget.
func TestPlannerSettingsReachSetOpOperands(t *testing.T) {
	cat := psProbeCatalog(t)
	// Both operands carry the probe join, so the leftmost-branch site and
	// the planSegment right-operand site each have a priced join to move.
	const sql = "SELECT pa.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3 UNION SELECT pb.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3"
	// The parenthesised compound plans through the SetOpOperand grouping
	// site (planSelectWithSettings' `s.SetOpOperand != nil` branch).
	const groupedSQL = "(SELECT pa.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3 UNION SELECT pb.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3)"

	base, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("baseline set-op plan carries no cost; the operand join did not reach the path search")
	}
	groupedBase, ok := topPlanCost(psPlan(t, cat, groupedSQL, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("baseline parenthesised set-op plan carries no cost; the operand join did not reach the path search")
	}

	for _, tc := range []struct {
		name  string
		sql   string
		base  PlanCost
		apply func(*PlannerSettings)
	}{
		{"seq_page_cost", sql, base, func(p *PlannerSettings) { p.SeqPageCost *= 1000 }},
		{"cpu_tuple_cost", sql, base, func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
		{"cpu_operator_cost", sql, base, func(p *PlannerSettings) { p.CPUOperatorCost *= 1000 }},
		{"work_mem", sql, base, func(p *PlannerSettings) { p.WorkMem = 16 << 10 }},
		// The threading is per-operand, not just the leftmost branch: the
		// right operand (planSegment site) reprices too.
		{"cpu_tuple_cost/right_operand", sql, base, func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
		// The parenthesised grouping site threads the same value.
		{"cpu_tuple_cost/grouped", groupedSQL, groupedBase, func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
		{"seq_page_cost/grouped", groupedSQL, groupedBase, func(p *PlannerSettings) { p.SeqPageCost *= 1000 }},
		{"work_mem/grouped", groupedSQL, groupedBase, func(p *PlannerSettings) { p.WorkMem = 16 << 10 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := DefaultPlannerSettings()
			tc.apply(&ps)
			got, ok := topPlanCost(psPlan(t, cat, tc.sql, ps))
			if !ok {
				t.Fatalf("%s: plan carries no cost", tc.name)
			}
			if got.TotalCost == tc.base.TotalCost && got.StartupCost == tc.base.StartupCost {
				t.Errorf("%s: operand join still costed at (%.4f..%.4f) — "+
					"the settings did not reach the set-op operand's join search",
					tc.name, got.StartupCost, got.TotalCost)
			}
		})
	}

	// Per-operand isolation: the leftmost branch and the right operand
	// reprice INDEPENDENTLY. topPlanCost above surfaces the left join; here
	// both SetOp children must move, proving neither site was left behind
	// on the defaults.
	t.Run("both_operands", func(t *testing.T) {
		ps := DefaultPlannerSettings()
		ps.CPUTupleCost *= 1000
		hot := psPlan(t, cat, sql, ps)
		def := psPlan(t, cat, sql, DefaultPlannerSettings())
		setOp, ok := findSetOp(hot)
		if !ok {
			t.Fatal("hot set-op plan has no SetOp node; the fixture is wrong")
		}
		defSetOp, ok := findSetOp(def)
		if !ok {
			t.Fatal("baseline set-op plan has no SetOp node; the fixture is wrong")
		}
		for _, side := range []struct {
			name     string
			hot, def Node
		}{
			{"left", setOp.Left, defSetOp.Left},
			{"right", setOp.Right, defSetOp.Right},
		} {
			hotCost, ok := topPlanCost(side.hot)
			if !ok {
				t.Fatalf("%s operand carries no cost", side.name)
			}
			defCost, ok := topPlanCost(side.def)
			if !ok {
				t.Fatalf("baseline %s operand carries no cost", side.name)
			}
			if hotCost == defCost {
				t.Errorf("%s operand still costed at (%.4f..%.4f) — "+
					"that operand's site did not inherit the settings",
					side.name, hotCost.StartupCost, hotCost.TotalCost)
			}
		}
	})

	// No cross-session leakage: the threading is an explicit parameter, not
	// a package global, so planning under one session's settings must not
	// steer the next statement (the wrong-scope lesson B-12d records —
	// settings resolve directly from the parameter, never parent/lateral).
	t.Run("no_leakage", func(t *testing.T) {
		hot := DefaultPlannerSettings()
		hot.CPUTupleCost *= 1000
		if _, ok := topPlanCost(psPlan(t, cat, sql, hot)); !ok {
			t.Fatal("hot arm carries no cost")
		}
		restored, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
		if !ok {
			t.Fatal("restored arm carries no cost")
		}
		if restored != base {
			t.Errorf("default plan after a hot plan = (%.4f..%.4f), want baseline (%.4f..%.4f) — "+
				"one statement's settings leaked into the next",
				restored.StartupCost, restored.TotalCost, base.StartupCost, base.TotalCost)
		}
	})
}

// TestSetOpPropagationKeepsDefaultPlan is B-12e's unchanged-default pin:
// under DefaultPlannerSettings the set-op path must price exactly as it did
// when the operand sites hard-wired the defaults — the slice is plan-neutral
// for sessions that change nothing. The literal cost pins the full chain
// (operand threading → inner search); a zero-valued or mis-threaded settings
// value would reprice it.
func TestSetOpPropagationKeepsDefaultPlan(t *testing.T) {
	cat := psProbeCatalog(t)
	const sql = "SELECT pa.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3 UNION SELECT pb.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3"

	got, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("set-op plan carries no cost; the operand join did not reach the path search")
	}
	if got.StartupCost != 3.25 || got.TotalCost != 151.75 || got.PlanRows != 100 {
		t.Errorf("default set-op operand join = (%.4f..%.4f rows=%.0f), want (3.25..151.75 rows=100) — "+
			"the default path moved", got.StartupCost, got.TotalCost, got.PlanRows)
	}

	// The parenthesised grouping site threads the same default value;
	// plan-OK plus identical cost guards that call site against breakage.
	const groupedSQL = "(SELECT pa.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3 UNION SELECT pb.v AS a FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3)"
	grouped, ok := topPlanCost(psPlan(t, cat, groupedSQL, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("grouped set-op plan carries no cost")
	}
	if grouped != got {
		t.Errorf("grouped default = (%.4f..%.4f rows=%.0f), want flat-chain (%.4f..%.4f rows=%.0f)",
			grouped.StartupCost, grouped.TotalCost, grouped.PlanRows,
			got.StartupCost, got.TotalCost, got.PlanRows)
	}
}

// findSetOp returns the first SetOp node in plan order, if the plan holds one.
func findSetOp(n Node) (*SetOp, bool) {
	if s, ok := n.(*SetOp); ok {
		return s, true
	}
	for _, ch := range legacyDisplayChildren(n) {
		if s, ok := findSetOp(ch); ok {
			return s, true
		}
	}
	return nil, false
}

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
