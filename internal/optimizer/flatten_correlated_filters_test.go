package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0145-0027: flattenCorrelatedSeqScanFilters is the knob-route repair that
// hands TPC-H Q20's correlated scalar body — `Filter{corr}(Filter_searched
// {local}(SeqScan))` — back to the bypass shape `Filter{all}(SeqScan)` so
// `rewriteScanInputsWithSingleTablePredicates` can build the correlated index
// probe. The cases pin its fail-closed contract: it fires ONLY on a stacked
// filter chain directly over one SeqScan that carries a correlated conjunct.
func TestFlattenCorrelatedSeqScanFilters(t *testing.T) {
	col := func(i int) Expr { return &ColumnRef{Index: i, Name: "c"} }
	corrEq := func() Expr {
		return &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &OuterColumnRef{Level: 1, Index: 0, Name: "o"}}
	}
	constLt := func() Expr {
		return &BinaryOp{Op: parser.OpLt, Left: col(1), Right: &IntegerConst{Value: 5}}
	}

	t.Run("correlated conjunct stranded above searched leaf flattens", func(t *testing.T) {
		ss := &SeqScan{}
		leaf := &Filter{Child: ss, Predicate: constLt(), LeafLocal: true}
		markSearchedTree(leaf)
		top := &Filter{Child: leaf, Predicate: corrEq()}
		got, ok := flattenCorrelatedSeqScanFilters(top)
		if !ok {
			t.Fatal("expected the stacked correlated chain to flatten")
		}
		f, isF := got.(*Filter)
		if !isF || f.Child != Node(ss) {
			t.Fatalf("want Filter directly over the original SeqScan, got %T", got)
		}
		if isSearchedTree(f) || f.LeafLocal {
			t.Fatal("the flattened Filter must be an unsearched, non-leaf-local wrapper (the bypass shape)")
		}
		conjs := splitAnd(f.Predicate)
		if len(conjs) != 2 || !exprHasOuterRef(conjs[0]) || exprHasOuterRef(conjs[1]) {
			t.Fatalf("want [correlated, local] conjuncts in outer-first order, got %d conjuncts", len(conjs))
		}
	})

	t.Run("no correlated conjunct declines", func(t *testing.T) {
		leaf := &Filter{Child: &SeqScan{}, Predicate: constLt(), LeafLocal: true}
		top := &Filter{Child: leaf, Predicate: constLt()}
		if _, ok := flattenCorrelatedSeqScanFilters(top); ok {
			t.Fatal("a chain with no outer reference must keep the search's election")
		}
	})

	t.Run("single filter is already the bypass shape", func(t *testing.T) {
		top := &Filter{Child: &SeqScan{}, Predicate: corrEq()}
		if _, ok := flattenCorrelatedSeqScanFilters(top); ok {
			t.Fatal("Filter{SeqScan} needs no flattening")
		}
	})

	t.Run("a non-Filter node in the chain declines", func(t *testing.T) {
		proj := &Project{Child: &SeqScan{}}
		top := &Filter{Child: &Filter{Child: proj, Predicate: constLt()}, Predicate: corrEq()}
		if _, ok := flattenCorrelatedSeqScanFilters(top); ok {
			t.Fatal("a Project between the filters and the scan changes coordinates; must decline")
		}
	})
}
