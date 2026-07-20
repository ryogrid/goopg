package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Stage 6 (S1c) — D3.0 collector fix: correlated EXISTS / scalar
// subqueries must decorrelate even when the inner planner folds the
// correlation equijoin into an IndexScan probe key instead of a Filter
// conjunct. Before the fix these shapes bailed to a SubPlan on any
// indexed inner table, which is why decorrelation never fired on the
// TPC-H schema (bundle W1). See harvestIndexKeyParams + the IndexScan
// arm of clonePlanReplacingOuter in unnest.go.
//
// These tests build a catalog WITH an index on the inner correlation
// column so the inner planner produces the `IndexScan.Key =
// OuterColumnRef` shape the fix targets — the index-less variants are
// already covered by exists_unnest_test.go / the S1a guard tests.

// indexedCorrCatalog: outer(o_key, o_val) and inner(i_id, i_key, i_a,
// i_b) with a single-column index on the correlation column i_key and a
// composite index on (i_key, i_a).
func indexedCorrCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	outer, err := c.CreateTable(parser.ObjectName{Name: "outer_t"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_val", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "outer_pk"}, outer, []string{"o_key"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	inner, err := c.CreateTable(parser.ObjectName{Name: "inner_t"}, []catalog.Column{
		{Name: "i_id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
		{Name: "i_a", Type: catalog.Type{Name: "int4"}},
		{Name: "i_b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "inner_key_idx"}, inner, []string{"i_key"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "inner_key_a_idx"}, inner, []string{"i_key", "i_a"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	// D6.3a: the semi/anti NLI cost gate requires ANALYZE stats and keeps
	// hash without them. These numbers model the selective shape the
	// fixture exists for: 1 000 outer rows probing a 10 000-row inner
	// whose correlation column has ~5 000 distinct values (match set 2).
	outer.Stats = &catalog.TableStats{RowCount: 1000, Columns: []catalog.ColumnStats{
		{NDistinct: 1000}, {NDistinct: 500},
	}}
	inner.Stats = &catalog.TableStats{RowCount: 10000, Columns: []catalog.ColumnStats{
		{NDistinct: 10000}, {NDistinct: 5000}, {NDistinct: 100}, {NDistinct: 50},
	}}
	return c
}

// indexedCorrCatalogNoIndex is the same schema with NO index on the
// correlation column, for the index-toggle determinism test.
func indexedCorrCatalogNoIndex(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "outer_t"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "o_val", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "inner_t"}, []catalog.Column{
		{Name: "i_id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
		{Name: "i_a", Type: catalog.Type{Name: "int4"}},
		{Name: "i_b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// hasSemiOrAntiOfType reports whether the plan contains a semi/anti join
// of the wanted type, as EITHER a *Join or a *NestedLoopIndexJoin — a
// clean index-probe semi/anti is rewritten to NLI (rendered "Nested Loop
// (SEMI)"), so a test that only looked for *Join would miss it.
func hasSemiOrAntiOfType(node Node, want JoinType) bool {
	found := false
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || found {
			return
		}
		switch x := n.(type) {
		case *Join:
			if x.Type == want {
				found = true
				return
			}
			walk(x.Left)
			walk(x.Right)
		case *NestedLoopIndexJoin:
			if x.Type == want {
				found = true
				return
			}
			walk(x.Outer)
			walk(x.Inner)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *MultiHashJoin:
			for _, tbl := range x.Tables {
				walk(tbl)
			}
		}
	}
	walk(node)
	return found
}

func hasAnySemiOrAnti(node Node) bool {
	return hasSemiOrAntiOfType(node, JoinTypeSemi) || hasSemiOrAntiOfType(node, JoinTypeAnti)
}

// firstSemiOrAntiPredicate returns the residual Predicate of the first
// semi/anti join (Join or NestedLoopIndexJoin) in the plan, or nil.
func firstSemiOrAntiPredicate(node Node) Expr {
	var pred Expr
	var walk func(Node)
	done := false
	walk = func(n Node) {
		if n == nil || done {
			return
		}
		switch x := n.(type) {
		case *Join:
			if x.Type == JoinTypeSemi || x.Type == JoinTypeAnti {
				pred = x.Predicate
				done = true
				return
			}
			walk(x.Left)
			walk(x.Right)
		case *NestedLoopIndexJoin:
			if x.Type == JoinTypeSemi || x.Type == JoinTypeAnti {
				pred = x.Predicate
				done = true
				return
			}
			walk(x.Outer)
			walk(x.Inner)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *MultiHashJoin:
			for _, tbl := range x.Tables {
				walk(tbl)
			}
		}
	}
	walk(node)
	return pred
}

// selfProbeIndexScan finds an IndexScan whose probe key is a ColumnRef
// naming one of its OWN table's columns — the circular self-probe the
// clone rewrite must avoid. Returns nil when none exists.
func selfProbeIndexScan(node Node) *IndexScan {
	var found *IndexScan
	var walk func(Node)
	keyIsOwnColumn := func(is *IndexScan, e Expr) bool {
		cr, ok := e.(*ColumnRef)
		if !ok || is.Table == nil {
			return false
		}
		for _, col := range is.Table.Columns {
			if col.Name == cr.Name {
				return true
			}
		}
		return false
	}
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		if is, ok := n.(*IndexScan); ok {
			if keyIsOwnColumn(is, is.Key) {
				found = is
				return
			}
			for _, k := range is.Keys {
				if keyIsOwnColumn(is, k) {
					found = is
					return
				}
			}
		}
		switch x := n.(type) {
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *MultiHashJoin:
			for _, tbl := range x.Tables {
				walk(tbl)
			}
		}
	}
	walk(node)
	return found
}

// planHasSubqueryExpr reports whether any SubqueryExpr / ExistsExpr /
// InExpr with a plan survives — i.e. the sublink did NOT decorrelate.
func planHasSubqueryExpr(node Node) bool {
	found := false
	walkPlanExprs(node, func(e Expr) {
		switch x := e.(type) {
		case *SubqueryExpr:
			if x.Plan != nil {
				found = true
			}
		case *ExistsExpr:
			if x.Plan != nil {
				found = true
			}
		case *InExpr:
			if x.Plan != nil {
				found = true
			}
		}
	})
	return found
}

// exprTreeMentions reports whether a ColumnRef named `name` appears
// anywhere in a plan node or an expression tree.
func exprTreeMentions(x interface{}, name string) bool {
	found := false
	check := func(e Expr) {
		if cr, ok := e.(*ColumnRef); ok && cr.Name == name {
			found = true
		}
		if oc, ok := e.(*OuterColumnRef); ok && oc.Name == name {
			found = true
		}
	}
	switch v := x.(type) {
	case Node:
		walkPlanExprs(v, check)
	case Expr:
		walkExprTree(v, check)
	}
	return found
}

// findFirstAggregate returns the first Aggregate node in the plan.
func findFirstAggregate(node Node) *Aggregate {
	var found *Aggregate
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		if a, ok := n.(*Aggregate); ok {
			found = a
			return
		}
		switch x := n.(type) {
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *MultiHashJoin:
			for _, tbl := range x.Tables {
				walk(tbl)
			}
		}
	}
	walk(node)
	return found
}

func TestIndexKeyCorrelatedExistsDecorrelates(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAnySemiOrAnti(node) {
		t.Fatalf("correlated EXISTS on an indexed inner did not decorrelate to a semi/anti join")
	}
	if !hasSemiOrAntiOfType(node, JoinTypeSemi) {
		t.Fatalf("EXISTS should decorrelate to a SEMI join specifically")
	}
	if planHasSubqueryExpr(node) {
		t.Fatalf("an ExistsExpr survived after decorrelation")
	}
	if planHasOuterRefRemaining(node) {
		t.Fatalf("an OuterColumnRef survived the rewrite")
	}
	if is := selfProbeIndexScan(node); is != nil {
		t.Fatalf("cloned inner is a circular self-probe IndexScan on %s", is.Table.Name)
	}
}

func TestIndexKeyCorrelatedNotExistsDecorrelatesToAnti(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE NOT EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSemiOrAntiOfType(node, JoinTypeAnti) {
		t.Fatalf("NOT EXISTS on an indexed inner did not decorrelate to an ANTI join")
	}
	if planHasSubqueryExpr(node) {
		t.Fatalf("an ExistsExpr survived after decorrelation")
	}
	if planHasOuterRefRemaining(node) {
		t.Fatalf("an OuterColumnRef survived the rewrite")
	}
}

func TestIndexKeyExistsWithInnerResidualDecorrelates(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key AND i_a < i_b)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAnySemiOrAnti(node) {
		t.Fatalf("EXISTS + inner residual did not decorrelate")
	}
	if planHasSubqueryExpr(node) {
		t.Fatalf("an ExistsExpr survived after decorrelation")
	}
	// The inner-only residual `i_a < i_b` must survive somewhere in the
	// build side — it is not part of the correlation and must still
	// filter the semi-join build rows.
	if !exprTreeMentions(node, "i_a") || !exprTreeMentions(node, "i_b") {
		t.Fatalf("the inner residual i_a < i_b did not survive the rewrite")
	}
	if planHasOuterRefRemaining(node) {
		t.Fatalf("an OuterColumnRef survived the rewrite")
	}
}

// TestIndexKeyScalarProbeCheapStaysSubPlan pins the S6 (D6.2)
// selectivity-aware policy — the measured amendment to the bundle's
// D6.1: a correlated scalar whose inner plans as an index probe
// (Aggregate over IndexScan — the Q17/Q20 shape) is served better by the
// executor's CorrSubqOps rescan path (Q17: rebuilds=1, rescans=6667)
// than by a whole-table GROUP BY + join (measured 58.27 s → 86.65 s), so
// even with the harvest enabled it must remain a SubPlan.
func TestIndexKeyScalarProbeCheapStaysSubPlan(t *testing.T) {
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE o_val < (SELECT avg(i_a) FROM inner_t WHERE i_key = outer_t.o_key)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findFirstJoinByType(node, JoinTypeInner) != nil && findFirstAggregate(node) != nil {
		t.Fatalf("index-probe-cheap scalar must NOT decorrelate (S6 policy)")
	}
	if !planHasSubqueryExpr(node) {
		t.Fatalf("the scalar SubqueryExpr should survive as a SubPlan")
	}
}

// TestIndexKeyScalarJoinInnerDecorrelates is the policy's other half —
// the Q2 shape: a correlated scalar whose inner joins another table has
// no cheap rescan form (its SubPlan path rebuilt an Aggregate over a
// multi-table join at ≈26 ms per call; decorrelating measured
// 10.87 s → 3.36 s), so with the harvest on it must decorrelate to the
// GROUP BY + INNER join.
func TestIndexKeyScalarJoinInnerDecorrelates(t *testing.T) {
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	// extra_t joins inner_t inside the subquery, so the inner plan is an
	// Aggregate over a join — not an index-probe shape.
	if _, err := cat.CreateTable(parser.ObjectName{Name: "extra_t"}, []catalog.Column{
		{Name: "e_id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "e_w", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	sql := "SELECT o_key FROM outer_t WHERE o_val < (SELECT min(i_a) FROM inner_t, extra_t WHERE i_key = outer_t.o_key AND e_id = i_b)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findFirstJoinByType(node, JoinTypeInner) == nil {
		t.Fatalf("scalar with a multi-table inner did not decorrelate to an INNER join")
	}
	if findFirstAggregate(node) == nil {
		t.Fatalf("decorrelated scalar should build a GROUP BY aggregate")
	}
	if planHasSubqueryExpr(node) {
		t.Fatalf("a SubqueryExpr survived after decorrelation")
	}
	if planHasOuterRefRemaining(node) {
		t.Fatalf("an OuterColumnRef survived the rewrite")
	}
}

// TestIndexKeyExistsResidualBecomesNLISemi is the S6 (D6.2) end-to-end
// Q4-shape assertion: with the harvest on, an EXISTS whose body carries
// an inner-only residual AND whose WHERE has an outer local conjunct
// must plan as an index-driven NLI semi join — residual on the NLI, the
// local conjunct sunk BELOW the join (pushConjunctsBelowSemiAnti) so the
// probe count and the NLI cost gate both see the filtered outer.
func TestIndexKeyExistsResidualBecomesNLISemi(t *testing.T) {
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE o_val > 5 AND EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key AND i_a < i_b)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	var nli *NestedLoopIndexJoin
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || nli != nil {
			return
		}
		switch x := n.(type) {
		case *NestedLoopIndexJoin:
			if x.Type == JoinTypeSemi {
				nli = x
				return
			}
			walk(x.Outer)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		}
	}
	walk(node)
	if nli == nil {
		t.Fatalf("EXISTS with inner residual did not become an NLI semi join")
	}
	if nli.Predicate == nil {
		t.Fatalf("the inner residual was lost — NLI.Predicate is nil")
	}
	f, isFilter := nli.Outer.(*Filter)
	if !isFilter {
		t.Fatalf("the outer local conjunct was not sunk below the semi join; NLI.Outer is %T", nli.Outer)
	}
	if !exprTreeMentions(f.Predicate, "o_val") {
		t.Fatalf("the sunk Filter does not carry the o_val conjunct")
	}
	if sp := selfProbeIndexScan(node); sp != nil {
		t.Fatalf("self-probe IndexScan survived: %v", sp.Index)
	}
	if planHasSubqueryExpr(node) {
		t.Fatalf("an ExistsExpr survived after decorrelation")
	}
}

// TestIndexKeyToggleDeterminism: the same correlated EXISTS decorrelates
// whether or not the inner correlation column is indexed. Before the fix
// only the index-less form fired.
func TestIndexKeyToggleDeterminism(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	sql := "SELECT o_key FROM outer_t WHERE EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key)"

	withIdx, err := Plan(parseOne(t, sql), indexedCorrCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	noIdx, err := Plan(parseOne(t, sql), indexedCorrCatalogNoIndex(t))
	if err != nil {
		t.Fatal(err)
	}
	if !hasAnySemiOrAnti(withIdx) {
		t.Fatalf("indexed inner did not decorrelate")
	}
	if !hasAnySemiOrAnti(noIdx) {
		t.Fatalf("index-less inner did not decorrelate (regression in the base path)")
	}
}

// TestIndexKeyCompositeCorrelationStaysSubPlan: a two-column (composite)
// correlation must NOT be pulled up to a semi/anti join, because the
// second equijoin pair would have to ride as an equi-residual on the
// semi join, which the downstream NLI rewrite can silently drop (the
// pair gets extracted as a competing probe key). Refusing the pull-up
// keeps the answer correct via the SubPlan path; no TPC-H query needs
// composite-EXISTS decorrelation. Single-column correlations still fire
// (the other tests here). Composite decorrelation is a recorded
// follow-up. A regression that starts pulling this up — without also
// making the NLI path preserve the second equality — would return wrong
// results, so this test guards the refusal.
func TestIndexKeyCompositeCorrelationStaysSubPlan(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE EXISTS (SELECT 1 FROM inner_t WHERE i_key = outer_t.o_key AND i_a = outer_t.o_val)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if hasAnySemiOrAnti(node) {
		t.Fatalf("composite (two-equijoin) EXISTS must NOT decorrelate — the second pair can be dropped by the NLI rewrite")
	}
	if !planHasSubqueryExpr(node) {
		t.Fatalf("composite EXISTS should remain a SubPlan")
	}
}

// TestIndexKeyRangeCorrelationStaysSubPlan: a range correlation lands in
// LowKey/HighKey, which is NOT an equijoin and must keep the shape a
// SubPlan (matrix row M14).
func TestIndexKeyRangeCorrelationStaysSubPlan(t *testing.T) {
	// The harvest is ON by default since S6; enabled explicitly here so the
	// test is self-contained regardless of sibling-test toggles.
	SetIndexKeyHarvestEnabled(true)
	t.Cleanup(func() { SetIndexKeyHarvestEnabled(true) }) // restore the ON default

	cat := indexedCorrCatalog(t)
	sql := "SELECT o_key FROM outer_t WHERE EXISTS (SELECT 1 FROM inner_t WHERE i_key > outer_t.o_key)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if hasAnySemiOrAnti(node) {
		t.Fatalf("range-correlated EXISTS must NOT decorrelate to a semi/anti join")
	}
	if !planHasSubqueryExpr(node) {
		t.Fatalf("range-correlated EXISTS should remain a SubPlan (ExistsExpr)")
	}
}
