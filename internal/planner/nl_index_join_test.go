package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestNLIRulePromotesEquiJoinOnIndexedInner asserts the M0054-0006c
// rewrite fires for the canonical shape: a binary equi-join whose
// inner table has a single-column B-tree index on the join key.
func TestNLIRulePromotesEquiJoinOnIndexedInner(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, parts,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stmt := parseOne(t, `SELECT p_name, l_quantity FROM lineitem, part WHERE l_partkey = p_partkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !findNLI(node) {
		t.Fatalf("expected NestedLoopIndexJoin in plan; got: %s", describePlanTree(node))
	}
}

// TestNLIRuleSkipsWhenInnerHasNoIndex asserts the rewrite is a
// no-op when the inner side's join column has no B-tree index.
// The cost-gate path is also exercised — outer is small but no
// index → no NLI.
func TestNLIRuleSkipsWhenInnerHasNoIndex(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "a"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "b"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM a, b WHERE a.x = b.y`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findNLI(node) {
		t.Fatalf("did not expect NLI when no index exists; tree: %s", describePlanTree(node))
	}
}

// TestNLIRuleRespectsKillSwitch confirms `SetNLIEnabled(false)`
// short-circuits the rewrite — the rollback path described in the
// M0054-0006e design.
func TestNLIRuleRespectsKillSwitch(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, parts,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM lineitem, part WHERE l_partkey = p_partkey`)

	SetNLIEnabled(false)
	defer SetNLIEnabled(true)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findNLI(node) {
		t.Fatalf("kill-switch off: did not expect NLI; tree: %s", describePlanTree(node))
	}
}

// TestNLIRulePromotesCompositeKeyJoinWithFullLeadingPrefix
// (M0054-0006-followup-Q9-composite) asserts that when every
// leading column of a composite B-tree index is bound by an
// equi-conjunct, the rule emits an NLI with `Keys` populated
// and a multi-column probe.
func TestNLIRulePromotesCompositeKeyJoinWithFullLeadingPrefix(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_supplycost", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "partsupp_pk"}, parts,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM lineitem, partsupp WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nli := firstNLI(node)
	if nli == nil {
		t.Fatalf("expected NLI in plan; tree: %s", describePlanTree(node))
	}
	if got := len(nli.Inner.Keys); got != 2 {
		t.Fatalf("expected Inner.Keys length 2 for composite-key full-prefix probe; got %d (Inner.Key=%v)", got, nli.Inner.Key)
	}
	if nli.Inner.Index == nil || nli.Inner.Index.Name != "partsupp_pk" {
		t.Fatalf("expected partsupp_pk index; got %v", nli.Inner.Index)
	}
}

// TestNLIRuleSkipsCompositeIndexWithPartialKey
// (M0054-0006-followup-Q9-composite) asserts the regression
// guard: when only the leading column of a composite index has
// an equi-conjunct, the rule MUST refuse to promote and the
// plan keeps a HashJoin (or whatever the upstream rules
// produced). Promoting on a partial prefix would hit the Q9
// "is not numeric at runtime" regression.
func TestNLIRuleSkipsCompositeIndexWithPartialKey(t *testing.T) {
	cat := catalog.NewInMemory()
	parts, err := cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only a composite index — single-column index on ps_partkey
	// is intentionally absent so the rule has only the composite
	// to choose from.
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "partsupp_pk"}, parts,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	_, err = cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Predicate binds only ps_partkey — ps_suppkey unbound.
	stmt := parseOne(t, `SELECT * FROM lineitem, partsupp WHERE l_partkey = ps_partkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findNLI(node) {
		t.Fatalf("partial-prefix on composite index must not promote to NLI; tree: %s", describePlanTree(node))
	}
}

// (M0077-0001 / takeover) Q8's chained-NLI rebind regression
// (`customer_pk` and `nation_pk` probe keys pointing at stale
// slots) is locked end-to-end by the live focused TPC-H gate
// (`./tpch-runner --queries=8`) rather than a synthetic-plan
// unit test — the live planner's choice of NLI shape depends on
// SF=1 statistics that the in-memory test fixture cannot
// reproduce. The rebind helper itself (the type-mismatch
// override that bypasses M0075-0002's selectivity guard when
// the runtime probe encoder would otherwise fail with 42804) is
// pinned by `TestOrigTypeMatchesDetectsRuntimeTypeMismatch` in
// `nl_index_join_selectivity_test.go`.

// firstNLI returns the first *NestedLoopIndexJoin found in the
// plan tree, or nil. Used by tests that inspect the produced
// NLI's inner keys.
func firstNLI(n Node) *NestedLoopIndexJoin {
	if n == nil {
		return nil
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		return x
	case *Project:
		return firstNLI(x.Child)
	case *Filter:
		return firstNLI(x.Child)
	case *Sort:
		return firstNLI(x.Child)
	case *Limit:
		return firstNLI(x.Child)
	case *Aggregate:
		return firstNLI(x.Child)
	case *WindowAgg:
		return firstNLI(x.Child)
	case *Join:
		if r := firstNLI(x.Left); r != nil {
			return r
		}
		return firstNLI(x.Right)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			if r := firstNLI(t); r != nil {
				return r
			}
		}
	}
	return nil
}

// TestNLIRulePromotesAcrossFilterCrossJoinFromView
// (M0054-0006-followup-Q15b) covers the view-substitution shape
// where the WHERE equi-conjunct ends up on a top-level Filter and
// the underlying Join is a CROSS JOIN with no Predicate. The
// rule used to emit NLI on the indexed supplier table here, but
// M0063-0001 disabled NLI for outers that are isolated-scope
// Project wrappers (view-rename / derived-table). The runtime
// row layout flips when the inner SeqScan is on the original
// Right and the outer is the IsolatedScope subtree (Q15b's
// shape), causing the parent Filter's ColumnRefs to land on the
// wrong slots. The hash-join path handles this correctly.
//
// This test now asserts the gate fires: the plan must NOT
// contain an NLI when the outer is an IsolatedScope-wrapped
// view. Re-enable when the row-layout-flip is fixed at the
// `nliRewrite` substitution level.
func TestNLIRuleSkipsIsolatedScopeOuter(t *testing.T) {
	cat := catalog.NewInMemory()
	supplier, err := cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "s_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "supplier_pk"}, supplier,
		[]string{"s_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_extendedprice", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Install revenue0 as a real catalog VIEW. The view-substitution
	// path is what produces the Cross-Join + top-Filter shape.
	viewStmts, err := parser.Parse(`SELECT l_suppkey, sum(l_extendedprice) FROM lineitem GROUP BY l_suppkey`)
	if err != nil {
		t.Fatal(err)
	}
	innerSel := viewStmts[0].(*parser.SelectStmt)
	if _, err := cat.CreateView(parser.ObjectName{Name: "revenue0"},
		[]catalog.Column{
			{Name: "supplier_no", Type: catalog.Type{Name: "int4"}},
			{Name: "total_revenue", Type: catalog.Type{Name: "int4"}},
		},
		[]string{"supplier_no", "total_revenue"},
		innerSel, true); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	stmt := parseOne(t, `SELECT s_suppkey, s_name, total_revenue FROM supplier, revenue0 WHERE s_suppkey = supplier_no`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if nli := firstNLI(node); nli != nil {
		t.Fatalf("M0063-0001: NLI must NOT fire for IsolatedScope outer; tree: %s", describePlanTree(node))
	}
}

// TestNLIRulePromotesAcrossOROfANDsCommonEqui
// (M0054-0006-followup-Q19) covers the Q19-shape disjunctive
// predicate where the join equi-conjunct is repeated in every
// OR branch. The rule must factor the common equi-conjunct
// into the join key while keeping the full OR as the residual
// Predicate on NLI for per-branch filtering.
func TestNLIRulePromotesAcrossOROfANDsCommonEqui(t *testing.T) {
	cat := catalog.NewInMemory()
	part, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_brand", Type: catalog.Type{Name: "char", Args: []int64{10}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, part, []string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, `SELECT * FROM lineitem, part WHERE
		(p_partkey = l_partkey AND p_brand = 'Brand#12' AND l_quantity >= 1)
		OR (p_partkey = l_partkey AND p_brand = 'Brand#23' AND l_quantity >= 10)
		OR (p_partkey = l_partkey AND p_brand = 'Brand#34' AND l_quantity >= 20)`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nli := firstNLI(node)
	if nli == nil {
		t.Fatalf("expected NLI from OR-factor; tree: %s", describePlanTree(node))
	}
	if nli.Inner.Index == nil || nli.Inner.Index.Name != "part_pk" {
		t.Fatalf("expected part_pk on inner; got %v", nli.Inner.Index)
	}
}

// findNLI returns true when the plan tree contains a
// `*NestedLoopIndexJoin` anywhere.
func findNLI(n Node) bool {
	if n == nil {
		return false
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		return true
	case *Project:
		return findNLI(x.Child)
	case *Filter:
		return findNLI(x.Child)
	case *Sort:
		return findNLI(x.Child)
	case *Limit:
		return findNLI(x.Child)
	case *Aggregate:
		return findNLI(x.Child)
	case *WindowAgg:
		return findNLI(x.Child)
	case *Join:
		return findNLI(x.Left) || findNLI(x.Right)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			if findNLI(t) {
				return true
			}
		}
	}
	return false
}

// describePlanTree returns a short string summary used only for
// failure messages.
func describePlanTree(n Node) string {
	if n == nil {
		return "nil"
	}
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		return "NestedLoopIndexJoin{Outer:" + describePlanTree(x.Outer) + ", Inner:" + describePlanTree(x.Inner) + "}"
	case *Project:
		return "Project(" + describePlanTree(x.Child) + ")"
	case *Filter:
		return "Filter(" + describePlanTree(x.Child) + ")"
	case *Join:
		return "Join{algo:" + joinAlgoName(x.Algo) + "}"
	case *MultiHashJoin:
		return "MHJ"
	case *SeqScan:
		return "Seq(" + x.Table.Name + ")"
	case *IndexScan:
		return "Idx(" + x.Table.Name + "/" + x.Index.Name + ")"
	}
	return "?"
}

func joinAlgoName(a JoinAlgo) string {
	switch a {
	case JoinAlgoNestedLoop:
		return "NL"
	case JoinAlgoHash:
		return "Hash"
	case JoinAlgoMerge:
		return "Merge"
	}
	return "?"
}
