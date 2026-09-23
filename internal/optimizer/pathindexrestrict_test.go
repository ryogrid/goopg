package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// restrictionEqualityPrefix binds a GAPLESS leading prefix of the index to the
// leaf's `col = const` conjuncts (btree amoptionalkey, build_index_paths): a
// bound second column behind an unbound first one is not an index qual, and a
// non-equality or boolean-constant conjunct never binds. M0145-0029 slice 1.
func TestRestrictionEqualityPrefix(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx.Name == "idx_item_sk_flag" {
			composite = idx
		}
	}
	if composite == nil {
		t.Fatal("idx_item_sk_flag missing")
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{Value: 7}, Right: col(2)} // const on the left
	gtSK := &BinaryOp{Op: parser.OpGt, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqBool := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &BooleanConst{Value: true}}

	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag, eqSK}); len(got) != 2 ||
		got[0].indexCol != 0 || got[0].local != Expr(eqSK) || got[1].indexCol != 1 || got[1].local != Expr(eqFlag) {
		t.Fatalf("both columns bound: want [sk, flag] in index order, got %+v", got)
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag}); len(got) != 0 {
		t.Fatalf("second column alone must not bind (gapless prefix), got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqSK}); len(got) != 1 {
		t.Fatalf("leading column alone is a 1-column prefix, got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{gtSK, eqBool}); len(got) != 0 {
		t.Fatalf("range / boolean-constant conjuncts must not bind as equality keys, got %d", len(got))
	}
}

// The lowering reinstates the leaf's Filter WITHOUT the conjuncts the probe
// already applies (PG's qpqual excludes quals redundant with the index quals),
// omitting a wrapper that is left empty and keeping LeafLocal.
func TestRewrapLeafDroppingRemovesConsumedConjuncts(t *testing.T) {
	a := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 1}}
	b := &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 1}, Right: &IntegerConst{Value: 5}}
	inner := &Filter{Child: &SeqScan{}, Predicate: &BinaryOp{Op: parser.OpAnd, Left: a, Right: b}, LeafLocal: true}
	outer := &Filter{Child: inner, Predicate: a, LeafLocal: true}
	scan := &IndexScan{}

	got := rewrapLeafDropping(outer, scan, map[Expr]bool{a: true})
	f, ok := got.(*Filter)
	if !ok || f.Child != Node(scan) || f.Predicate != Expr(b) || !f.LeafLocal {
		t.Fatalf("want one LeafLocal Filter{b} directly over the scan, got %T", got)
	}
	if got := rewrapLeafDropping(&Filter{Child: &SeqScan{}, Predicate: a}, scan, map[Expr]bool{a: true}); got != Node(scan) {
		t.Fatalf("a wrapper left empty must be omitted, got %T", got)
	}
}
