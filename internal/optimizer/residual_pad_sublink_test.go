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
	// M0146-0005bs: a correlated plan reads the row only through its outer
	// references, which carry names — the walk is total and reports them
	// (TPC-DS Q35's exists-or-exists residual had fallen back on any padded
	// needed column). A nameless outer reference still makes it partial.
	var corrNames []string
	if !residualColumnRefsByName(corr, func(n string) { corrNames = append(corrNames, n) }) {
		t.Fatal("correlated sublink: walk must be total when every outer ref is named")
	}
	hasCust := false
	for _, n := range corrNames {
		if n == "o_custkey" {
			hasCust = true
		}
	}
	if !hasCust {
		t.Errorf("correlated sublink: want the outer ref o_custkey reported, got %v", corrNames)
	}
	nameless := &InExpr{Operand: operand, Plan: &Filter{
		Child: &SeqScan{Table: &catalog.Table{Name: "lineitem"}},
		Predicate: &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "l_orderkey"},
			Right: &OuterColumnRef{Level: 1, Index: 3}},
	}}
	if residualColumnRefsByName(nameless, func(string) {}) {
		t.Error("a nameless outer ref must keep the walk partial")
	}
	if visitColumnRefsByName(uncorr, func(string) {}) {
		t.Error("visitColumnRefsByName must still report any inner plan as partial")
	}
}
