package optimizer

// S5a (D3.1) tests: sublink pull-up before join-order search with the
// decorrelated semi/anti joins pinned above the searched subtree.
//
// The F8 hazard these tests exist for: after tryBushyDP reorders the
// outer layout UNDER a pinned semi/anti join, the join's keys/predicate
// and any retained SubPlans' outer refs hold indices into the pre-DP
// layout — stale indices are silent wrong results, this repository's
// most expensive failure class.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// preDPCatalog builds four tables with ANALYZE stats shaped so that
// bushy DP prefers a join order DIFFERENT from the FROM order: the
// FROM order lists big tables first (big1, big2) and small ones last
// (small3, small4), while DP's cost model starts from the cheapest
// pairs — forcing a layout change below the pinned spine. inner_e is
// the EXISTS target (indexed correlation column).
func preDPCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column, rows int64, nds []int64) *catalog.Table {
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		cs := make([]catalog.ColumnStats, len(nds))
		for i, nd := range nds {
			cs[i] = catalog.ColumnStats{NDistinct: nd}
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: cs}
		return tbl
	}
	mk("big1", []catalog.Column{
		{Name: "b1_k", Type: catalog.Type{Name: "int4"}},
		{Name: "b1_j", Type: catalog.Type{Name: "int4"}},
	}, 100000, []int64{100000, 1000})
	mk("big2", []catalog.Column{
		{Name: "b2_k", Type: catalog.Type{Name: "int4"}},
		{Name: "b2_j", Type: catalog.Type{Name: "int4"}},
	}, 90000, []int64{90000, 1000})
	mk("small3", []catalog.Column{
		{Name: "s3_k", Type: catalog.Type{Name: "int4"}},
		{Name: "s3_j", Type: catalog.Type{Name: "int4"}},
	}, 10, []int64{10, 10})
	mk("small4", []catalog.Column{
		{Name: "s4_k", Type: catalog.Type{Name: "int4"}},
		{Name: "s4_j", Type: catalog.Type{Name: "int4"}},
	}, 20, []int64{20, 20})
	inner, err := c.CreateTable(parser.ObjectName{Name: "inner_e"}, []catalog.Column{
		{Name: "e_k", Type: catalog.Type{Name: "int4"}},
		{Name: "e_v", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner.Stats = &catalog.TableStats{RowCount: 1000,
		Columns: []catalog.ColumnStats{{NDistinct: 500}, {NDistinct: 100}}}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "inner_e_k_ix"}, inner, []string{"e_k"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c
}

const preDPFourTableExists = "SELECT b1_k FROM big1, big2, small3, small4 " +
	"WHERE b1_j = b2_j AND b2_j = s3_j AND s3_k = s4_k " +
	"AND EXISTS (SELECT 1 FROM inner_e WHERE e_k = big1.b1_k)"

// findSpineSemi returns the topmost pinned semi/anti join reached by
// descending through Project/Sort/Filter wrappers. Returns either the
// hash-form *Join or, when the NLI rewrite converted it, nil with the
// *NestedLoopIndexJoin in the second value.
func findSpineSemi(node Node) (*Join, *NestedLoopIndexJoin) {
	for {
		switch x := node.(type) {
		case *Filter:
			node = x.Child
		case *Project:
			node = x.Child
		case *Sort:
			node = x.Child
		case *Join:
			if x.Type == JoinTypeSemi || x.Type == JoinTypeAnti {
				return x, nil
			}
			// K26: with implied join equalities the DP can legally place an
			// inner join ABOVE the pinned semi join, so the semi join is no
			// longer the first join on the spine. Descend both sides rather
			// than giving up — returning nil here made the caller nil-deref,
			// which reads as a planner crash and is a walker gap (K19 again,
			// fourth walker family).
			if l, n := findSpineSemi(x.Left); l != nil || n != nil {
				return l, n
			}
			return findSpineSemi(x.Right)
		case *NestedLoopIndexJoin:
			if x.Type == JoinTypeSemi || x.Type == JoinTypeAnti {
				return nil, x
			}
			return nil, nil
		default:
			return nil, nil
		}
	}
}

// spliceTestSchema is a 2-column schema builder for spliceSearchedSpine
// tests — bare SeqScan leaves stand in for "the outer schema a pinned
// Semi/Anti spine publishes" and "the schema a Phase B search returned",
// without needing a real join tree.
func spliceTestSchema(names ...string) Schema {
	s := make(Schema, len(names))
	for i, n := range names {
		s[i] = SchemaColumn{Name: n, Type: catalog.Type{Name: "int4"}}
	}
	return s
}

// planShapeString renders a stable structural signature of a plan for
// equality comparison (node types, join types, key/predicate exprs).
func planShapeString(n Node) string {
	var b strings.Builder
	var walk func(Node, int)
	walk = func(n Node, d int) {
		if n == nil {
			return
		}
		b.WriteString(strings.Repeat(" ", d))
		switch x := n.(type) {
		case *Filter:
			b.WriteString("Filter " + exprShape(x.Predicate) + "\n")
			walk(x.Child, d+1)
		case *Join:
			b.WriteString("Join type=" + joinTypeShape(x.Type) +
				" l=" + exprShape(x.LeftKey) + " r=" + exprShape(x.RightKey) +
				" p=" + exprShape(x.Predicate) + "\n")
			walk(x.Left, d+1)
			walk(x.Right, d+1)
		case *SeqScan:
			b.WriteString("SeqScan " + x.Table.Name + "\n")
		case *IndexScan:
			b.WriteString("IndexScan " + x.Table.Name + " key=" + exprShape(x.Key) + "\n")
		case *Project:
			b.WriteString("Project\n")
			walk(x.Child, d+1)
		case *Aggregate:
			b.WriteString("Aggregate\n")
			walk(x.Child, d+1)
		case *NestedLoopIndexJoin:
			b.WriteString("NLI type=" + joinTypeShape(x.Type) + " p=" + exprShape(x.Predicate) + "\n")
			walk(x.Outer, d+1)
			walk(x.Inner, d+1)
		default:
			b.WriteString("?\n")
		}
	}
	walk(n, 0)
	return b.String()
}

func joinTypeShape(t JoinType) string {
	switch t {
	case JoinTypeSemi:
		return "semi"
	case JoinTypeAnti:
		return "anti"
	case JoinTypeInner:
		return "inner"
	case JoinTypeCross:
		return "cross"
	}
	return "other"
}

func exprShape(e Expr) string {
	if e == nil {
		return "-"
	}
	var b strings.Builder
	var walk func(Expr)
	walk = func(e Expr) {
		switch x := e.(type) {
		case *ColumnRef:
			b.WriteString(x.Name)
			b.WriteString("#")
			b.WriteString(strings.Repeat("i", 0))
			b.WriteString(itoaShape(x.Index))
		case *BinaryOp:
			b.WriteString("(")
			walk(x.Left)
			b.WriteString(x.Op.String())
			walk(x.Right)
			b.WriteString(")")
		case *BooleanConst:
			if x.Value {
				b.WriteString("T")
			} else {
				b.WriteString("F")
			}
		default:
			b.WriteString("e")
		}
	}
	walk(e)
	return b.String()
}

func itoaShape(i int) string {
	if i < 0 {
		return "neg"
	}
	digits := "0123456789"
	if i < 10 {
		return string(digits[i])
	}
	return itoaShape(i/10) + string(digits[i%10])
}
