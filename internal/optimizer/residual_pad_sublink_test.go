package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestResidualColumnRefsByNameSublinkScopes pins M0145-0008e: the seam's
// residual pad check (searchedResidualHitsPad) must not treat an
// UNCORRELATED sublink's inner plan as an unwalkable residual. That plan
// reads nothing from the row the residual is evaluated on, so only its
// same-scope operand counts. TPC-H Q18's `o_orderkey IN (SELECT l_orderkey …
// HAVING …)` otherwise declined the whole join search (seam-decline
// reason=residual-hits-pad). A correlated plan still makes the walk partial,
// and the ordinary visitColumnRefsByName keeps its old answer for both.
func TestResidualColumnRefsByNameSublinkScopes(t *testing.T) {
	operand := &ColumnRef{Index: 0, Name: "o_orderkey"}
	uncorr := &InExpr{Operand: operand, Plan: &SeqScan{Table: &catalog.Table{Name: "lineitem"}}}
	corr := &InExpr{Operand: operand, Plan: &Filter{
		Child: &SeqScan{Table: &catalog.Table{Name: "lineitem"}},
		Predicate: &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "l_orderkey"},
			Right: &OuterColumnRef{Level: 1, Index: 3, Name: "o_custkey"}},
	}}

	var names []string
	collect := func(n string) { names = append(names, n) }

	if !residualColumnRefsByName(uncorr, collect) {
		t.Fatal("uncorrelated sublink: walk must be total")
	}
	if len(names) != 1 || names[0] != "o_orderkey" {
		t.Errorf("uncorrelated sublink: want only the operand o_orderkey, got %v", names)
	}
	if residualColumnRefsByName(corr, func(string) {}) {
		t.Error("correlated sublink: walk must stay partial (its outer refs are index-keyed)")
	}
	if visitColumnRefsByName(uncorr, func(string) {}) {
		t.Error("visitColumnRefsByName must still report any inner plan as partial")
	}
}
