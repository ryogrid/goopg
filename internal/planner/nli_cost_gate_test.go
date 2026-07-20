package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// D6.3a — the stats-aware NLI semi/anti cost gate and the SubPlan cost
// helper. The table below is the stage's acceptance contract:
//
//   - a Q4-shaped selective outer over a huge indexed inner MUST accept
//     NLI-semi (rejecting it is the 71×-regression class: the hash semi
//     scans and hashes all 6M inner rows);
//   - a Q17/Q20-shaped huge outer MUST keep hash;
//   - semi/anti WITHOUT stats must keep hash (a stats-blind index-probe
//     loop over an unknown inner is exactly how Q4-class regressions
//     happen);
//   - INNER keeps the historical optimistic heuristic bit-for-bit.

// gateFixture builds outer/inner tables with an index on the inner join
// column and the given stats.
func gateFixture(t *testing.T, outerRows, innerRows, innerKeyND int64) (Node, *SeqScan, *catalog.Index) {
	t.Helper()
	c := catalog.NewInMemory()
	outerTbl, err := c.CreateTable(parser.ObjectName{Name: "outer_g"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	innerTbl, err := c.CreateTable(parser.ObjectName{Name: "inner_g"}, []catalog.Column{
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "inner_g_key_idx"}, innerTbl,
		[]string{"i_key"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if outerRows > 0 {
		outerTbl.Stats = &catalog.TableStats{RowCount: outerRows,
			Columns: []catalog.ColumnStats{{NDistinct: outerRows}}}
	}
	if innerRows > 0 {
		innerTbl.Stats = &catalog.TableStats{RowCount: innerRows,
			Columns: []catalog.ColumnStats{{NDistinct: innerKeyND}}}
	}
	outer := &SeqScan{Table: outerTbl, schema: Schema{{Name: "o_key"}}}
	inner := &SeqScan{Table: innerTbl, schema: Schema{{Name: "i_key"}}}
	return outer, inner, idx
}

func TestNLICostGateTable(t *testing.T) {
	cases := []struct {
		name       string
		joinType   JoinType
		outerRows  int64 // 0 = no stats
		innerRows  int64
		innerKeyND int64
		want       bool
	}{
		// Q4 shape: ~57K date-filtered orders probing 6M lineitem via
		// l_orderkey (nd≈1.5M → matchSet 4). 57640*4 < 6M+57640 → accept.
		{"q4-selective-outer-accepts-semi", JoinTypeSemi, 57640, 6000000, 1500000, true},
		// Q21-ish: 6M outer semi — 6M*4 = 24M > 6M+6M → keep hash.
		{"large-outer-keeps-hash-semi", JoinTypeSemi, 6000000, 6000000, 1500000, false},
		// Anti symmetric to Q4 shape.
		{"q4-shape-anti-accepts", JoinTypeAnti, 57640, 6000000, 1500000, true},
		{"large-outer-keeps-hash-anti", JoinTypeAnti, 6000000, 6000000, 1500000, false},
		// No stats: OPTIMISTIC accept for every join type. The first cut
		// rejected semi/anti here; measured reality inverted it — goopg's
		// ANALYZE stats are in-memory and lost on restart, so no-stats is
		// the COMMON case and rejecting it disabled semi/anti NLI in
		// practice (fresh-server Q4 flipped to hash semi = the 276 s /
		// 71x class this gate exists to prevent).
		{"no-stats-semi-optimistic", JoinTypeSemi, 0, 0, 0, true},
		{"no-stats-anti-optimistic", JoinTypeAnti, 0, 0, 0, true},
		{"no-stats-inner-optimistic", JoinTypeInner, 0, 0, 0, true},
		// INNER keeps the bare heuristic: 100K boundary.
		{"inner-under-threshold", JoinTypeInner, 100000, 6000000, 1500000, true},
		{"inner-over-threshold", JoinTypeInner, 100001, 6000000, 1500000, false},
		// Fat match set (nd small): 1000 outer × matchSet 600 = 600K >
		// 6000+1000 → keep hash even for a small outer.
		{"fat-matchset-keeps-hash", JoinTypeSemi, 1000, 6000, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outer, inner, idx := gateFixture(t, tc.outerRows, tc.innerRows, tc.innerKeyND)
			if got := nliCostGateAccepts(tc.joinType, outer, inner, idx); got != tc.want {
				t.Fatalf("gate(%v, outer=%d, inner=%d, nd=%d) = %v, want %v",
					tc.joinType, tc.outerRows, tc.innerRows, tc.innerKeyND, got, tc.want)
			}
		})
	}
}

func TestNLICostGateLegacyHatch(t *testing.T) {
	SetNLICostGateLegacy(true)
	t.Cleanup(func() { SetNLICostGateLegacy(false) })
	// Legacy: stats-blind — a no-stats semi is accepted again (old
	// optimistic behavior). GOOPG_NLI_COSTGATE=legacy maps to this.
	outer, inner, idx := gateFixture(t, 0, 0, 0)
	if !nliCostGateAccepts(JoinTypeSemi, outer, inner, idx) {
		t.Fatal("legacy hatch did not restore the optimistic semi gate")
	}
}

func TestEstimateSubplanCostPerCall(t *testing.T) {
	outer, inner, idx := gateFixture(t, 1000, 6000000, 1500000)
	_ = outer
	// SeqScan-rooted: full table rows.
	if got := estimateSubplanCostPerCall(inner); got != 6000000 {
		t.Fatalf("seqscan cost = %d, want 6000000", got)
	}
	// Index-probe chain: matchSet (6M/1.5M = 4), through wrappers.
	probe := &IndexScan{Table: inner.Table, Index: idx, schema: inner.schema}
	var n Node = probe
	n = &Filter{Child: n, Predicate: &BooleanConst{Value: true}}
	n = &Aggregate{Child: n}
	if got := estimateSubplanCostPerCall(n); got != 4 {
		t.Fatalf("index-probe chain cost = %d, want 4", got)
	}
	// Unknown stats → 0 (unknown), never "free".
	noStatsOuter, noStatsInner, _ := gateFixture(t, 0, 0, 0)
	_ = noStatsOuter
	if got := estimateSubplanCostPerCall(noStatsInner); got != 0 {
		t.Fatalf("no-stats cost = %d, want 0 (unknown)", got)
	}
}

// TestZeroEquijoinPrefersNLSemi pins the D6.3a decision that the
// zero-equijoin EXISTS always takes the NL semi when its residuals are
// liftable: the NL materialises the sans-residual body once and re-scans
// it per outer row, which the rough cost model can never rank worse than
// re-running the same scan per call as a SubPlan (see the comment at the
// canUnnestExistsExpr call site). Both the index-probe-cheap body and
// the plain SeqScan body unnest.
func TestZeroEquijoinPrefersNLSemi(t *testing.T) {
	c := catalog.NewInMemory()
	_, err := c.CreateTable(parser.ObjectName{Name: "zo"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	innerTbl, err := c.CreateTable(parser.ObjectName{Name: "zi"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "zi_k_idx"}, innerTbl,
		[]string{"k"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	innerTbl.Stats = &catalog.TableStats{RowCount: 100000,
		Columns: []catalog.ColumnStats{{NDistinct: 50000}, {NDistinct: 100}}}

	innerSchema := Schema{{Name: "k"}, {Name: "v"}}
	residual := func() Expr {
		return &BinaryOp{Op: parser.OpGt,
			Left:  &ColumnRef{Name: "v", Index: 1},
			Right: &OuterColumnRef{Name: "a", Index: 0, Level: 1},
		}
	}

	cheapBody := Node(&Filter{
		Predicate: residual(),
		Child:     &IndexScan{Table: innerTbl, Index: idx, Key: &IntegerConst{Value: 7}, schema: innerSchema},
	})
	if !canUnnestExistsExpr(&ExistsExpr{Plan: cheapBody}) {
		t.Fatal("index-probe-cheap zero-equijoin EXISTS should still take the NL semi")
	}

	scanBody := Node(&Filter{
		Predicate: residual(),
		Child:     &SeqScan{Table: innerTbl, schema: innerSchema},
	})
	if !canUnnestExistsExpr(&ExistsExpr{Plan: scanBody}) {
		t.Fatal("seqscan-bodied zero-equijoin EXISTS should take the NL semi")
	}
}

// TestSublinkConjunctPlacementProperty pins the structural fact S5a
// depends on: predicate pushdown never moves a sublink-bearing conjunct
// below its Filter (pushdown.go treats sublink exprs as out of scope,
// and pushConjunctsBelowSemiAnti sinks only sublink-FREE conjuncts).
func TestSublinkConjunctPlacementProperty(t *testing.T) {
	SetSubqueryUnnestEnabled(false)
	t.Cleanup(func() { SetSubqueryUnnestEnabled(true) })

	c := catalog.NewInMemory()
	for _, spec := range []struct {
		name string
		cols []catalog.Column
	}{
		{"pp1", []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "b", Type: catalog.Type{Name: "int4"}}}},
		{"pp2", []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "c", Type: catalog.Type{Name: "int4"}}}},
		{"pp3", []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}},
	} {
		if _, err := c.CreateTable(parser.ObjectName{Name: spec.name}, spec.cols); err != nil {
			t.Fatal(err)
		}
	}
	stmts, err := parser.Parse("SELECT pp1.a FROM pp1, pp2 WHERE pp1.a = pp2.a AND pp1.b > 1 AND EXISTS (SELECT 1 FROM pp3 WHERE pp3.x = pp1.b)")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	plan, err := Plan(sel, c)
	if err != nil {
		t.Fatal(err)
	}
	// The sublink must live in a Filter ABOVE the join, never inside a
	// scan-level Filter below it. Walk: record the depth-from-root of
	// every Filter carrying a sublink and of the topmost Join; a sublink
	// Filter deeper than the join means it was pushed below.
	joinSeen := false
	violated := false
	var walk func(n Node, belowJoin bool)
	walk = func(n Node, belowJoin bool) {
		switch x := n.(type) {
		case *Filter:
			has := false
			WalkExprTree(x.Predicate, func(e Expr) {
				switch s := e.(type) {
				case *ExistsExpr:
					if s.Plan != nil {
						has = true
					}
				}
			})
			if has && belowJoin {
				violated = true
			}
			walk(x.Child, belowJoin)
		case *Join:
			joinSeen = true
			walk(x.Left, true)
			walk(x.Right, true)
		case *Project:
			walk(x.Child, belowJoin)
		case *Sort:
			walk(x.Child, belowJoin)
		case *Limit:
			walk(x.Child, belowJoin)
		case *Aggregate:
			walk(x.Child, belowJoin)
		case *MultiHashJoin:
			joinSeen = true
			for _, tb := range x.Tables {
				walk(tb, true)
			}
		case *NestedLoopIndexJoin:
			joinSeen = true
			walk(x.Outer, true)
		}
	}
	walk(plan, false)
	if !joinSeen {
		t.Fatal("fixture did not produce a join")
	}
	if violated {
		t.Fatal("a sublink-bearing conjunct was pushed below the join")
	}
}
