package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestParamJoinFilterClausesRegenerateAgainstTheOuterInput is M0146-0135:
// get_joinrel_parampathinfo (relnode.c) keeps, for a hash join of an
// unparameterised probe side X and a hashed side Y parameterised by Z, the
// class's clause `Z.Z = X.X` — generated directly or regenerated after the
// `Z.Z = Y.Y` form is dropped as movable into Y. TPC-DS Q95: Z = ws1, X =
// ws_wh_1, Y = web_returns probed by ws1, so the join filters
// `ws1.ws_order_number = ws_wh_1.ws_order_number`. A parameterised outer
// declines (PG's pick then depends on member order).
func TestParamJoinFilterClausesRegenerateAgainstTheOuterInput(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	ws1 := &ColumnRef{Index: 0, Name: "ws_order_number", Type: int4}
	wh1 := &ColumnRef{Index: 1, Name: "ws_order_number", Type: int4}
	wr := &ColumnRef{Index: 2, Name: "wr_order_number", Type: int4}
	probeClause := &restrictInfo{isEquijoin: true, ecID: 0,
		leftKey: ws1, leftRelids: 0b001, rightKey: wr, rightRelids: 0b100, relids: 0b101}
	hashClause := &restrictInfo{isEquijoin: true, ecID: 0,
		leftKey: wh1, leftRelids: 0b010, rightKey: wr, rightRelids: 0b100, relids: 0b110}
	s := &searchCtx{clauses: &restrictInfoList{all: []*restrictInfo{probeClause, hashClause}, nclasses: 1}}
	outer := &RelOptInfo{Relids: 0b010}
	o := &Path{Kind: PathPrebuilt}
	i := &Path{Kind: PathIndexScan, RequiredOuter: 0b001, IndexClauses: []indexPathClause{{ri: probeClause}}}

	got := s.paramJoinFilterClauses(outer, o, i, 0b001)
	if len(got) != 1 {
		t.Fatalf("got %d ppi clauses, want 1", len(got))
	}
	if !exprEqual(got[0].reqKey, ws1) || !exprEqual(got[0].local, wh1) {
		t.Fatalf("ppi clause = %v = %v, want ws1 = ws_wh_1", got[0].reqKey, got[0].local)
	}
	po := &Path{Kind: PathIndexScan, RequiredOuter: 0b001}
	if got := s.paramJoinFilterClauses(outer, po, i, 0b001); got != nil {
		t.Fatalf("a parameterised outer derived %d clauses, want none", len(got))
	}
	// A class with no member on the probe side generates nothing.
	if got := s.paramJoinFilterClauses(&RelOptInfo{Relids: 0b1000}, o, i, 0b001); got != nil {
		t.Fatalf("an outer outside the class derived %d clauses, want none", len(got))
	}
}
