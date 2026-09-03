package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor/hashsize"
	"github.com/goopg/goopg/internal/parser"
)

// Planner-context threading — take2 P2-01.
//
// The load-bearing property is that a non-default PlannerSettings handed to
// PlanWithSettings actually ARRIVES at the cost sites. Everything else in this
// item is plumbing; if the value does not arrive, the plumbing is decoration.
//
// design: docs/design/not_ralph/planner_refactor_take2/impl/P2-A-planner-context.md §7

// psProbeCatalog builds a two-table catalog with statistics, so a join is
// planned through the PG-shaped search rather than declined.
func psProbeCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	for _, name := range []string{"pa", "pb"} {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}},
			{Name: "v", Type: catalog.Type{Name: "int4"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{
			RowCount: 10000,
			Analyzed: true,
			Columns: []catalog.ColumnStats{
				{NDistinct: 10000},
				{NDistinct: 100},
			},
		}
	}
	return cat
}

func psPlan(t *testing.T, cat catalog.Catalog, sql string, ps PlannerSettings) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	n, err := PlanWithSettings(stmts[0], cat, ps)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return n
}

// TestPlannerSettingsReachTheJoinSearch asserts the statement's settings arrive
// at the cost site, by giving two arms wildly different page costs and
// requiring the resulting plans to differ.
//
// A cost that reaches nothing cannot change a plan; before P2-01 both arms
// produced identical output because defaultCostParams() was hard-wired.
func TestPlannerSettingsReachTheJoinSearch(t *testing.T) {
	cat := psProbeCatalog(t)
	const sql = "SELECT pa.v, pb.v FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3"

	cheapRandom := DefaultPlannerSettings()
	cheapRandom.RandomPageCost = 0.01
	cheapRandom.SeqPageCost = 100.0

	dearRandom := DefaultPlannerSettings()
	dearRandom.RandomPageCost = 10000.0
	dearRandom.SeqPageCost = 0.001

	a := psPlan(t, cat, sql, cheapRandom)
	b := psPlan(t, cat, sql, dearRandom)

	ca, oka := topPlanCost(a)
	cb, okb := topPlanCost(b)
	if !oka || !okb {
		t.Skipf("neither arm produced a costed root (a=%v b=%v); the statement did not "+
			"reach the path search, so this test cannot observe the seam", oka, okb)
	}
	if ca.TotalCost == cb.TotalCost {
		t.Errorf("both arms costed the plan at %.4f despite seq_page_cost differing by "+
			"1e5 — the settings did not reach the join search", ca.TotalCost)
	}
}

// TestDefaultPlannerSettingsMatchTheHardWiredParams pins the invariant that
// keeps this commit plan-neutral: DefaultPlannerSettings is defined AS what
// defaultCostParams reads, not as a second copy of the same numbers. Two lists
// of the same constants is the duplication the flag-label table was bitten by
// twice.
func TestDefaultPlannerSettingsMatchTheHardWiredParams(t *testing.T) {
	got := DefaultPlannerSettings().costParams()
	want := defaultCostParams()
	if got != want {
		t.Errorf("DefaultPlannerSettings().costParams() = %+v, want %+v", got, want)
	}
}

// TestUnstampedContextGetsDefaultsNotZeroes guards the failure mode that would
// make an unthreaded path catastrophic rather than merely unimproved: a zero
// PlannerSettings prices every page and tuple at 0.0.
func TestUnstampedContextGetsDefaultsNotZeroes(t *testing.T) {
	ctx := newResolveContext(nil, nil, DefaultPlannerSettings())
	if got := ctx.settings; got != DefaultPlannerSettings() {
		t.Errorf("fresh resolveContext settings = %+v, want the defaults", got)
	}
	if ctx.settings.SeqPageCost == 0 || ctx.settings.CPUTupleCost == 0 {
		t.Error("a zero-valued PlannerSettings would price every page and tuple at 0.0")
	}
}

// topPlanCost returns the cost stamped on the plan root, if it carries one.
func topPlanCost(n Node) (PlanCost, bool) {
	if c, ok := n.(PlanCostCarrier); ok {
		return c.PlanCostInfo()
	}
	for _, ch := range legacyDisplayChildren(n) {
		if pc, ok := topPlanCost(ch); ok {
			return pc, true
		}
	}
	return PlanCost{}, false
}

// TestCostGUCsReachTheCostingOnAHashJoin covers the cost GUCs whose effect is
// observable on a plain two-table hash join. It is deliberately NOT the full
// nine: several GUCs can only move a plan that contains a shape this fixture
// does not produce, and a test that stretched one statement to cover all nine
// would be contrived or would pass for the wrong reason.
//
// 09 §5's P2 acceptance row — "every cost GUC demonstrably changes at least one
// plan" — is therefore only PARTLY discharged here. The remainder is recorded
// in TODO under P2-02 with the live evidence gathered against the TPC-H bench
// server, where `SET seq_page_cost = 1000` switched a parallel Hash Join to a
// Merge Join over index scans and `SET work_mem = '64kB'` repriced the same
// hash join from 14835 to 23478.
func TestCostGUCsReachTheCostingOnAHashJoin(t *testing.T) {
	cat := psProbeCatalog(t)
	const sql = "SELECT pa.v, pb.v FROM pa, pb WHERE pa.id = pb.id AND pa.v = 3"

	baseCost, ok := topPlanCost(psPlan(t, cat, sql, DefaultPlannerSettings()))
	if !ok {
		t.Fatal("baseline plan carries no cost; the statement did not reach the path search")
	}

	for _, tc := range []struct {
		name  string
		apply func(*PlannerSettings)
	}{
		{"seq_page_cost", func(p *PlannerSettings) { p.SeqPageCost *= 1000 }},
		{"cpu_tuple_cost", func(p *PlannerSettings) { p.CPUTupleCost *= 1000 }},
		{"cpu_operator_cost", func(p *PlannerSettings) { p.CPUOperatorCost *= 1000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := DefaultPlannerSettings()
			tc.apply(&ps)
			got, ok := topPlanCost(psPlan(t, cat, sql, ps))
			if !ok {
				t.Fatalf("%s: plan carries no cost", tc.name)
			}
			if got.TotalCost == baseCost.TotalCost && got.StartupCost == baseCost.StartupCost {
				t.Errorf("%s: costing unchanged at (%.4f..%.4f) after a 1000x move — "+
					"the GUC does not reach the planner",
					tc.name, got.StartupCost, got.TotalCost)
			}
		})
	}
}

// TestCostGUCConversionIsTotal pins that every field of PlannerSettings is
// carried into costParams. It cannot show that a field CHANGES a plan — that is
// the test above and the TODO note — but it does show that none is silently
// dropped on the way in, which is the failure a per-shape test cannot
// distinguish from "this shape does not use that GUC".
func TestCostGUCConversionIsTotal(t *testing.T) {
	ps := PlannerSettings{
		SeqPageCost: 1.5, RandomPageCost: 2.5,
		CPUTupleCost: 3.5, CPUIndexTupleCost: 4.5, CPUOperatorCost: 5.5,
		ParallelSetupCost: 6.5, ParallelTupleCost: 7.5,
		EffectiveCacheSize: 8.5, WorkMem: 9, HashMemMultiplier: 2.0,
	}
	cp := ps.costParams()
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"seq_page_cost", cp.seqPageCost, ps.SeqPageCost},
		{"random_page_cost", cp.randomPageCost, ps.RandomPageCost},
		{"cpu_tuple_cost", cp.cpuTupleCost, ps.CPUTupleCost},
		{"cpu_index_tuple_cost", cp.cpuIndexTupleCost, ps.CPUIndexTupleCost},
		{"cpu_operator_cost", cp.cpuOperatorCost, ps.CPUOperatorCost},
		{"parallel_setup_cost", cp.parallelSetupCost, ps.ParallelSetupCost},
		{"parallel_tuple_cost", cp.parallelTupleCost, ps.ParallelTupleCost},
		{"effective_cache_size", cp.effectiveCacheSize, ps.EffectiveCacheSize},
		// take2 P2-03: workMem is no longer carried through unchanged — the
		// cost model's budget is work_mem * hash_mem_multiplier
		// (get_hash_memory_limit), so the conversion asserts the DERIVED
		// figure. Asserting equality here would pin the pre-P2-03 behaviour.
		{"work_mem (as the hash budget)", float64(cp.workMem),
			float64(hashsize.HashMemLimit(ps.WorkMem, ps.HashMemMultiplier))},
	} {
		if c.got != c.want {
			t.Errorf("%s dropped in conversion: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestHashMemMultiplierReachesTheBudget pins take2 P2-03. A hash build's
// budget is `work_mem * hash_mem_multiplier` (get_hash_memory_limit,
// nodeHash.c:3622). goopg budgeted `work_mem` alone, so with PG's default
// multiplier of 2.0 every hash table in goopg had HALF the memory PostgreSQL
// would give it — at the aligned work_mem=64MB, a 64MB budget against PG's
// 128MB, which is one reason a build PG keeps in one batch spills here.
func TestHashMemMultiplierReachesTheBudget(t *testing.T) {
	ps := DefaultPlannerSettings()
	if ps.HashMemMultiplier != hashsize.DefaultHashMemMultiplier {
		t.Errorf("default multiplier = %v, want %v",
			ps.HashMemMultiplier, hashsize.DefaultHashMemMultiplier)
	}
	// The planner's budget must be work_mem * multiplier, not work_mem.
	ps.WorkMem = 64 << 20
	ps.HashMemMultiplier = 2.0
	if got, want := ps.costParams().workMem, int64(128<<20); got != want {
		t.Errorf("costParams workMem = %d, want %d (64MB * 2.0)", got, want)
	}
	// A session that raises it gets more, which is the point of the GUC.
	ps.HashMemMultiplier = 4.0
	if got, want := ps.costParams().workMem, int64(256<<20); got != want {
		t.Errorf("at multiplier 4.0 workMem = %d, want %d", got, want)
	}
	// Zero means "use the default", so a zero-valued PlannerSettings still
	// prices hashes sanely rather than at zero bytes.
	ps.HashMemMultiplier = 0
	if got := ps.costParams().workMem; got <= 0 {
		t.Errorf("zero multiplier produced a %d-byte budget; it must fall back "+
			"to the default", got)
	}
}
