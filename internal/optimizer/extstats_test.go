package optimizer

// B-05b tests: planner consumption of functional-dependency extended
// statistics (dependencies_clauselist_selectivity / choose_best_statistics
// ports in extstats.go).
//
// Oracle references:
//   - README formula (postgres/src/backend/statistics/README.dependencies,
//     "Clause reduction"): P(a,b) = P(a) * (d + (1-d) * P(b)) for (a => b).
//   - Code formula (dependencies.c, clauselist_apply_dependencies):
//     P(a,b) = f * Min(P(a),P(b)) + (1-f) * P(a) * P(b), applied backwards
//     as conditionals over the greedy strongest-first chain.
//   - choose_best_statistics (extended_stats.c:1206): most covered
//     attributes wins, ties to fewest keys (seeded best=2 / bestKeys=9).

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// extStatsTable builds a SeqScan child over a table with the given OID,
// columns and per-position column stats (nil entries allowed only by passing
// a shorter slice — positions past len(stats) get no statistics).
func extStatsTable(t *testing.T, oid uint32, cols []catalog.Column, stats []catalog.ColumnStats) *SeqScan {
	t.Helper()
	tbl := &catalog.Table{
		Schema:  "public",
		Name:    "ext_t",
		OID:     oid,
		Columns: cols,
	}
	if stats != nil {
		tbl.Stats = &catalog.TableStats{RowCount: 1000, Columns: stats}
	}
	return &SeqScan{Table: tbl}
}

func extEq(col string, idx int, v int64) *BinaryOp {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: idx, Name: col, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: v},
	}
}

func extStrEq(col string, idx int, v string) *BinaryOp {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: idx, Name: col, Type: catalog.Type{Name: "text"}},
		Right: &StringConst{Value: v},
	}
}

func intCol(name string, ord int) catalog.Column {
	return catalog.Column{Name: name, Type: catalog.Type{Name: "int4"}, Ordinal: ord}
}

func registerExtDeps(t *testing.T, tableOID uint32, obj PlannerExtStatsObject) {
	t.Helper()
	t.Cleanup(ClearPlannerExtStats)
	RegisterPlannerExtStats(tableOID, obj)
}

func closeEnough(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestExtStatsREADMEBranch covers the s1 <= s2 branch against the README's
// formula directly: P(a,b) = P(a) * (d + (1-d) * P(b)) for (a => b).
// a has no stats (0.005), b hits MCV at 0.8, d = 0.5:
// 0.005 * (0.5 + 0.5 * 0.8) = 0.0045.
func TestExtStatsREADMEBranch(t *testing.T) {
	scan := extStatsTable(t, 7001,
		[]catalog.Column{intCol("a", 0), {Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 1}},
		[]catalog.ColumnStats{
			{},
			{NDistinct: 5, NullFrac: 0, MCV: []catalog.MCVEntry{{Value: "X", Frequency: 0.8}}},
		})
	registerExtDeps(t, 7001, PlannerExtStatsObject{
		StatsOID: 9001, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 0.5, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extEq("a", 0, 1), extStrEq("b", 1, "X")}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "README-branch selectivity", sel, 0.0045)
	if len(estimated) != 2 || !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsMinBranch covers s1 > s2: P(a,b) = f*Min + (1-f)*prod.
// a hits MCV at 0.8, b is 0.005, d = 0.5:
// 0.5*0.005 + 0.5*0.004 = 0.0045 — strictly between product (0.004) and
// min (0.005). Attenuation raises the estimate; it never lowers it.
func TestExtStatsMinBranch(t *testing.T) {
	scan := extStatsTable(t, 7002,
		[]catalog.Column{{Name: "a", Type: catalog.Type{Name: "text"}, Ordinal: 0}, intCol("b", 1)},
		[]catalog.ColumnStats{
			{NDistinct: 5, NullFrac: 0, MCV: []catalog.MCVEntry{{Value: "X", Frequency: 0.8}}},
			{},
		})
	registerExtDeps(t, 7002, PlannerExtStatsObject{
		StatsOID: 9002, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 0.5, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extStrEq("a", 0, "X"), extEq("b", 1, 2)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "min-branch selectivity", sel, 0.0045)
	prod := 0.8 * 0.005
	if !(sel > prod && sel < 0.005) {
		t.Errorf("selectivity %v not strictly between product %v and min 0.005", sel, prod)
	}
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsFullDependencyEqualsMin: degree 1.0 collapses to Min — the
// headline attenuation number (0.005 vs the naive product 2.5e-5, x200).
func TestExtStatsFullDependencyEqualsMin(t *testing.T) {
	scan := extStatsTable(t, 7003,
		[]catalog.Column{intCol("a", 0), intCol("b", 1)}, nil)
	registerExtDeps(t, 7003, PlannerExtStatsObject{
		StatsOID: 9003, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extEq("a", 0, 1), extEq("b", 1, 2)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "full-dependency selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
	// Direction check: independence assumes 2.5e-5.
	if naive := 0.005 * 0.005; sel <= naive {
		t.Errorf("selectivity %v should attenuate (exceed) the naive product %v", sel, naive)
	}
}

// TestExtStatsChainConditional: (a,b=>c, f=1) over three 0.005 clauses.
// Greedy order takes the width-3 dependency first (most attributes), then
// nothing else matches; backwards: P(c|a,b) = 1, so the answer is P(a)*P(b).
func TestExtStatsChainConditional(t *testing.T) {
	scan := extStatsTable(t, 7004,
		[]catalog.Column{intCol("a", 0), intCol("b", 1), intCol("c", 2)}, nil)
	registerExtDeps(t, 7004, PlannerExtStatsObject{
		StatsOID: 9004, Keys: []int16{1, 2, 3},
		Deps: []PlannerExtDependency{
			{Degree: 1.0, Attrs: []int16{1, 2, 3}},
			{Degree: 1.0, Attrs: []int16{1, 2}},
		},
	})
	conj := []Expr{extEq("a", 0, 1), extEq("b", 1, 2), extEq("c", 2, 3)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	// P(a,b) = min = 0.005 via (a=>b); P(c|a,b) = 1 via (a,b=>c).
	closeEnough(t, "chain selectivity", sel, 0.005)
	for i, e := range estimated {
		if !e {
			t.Errorf("estimated[%d] = false, want all true (%v)", i, estimated)
		}
	}
}

// TestExtStatsEstimatedClausesConsumption: the estimatedclauses contract.
// a,b are functionally dependent (f=1); c is independent. The estimator
// consumes exactly {a,b}; every base path must then price ONLY c on top.
func TestExtStatsEstimatedClausesConsumption(t *testing.T) {
	scan := extStatsTable(t, 7005,
		[]catalog.Column{intCol("a", 0), intCol("b", 1), intCol("c", 2)}, nil)
	registerExtDeps(t, 7005, PlannerExtStatsObject{
		StatsOID: 9005, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extEq("a", 0, 1), extEq("b", 1, 2), extEq("c", 2, 3)}

	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "consumed selectivity", sel, 0.005)
	want := []bool{true, true, false}
	for i := range want {
		if estimated[i] != want[i] {
			t.Fatalf("estimated = %v, want %v", estimated, want)
		}
	}

	// conjunctionSelectivity (goopg's clauselist_selectivity): ext for a,b
	// times the base estimate for c. Naive product of all three would be
	// 1.25e-7; the attenuated answer is 0.005 * 0.005 = 2.5e-5.
	closeEnough(t, "conjunctionSelectivity", conjunctionSelectivity(conj, scan), 2.5e-5)

	// filterSelectivity through a Filter node: same number.
	f := &Filter{Child: scan, Predicate: &BinaryOp{Op: parser.OpAnd,
		Left: &BinaryOp{Op: parser.OpAnd, Left: conj[0], Right: conj[1]}, Right: conj[2]}}
	closeEnough(t, "filterSelectivity", filterSelectivity(f), 2.5e-5)

	// WithSource twin: same value; unreliable (no stats anywhere).
	est, _ := dependenciesClauselistSelectivityWithSource(conj, scan)
	closeEnough(t, "WithSource value", est.value, 0.005)
	if est.reliable {
		t.Errorf("WithSource reliable = true, want false (fallback-driven clauses)")
	}
	andPred := &BinaryOp{Op: parser.OpAnd,
		Left: &BinaryOp{Op: parser.OpAnd, Left: conj[0], Right: conj[1]}, Right: conj[2]}
	full := clauseSelectivityWithSource(andPred, scan)
	closeEnough(t, "WithSource AND value", full.value, 2.5e-5)
	if full.reliable {
		t.Errorf("WithSource AND reliable = true, want false")
	}
}

// TestExtStatsReliableWhenMeasured: with MCV stats behind every consumed
// clause the twin reports reliable (so applyLocalFilterSelectivity trusts
// the attenuated answer).
func TestExtStatsReliableWhenMeasured(t *testing.T) {
	mcv := func(v string, f float64) catalog.ColumnStats {
		return catalog.ColumnStats{NDistinct: 4, NullFrac: 0,
			MCV: []catalog.MCVEntry{{Value: v, Frequency: f}}}
	}
	scan := extStatsTable(t, 7006,
		[]catalog.Column{
			{Name: "a", Type: catalog.Type{Name: "text"}, Ordinal: 0},
			{Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		},
		[]catalog.ColumnStats{mcv("X", 0.25), mcv("Y", 0.5)})
	registerExtDeps(t, 7006, PlannerExtStatsObject{
		StatsOID: 9006, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extStrEq("a", 0, "X"), extStrEq("b", 1, "Y")}
	est, estimated := dependenciesClauselistSelectivityWithSource(conj, scan)
	// f=1: min(0.25, 0.5) = 0.25.
	closeEnough(t, "measured selectivity", est.value, 0.25)
	if !est.reliable {
		t.Errorf("WithSource reliable = false, want true (all clauses stat-driven)")
	}
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsChooseBest: the choose_best_statistics port in isolation.
// Most covered attributes wins; ties go to fewest keys; the 2-cover wins
// through the tie-break against the (2, 9) seed; single covers never win.
func TestExtStatsChooseBest(t *testing.T) {
	objs := []PlannerExtStatsObject{
		{StatsOID: 1, Keys: []int16{1, 2, 3}},
		{StatsOID: 2, Keys: []int16{1, 2}},
		{StatsOID: 3, Keys: []int16{4, 5}},
	}
	set := func(attnums ...int16) map[int16]bool {
		m := map[int16]bool{}
		for _, a := range attnums {
			m[a] = true
		}
		return m
	}
	if got := chooseBestPlannerExtStats(objs, []map[int16]bool{set(1), set(2), set(3)}); got != 0 {
		t.Errorf("3-cover: best = %d, want 0 (most attributes)", got)
	}
	if got := chooseBestPlannerExtStats(objs, []map[int16]bool{set(1), set(2)}); got != 1 {
		t.Errorf("2-cover tie: best = %d, want 1 (fewest keys)", got)
	}
	if got := chooseBestPlannerExtStats(objs, []map[int16]bool{set(1)}); got != -1 {
		t.Errorf("1-cover: best = %d, want -1", got)
	}
	if got := chooseBestPlannerExtStats(objs, []map[int16]bool{set(1), set(9)}); got != -1 {
		t.Errorf("no 2-cover: best = %d, want -1", got)
	}
	if got := chooseBestPlannerExtStats(objs, []map[int16]bool{set(4), set(5)}); got != 2 {
		t.Errorf("disjoint 2-cover: best = %d, want 2", got)
	}
}

// TestExtStatsBestPickVsProduct: two overlapping objects; the claim loop
// picks the widest cover first. Wide object (a,b=>c, f=1) claims all three
// clauses: P(a,b) = 2.5e-5 product, P(c|a,b) = 1 — total 2.5e-5 against the
// naive 1.25e-7. The narrow object's (a=>b) dep is then inapplicable
// (nothing uncovered), exactly as re-running choose_best on the remainder
// yields no match.
func TestExtStatsBestPickVsProduct(t *testing.T) {
	scan := extStatsTable(t, 7007,
		[]catalog.Column{intCol("a", 0), intCol("b", 1), intCol("c", 2)}, nil)
	registerExtDeps(t, 7007, PlannerExtStatsObject{
		StatsOID: 9007, Keys: []int16{1, 2, 3},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2, 3}}},
	})
	RegisterPlannerExtStats(7007, PlannerExtStatsObject{
		StatsOID: 9008, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	conj := []Expr{extEq("a", 0, 1), extEq("b", 1, 2), extEq("c", 2, 3)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "best-pick selectivity", sel, 2.5e-5)
	for i, e := range estimated {
		if !e {
			t.Errorf("estimated[%d] = false, want all true (%v)", i, estimated)
		}
	}
	naive := 0.005 * 0.005 * 0.005
	if sel <= naive {
		t.Errorf("selectivity %v should attenuate (exceed) the naive product %v", sel, naive)
	}
}

// TestExtStatsOrSameColumn: OR of equalities on one column is one compatible
// clause (the is_orclause arm); (a=1 OR a=2) has 0.009975, and with
// (a=>b, f=1) the pair prices at min = 0.005.
func TestExtStatsOrSameColumn(t *testing.T) {
	scan := extStatsTable(t, 7008,
		[]catalog.Column{intCol("a", 0), intCol("b", 1)}, nil)
	registerExtDeps(t, 7008, PlannerExtStatsObject{
		StatsOID: 9008, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	orClause := &BinaryOp{Op: parser.OpOr, Left: extEq("a", 0, 1), Right: extEq("a", 0, 2)}
	conj := []Expr{orClause, extEq("b", 1, 3)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "OR-clause selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsOrMixedColumnsDeclines: OR arms on different columns poison
// the clause (upstream returns false on the first mismatched attnum).
func TestExtStatsOrMixedColumnsDeclines(t *testing.T) {
	scan := extStatsTable(t, 7009,
		[]catalog.Column{intCol("a", 0), intCol("b", 1)}, nil)
	registerExtDeps(t, 7009, PlannerExtStatsObject{
		StatsOID: 9009, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	orClause := &BinaryOp{Op: parser.OpOr, Left: extEq("a", 0, 1), Right: extEq("b", 1, 2)}
	conj := []Expr{orClause, extEq("b", 1, 3)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "mixed-OR selectivity", sel, 1.0)
	if estimated[0] || estimated[1] {
		t.Errorf("estimated = %v, want [false false]", estimated)
	}
}

// TestExtStatsInListCompatible: `a IN (consts)` is the ScalarArray arm.
func TestExtStatsInListCompatible(t *testing.T) {
	scan := extStatsTable(t, 7010,
		[]catalog.Column{intCol("a", 0), intCol("b", 1)}, nil)
	registerExtDeps(t, 7010, PlannerExtStatsObject{
		StatsOID: 9010, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	inClause := &InExpr{
		Operand: &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}},
		List:    []Expr{&IntegerConst{Value: 1}, &IntegerConst{Value: 2}},
	}
	conj := []Expr{inClause, extEq("b", 1, 3)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	// IN selectivity is the disjoint sum 0.01; f=1 takes min(0.01, 0.005).
	closeEnough(t, "IN-clause selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsAttnumMapping: attnums are Ordinal+1, not positions among the
// constrained columns. Table (x, a, b) with a dependency on attnums {2, 3}.
func TestExtStatsAttnumMapping(t *testing.T) {
	scan := extStatsTable(t, 7011,
		[]catalog.Column{intCol("x", 0), intCol("a", 1), intCol("b", 2)}, nil)
	registerExtDeps(t, 7011, PlannerExtStatsObject{
		StatsOID: 9011, Keys: []int16{2, 3},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{2, 3}}},
	})
	conj := []Expr{extEq("a", 1, 1), extEq("b", 2, 2)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	closeEnough(t, "attnum-mapped selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}

// TestExtStatsDeclines table-drives the gates that must leave the base
// estimator untouched: each case expects 1.0 with nothing estimated.
func TestExtStatsDeclines(t *testing.T) {
	twoCols := []catalog.Column{intCol("a", 0), intCol("b", 1)}
	fullDep := PlannerExtStatsObject{
		StatsOID: 9012, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	}
	rangeClause := &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 5},
	}

	t.Run("no registry", func(t *testing.T) {
		t.Cleanup(ClearPlannerExtStats)
		scan := extStatsTable(t, 7101, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{extEq("a", 0, 1), extEq("b", 1, 2)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
	t.Run("single clause", func(t *testing.T) {
		registerExtDeps(t, 7102, fullDep)
		scan := extStatsTable(t, 7102, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity([]Expr{extEq("a", 0, 1)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] {
			t.Errorf("estimated = %v, want [false]", estimated)
		}
	})
	t.Run("same column twice", func(t *testing.T) {
		registerExtDeps(t, 7103, fullDep)
		scan := extStatsTable(t, 7103, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{extEq("a", 0, 1), extEq("a", 0, 2)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
	t.Run("range plus eq", func(t *testing.T) {
		// Ranges are incompatible (non-eqsel operator), leaving one
		// distinct attnum — below the two-attnum gate.
		registerExtDeps(t, 7104, fullDep)
		scan := extStatsTable(t, 7104, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{rangeClause, extEq("b", 1, 2)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
	t.Run("object covers one clause column", func(t *testing.T) {
		t.Cleanup(ClearPlannerExtStats)
		RegisterPlannerExtStats(7105, PlannerExtStatsObject{
			StatsOID: 9013, Keys: []int16{1, 2},
			Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
		})
		scan := extStatsTable(t, 7105,
			[]catalog.Column{intCol("a", 0), intCol("b", 1), intCol("c", 2)}, nil)
		// Clauses on a,c: the object matches one (< 2) — no candidate.
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{extEq("a", 0, 1), extEq("c", 2, 3)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
	t.Run("dependency not fully matched", func(t *testing.T) {
		t.Cleanup(ClearPlannerExtStats)
		// Object matches (keys {1,2} both present) but its dependency
		// mentions attnum 3, which has no clause.
		RegisterPlannerExtStats(7106, PlannerExtStatsObject{
			StatsOID: 9014, Keys: []int16{1, 2},
			Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2, 3}}},
		})
		scan := extStatsTable(t, 7106, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{extEq("a", 0, 1), extEq("b", 1, 2)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
	t.Run("corrupt degree skipped", func(t *testing.T) {
		t.Cleanup(ClearPlannerExtStats)
		RegisterPlannerExtStats(7107, PlannerExtStatsObject{
			StatsOID: 9015, Keys: []int16{1, 2},
			Deps: []PlannerExtDependency{{Degree: math.NaN(), Attrs: []int16{1, 2}}},
		})
		scan := extStatsTable(t, 7107, twoCols, nil)
		sel, estimated := dependenciesClauselistSelectivity(
			[]Expr{extEq("a", 0, 1), extEq("b", 1, 2)}, scan)
		closeEnough(t, "selectivity", sel, 1.0)
		if estimated[0] || estimated[1] {
			t.Errorf("estimated = %v, want none", estimated)
		}
	})
}

// TestExtStatsJoinShapedDeclines: clauses resolving to two relation instances
// decline the whole list — the find_single_rel_for_clauses analog. Upstream
// NEVER runs extended statistics on a join clause list for the same reason.
func TestExtStatsJoinShapedDeclines(t *testing.T) {
	left := extStatsTable(t, 7201, []catalog.Column{intCol("a", 0)}, nil)
	right := extStatsTable(t, 7202, []catalog.Column{intCol("b", 0)}, nil)
	registerExtDeps(t, 7201, PlannerExtStatsObject{
		StatsOID: 9016, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	join := &Join{Left: left, Right: right}
	conj := []Expr{extEq("a", 0, 1), extEq("b", 1, 2)}
	sel, estimated := dependenciesClauselistSelectivity(conj, join)
	closeEnough(t, "join-list selectivity", sel, 1.0)
	if estimated[0] || estimated[1] {
		t.Errorf("estimated = %v, want none", estimated)
	}
}

// TestExtStatsBareBoolAndNot: bare boolean and NOT-column clauses are
// compatible (the "x = true" / "NOT x = false" fall-throughs).
func TestExtStatsBareBoolAndNot(t *testing.T) {
	scan := extStatsTable(t, 7301,
		[]catalog.Column{
			{Name: "flag", Type: catalog.Type{Name: "bool"}, Ordinal: 0},
			intCol("b", 1),
		}, nil)
	registerExtDeps(t, 7301, PlannerExtStatsObject{
		StatsOID: 9017, Keys: []int16{1, 2},
		Deps: []PlannerExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}},
	})
	bare := &ColumnRef{Index: 0, Name: "flag", Type: catalog.Type{Name: "bool"}}
	conj := []Expr{bare, extEq("b", 1, 2)}
	sel, estimated := dependenciesClauselistSelectivity(conj, scan)
	// Bare bool prices at the generic 1/3; f=1 takes min(1/3, 0.005).
	closeEnough(t, "bare-bool selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}

	notClause := &UnaryOp{Op: parser.OpNot, Operand: &ColumnRef{
		Index: 0, Name: "flag", Type: catalog.Type{Name: "bool"}}}
	sel, estimated = dependenciesClauselistSelectivity([]Expr{notClause, extEq("b", 1, 2)}, scan)
	closeEnough(t, "NOT selectivity", sel, 0.005)
	if !estimated[0] || !estimated[1] {
		t.Errorf("estimated = %v, want [true true]", estimated)
	}
}
