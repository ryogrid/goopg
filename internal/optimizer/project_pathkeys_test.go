package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestProjectEmissionPathkeysCarriesComputedOrdering pins M0146-0005ax
// (convert_subquery_pathkeys): rows sorted on an expression keep that order
// through a Project that computes the same expression as a column — TPC-DS
// Q51's `item_sk = CASE …` over the window input sorted on that CASE — so
// an ORDER BY on the column needs no second Sort. A key whose expression
// calls a function is not carried: the Project re-evaluates it.
func TestProjectEmissionPathkeysCarriesComputedOrdering(t *testing.T) {
	scan := scanWithStats("t", 100, 100)
	plus := func() Expr {
		return &BinaryOp{Op: parser.OpAdd, Left: jrCol(0), Right: &IntegerConst{Value: 1}}
	}
	int4 := catalog.Type{Name: "int4"}
	sorted := &Sort{Child: scan, Keys: []SortKey{{Expr: plus()}}}
	proj := &Project{Child: sorted, Targets: []Expr{jrCol(0), plus()},
		schema: Schema{{Name: "c", Type: int4}, {Name: "k", Type: int4}}}

	keys := inputNodePathkeys(proj)
	if len(keys) != 1 {
		t.Fatalf("want the Sort's ordering carried through the computing Project, got %v", keys)
	}
	if cr, ok := keys[0].Expr.(*ColumnRef); !ok || cr.Index != 1 || !keys[0].SortAsc {
		t.Fatalf("want ascending output column 1 (k), got %#v", keys[0])
	}

	call := func() Expr { return &FuncCall{Name: "random"} }
	volatile := &Project{Child: &Sort{Child: scan, Keys: []SortKey{{Expr: call()}}},
		Targets: []Expr{call()}, schema: Schema{{Name: "r", Type: catalog.Type{Name: "float8"}}}}
	if keys := inputNodePathkeys(volatile); len(keys) != 0 {
		t.Fatalf("a function-call key must not cross the Project, got %v", keys)
	}

	// The labelling wrapper publishes its subplan's rows position for
	// position, so the carried ordering survives it too.
	wrapped := &SubqueryScan{Alias: "y", Child: proj, schema: proj.Output()}
	if keys := inputNodePathkeys(wrapped); len(keys) != 1 {
		t.Fatalf("want the ordering carried through Subquery Scan, got %v", keys)
	}
}
