package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Q78-shaped catalog: web_sales LEFT JOIN web_returns with disjoint
// prefixed columns (TPC-DS naming convention), so every unqualified ref
// has a unique owner.
func unqualifiedDemotionCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols ...string) {
		var cs []catalog.Column
		for _, n := range cols {
			cs = append(cs, catalog.Column{Name: n, Type: catalog.Type{Name: "int4"}})
		}
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, cs); err != nil {
			t.Fatal(err)
		}
	}
	mk("web_sales", "ws_order_number", "ws_item_sk", "ws_sold_date_sk")
	mk("web_returns", "wr_order_number", "wr_item_sk")
	mk("date_dim", "d_date_sk", "d_year")
	return c
}

func unqualCol(col string) *parser.ColumnRef {
	return &parser.ColumnRef{Column: col}
}

func TestReduceOuterJoinsLeftToAntiUnqualified(t *testing.T) {
	// Q78 CTE-body shape, all refs unqualified:
	//   web_sales LEFT JOIN web_returns
	//     ON wr_order_number=ws_order_number AND ws_item_sk=wr_item_sk
	//   WHERE wr_order_number IS NULL
	// → unique ownership resolves wr_* to web_returns → ANTI.
	cat := unqualifiedDemotionCatalog(t)
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "web_sales"},
		Joins: []parser.JoinExpr{
			{
				Type:  parser.JoinLeft,
				Right: parser.RangeVar{Name: "web_returns"},
				On: &parser.BinaryOp{
					Op: parser.OpAnd,
					Left: &parser.BinaryOp{
						Op:    parser.OpEq,
						Left:  unqualCol("wr_order_number"),
						Right: unqualCol("ws_order_number"),
					},
					Right: &parser.BinaryOp{
						Op:    parser.OpEq,
						Left:  unqualCol("ws_item_sk"),
						Right: unqualCol("wr_item_sk"),
					},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{Operand: unqualCol("wr_order_number")}

	reduceOuterJoins(from, where, cat)

	if got := from[0].Joins[0].Type; got != parser.JoinAnti {
		t.Errorf("unqualified LEFT JOIN + IS NULL on uniquely-owned nullable-side column: got %v, want JoinAnti", got)
	}
}

func TestReduceOuterJoinsUnqualifiedAmbiguousNoDemotion(t *testing.T) {
	// Both sides own `k` → the unqualified IS NULL cannot attribute →
	// no demotion (conservative = old behavior).
	c := catalog.NewInMemory()
	for _, name := range []string{"a", "b"} {
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: "k", Type: catalog.Type{Name: "int4"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type:  parser.JoinLeft,
				Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "k"},
					Right: &parser.ColumnRef{Table: "b", Column: "k"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{Operand: unqualCol("k")}

	reduceOuterJoins(from, where, c)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("ambiguous unqualified IS NULL must not demote: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsUnqualifiedUnknownColumnNoDemotion(t *testing.T) {
	// IS NULL on a column no scope table owns → skip → no demotion.
	cat := unqualifiedDemotionCatalog(t)
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "web_sales"},
		Joins: []parser.JoinExpr{
			{
				Type:  parser.JoinLeft,
				Right: parser.RangeVar{Name: "web_returns"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  unqualCol("wr_order_number"),
					Right: unqualCol("ws_order_number"),
				},
			},
		},
	}}
	where := &parser.IsNullExpr{Operand: unqualCol("no_such_column")}

	reduceOuterJoins(from, where, cat)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("unresolvable unqualified IS NULL must not demote: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsQualifiedWithCatalog(t *testing.T) {
	// Qualified refs keep working once a real catalog is threaded through
	// (existing S9.3 tests only cover cat == nil).
	cat := unqualifiedDemotionCatalog(t)
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "web_sales"},
		Joins: []parser.JoinExpr{
			{
				Type:  parser.JoinLeft,
				Right: parser.RangeVar{Name: "web_returns"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "web_sales", Column: "ws_order_number"},
					Right: &parser.ColumnRef{Table: "web_returns", Column: "wr_order_number"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "web_returns", Column: "wr_order_number"},
	}

	reduceOuterJoins(from, where, cat)

	if got := from[0].Joins[0].Type; got != parser.JoinAnti {
		t.Errorf("qualified LEFT JOIN + IS NULL with catalog: got %v, want JoinAnti", got)
	}
}

func TestReduceOuterJoinsLeftToInnerUnqualified(t *testing.T) {
	// Unqualified strict WHERE on the nullable side demotes LEFT→INNER:
	//   web_returns LEFT JOIN web_sales ON ... WHERE ws_order_number = 5
	// (mirrors the outer-query `ss_sold_year = 1998` pattern).
	cat := unqualifiedDemotionCatalog(t)
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "web_returns"},
		Joins: []parser.JoinExpr{
			{
				Type:  parser.JoinLeft,
				Right: parser.RangeVar{Name: "web_sales"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  unqualCol("wr_order_number"),
					Right: unqualCol("ws_order_number"),
				},
			},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  unqualCol("ws_order_number"),
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where, cat)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("unqualified strict WHERE on nullable side: got %v, want JoinInner", got)
	}
}
