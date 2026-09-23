package optimizer

// M0145-0008 — the one-relation index producer on the routes that skip the
// rule-based `isSimpleSingle` bypass.
//
// The bypass is the only producer of an index path driven by a correlated
// (outer-reference) restriction. `GOOPG_JOINTREE_PIPELINE` and
// `GOOPG_ONEREL_SEARCH` both route a single-relation scope through the generic
// arm instead, whose base-rel pathlist has no such producer — so the scope came
// out as a bare `Filter{SeqScan}`.
//
// That is not cosmetic. `canUnnestSubquery`'s S6/D6.2 guard
// (`innerPlanIsIndexProbeCheap`) reads the body's SHAPE to decide whether
// decorrelating a correlated scalar is a loss, so a body that never got its
// probe reads as "not cheap" and is decorrelated into a whole-table GROUP BY.
// Measured on TPC-H Q17 at SF1: 1021 ms -> 11155 ms on the jointree arm and
// 1021 ms -> 10625 ms with GOOPG_ONEREL_SEARCH=on on the DEFAULT arm. The
// defect is route-borne, not arm-borne, which is why both routes are pinned.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func oneRelIndexCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	outer, err := cat.CreateTable(parser.ObjectName{Name: "ori_outer"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "q", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = outer
	inner, err := cat.CreateTable(parser.ObjectName{Name: "ori_inner"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "ori_inner_k"}, inner,
		[]string{"k"}, false, "btree", true); err != nil {
		t.Fatal(err)
	}
	return cat
}

// TestOneRelIndexProducerKeepsCorrelatedScalarProbe is the Q17 pin, in
// miniature: the correlated scalar body is a single-relation scope whose only
// restriction is the correlation itself. On every route the body must reach an
// index path, and the statement must therefore KEEP its correlated subquery
// rather than decorrelate into a grouped whole-table aggregate — which is what
// PG 18.3 does (it has no EXPR_SUBLINK conversion at all).
func TestOneRelIndexProducerKeepsCorrelatedScalarProbe(t *testing.T) {
	cat := oneRelIndexCatalog(t)
	const sql = `select q from ori_outer where q < (select avg(v) from ori_inner where k = ori_outer.k)`

	for _, tc := range []struct {
		name     string
		jointree bool
		oneRel   bool
	}{
		{"bypass-route", false, false},
		{"jointree-route", true, false},
		{"onerel-search-route", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(j bool) { jointreePipeline = j }(jointreePipeline)
			jointreePipeline = tc.jointree
			defer func(o bool) { oneRelSearch = o }(oneRelSearch)
			oneRelSearch = tc.oneRel

			node, err := Plan(parseOne(t, sql), cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			// The correlated scalar must survive as a subquery expression.
			// If the probe-cheap guard was fed a bare SeqScan body it would
			// have decorrelated instead, and no SubqueryExpr would remain.
			found := false
			walkPlanExprs(node, func(e Expr) {
				if _, ok := e.(*SubqueryExpr); ok {
					found = true
				}
			})
			if !found {
				t.Fatalf("correlated scalar was decorrelated on route %s — the body never reached its index path; tree: %s",
					tc.name, describePlanTree(node))
			}
		})
	}
}

// TestPlanIsBareSeqScanTreeIsFailClosed pins the helper's contract: it admits
// only the recognised wrappers over a *SeqScan, so an unrecognised shape makes
// the producer stand down rather than override a tree it cannot read.
func TestPlanIsBareSeqScanTreeIsFailClosed(t *testing.T) {
	scan := &SeqScan{}
	if !planIsBareSeqScanTree(scan) {
		t.Fatal("bare SeqScan must be admitted")
	}
	if !planIsBareSeqScanTree(&Filter{Child: scan}) {
		t.Fatal("Filter over SeqScan must be admitted")
	}
	if !planIsBareSeqScanTree(&Project{Child: scan}) {
		t.Fatal("Project over SeqScan must be admitted")
	}
	// An index path already elected: the producer must NOT fire.
	if planIsBareSeqScanTree(&Filter{Child: &IndexScan{}}) {
		t.Fatal("a tree that already reached an IndexScan must be refused")
	}
	// Unrecognised wrapper: fail closed.
	if planIsBareSeqScanTree(&Sort{Child: scan}) {
		t.Fatal("an unrecognised wrapper must be refused (fail-closed)")
	}
	if planIsBareSeqScanTree(nil) {
		t.Fatal("nil must be refused")
	}
}

// TestOneRelIndexProducerKeepsMultiConjunctCorrelatedProbe is the Q20 pin
// (M0145-0027), in miniature: the correlated scalar body's WHERE is the
// correlation AND a constant restriction. `planIndexScanFromWhere` declines a
// multi-conjunct WHERE on every route; the bypass gets its probe from
// `rewriteScanInputsWithSingleTablePredicates`, which the jointree/one-rel
// routes could not reach because the search split the constant qual into a
// searched leaf Filter and stranded the correlation above it.
func TestOneRelIndexProducerKeepsMultiConjunctCorrelatedProbe(t *testing.T) {
	cat := oneRelIndexCatalog(t)
	const sql = `select q from ori_outer where q < (select avg(v) from ori_inner where k = ori_outer.k and v > 3)`

	for _, tc := range []struct {
		name     string
		jointree bool
		oneRel   bool
	}{
		{"bypass-route", false, false},
		{"jointree-route", true, false},
		{"onerel-search-route", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(j bool) { jointreePipeline = j }(jointreePipeline)
			jointreePipeline = tc.jointree
			defer func(o bool) { oneRelSearch = o }(oneRelSearch)
			oneRelSearch = tc.oneRel

			node, err := Plan(parseOne(t, sql), cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			found := false
			walkPlanExprs(node, func(e Expr) {
				if _, ok := e.(*SubqueryExpr); ok {
					found = true
				}
			})
			if !found {
				t.Fatalf("correlated scalar was decorrelated on route %s — the multi-conjunct body never reached its index probe; tree: %s",
					tc.name, describePlanTree(node))
			}
		})
	}
}
