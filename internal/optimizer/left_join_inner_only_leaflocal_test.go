package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestLeftJoinInnerOnlyConjunctFilterIsLeafLocal pins the
// tpch/Q13-regression fix: the Filter wrapper that planFromItem
// builds for a LEFT JOIN's inner-only ON-clause conjuncts (the
// `customer LEFT JOIN orders ON c_custkey = o_custkey AND o_comment
// NOT LIKE '%special%requests%'` shape) carries leaf-local
// ColumnRef.Index values (relative to the inner side's own output
// schema), not FROM-cumulative ones. Without LeafLocal=true, the
// post-rewrite posMap passes (applyJoinTreePosMap /
// remapPosMapAfterRewrite, run via remapWithBindings /
// remapExprRefsToMHJ after rewriteJoinsToNLI) mistake the
// already-local index for a stale FROM-cumulative offset and remap
// it a second time — corrupting it. Concretely, with customer(8
// cols) LEFT JOIN orders(9 cols, o_comment last at local index 8),
// the bug remapped o_comment's correct local index 8 down to 0,
// silently resolving to o_orderdate (a Time value) instead and
// blowing up `NOT LIKE` with a Kind mismatch (42883).
func TestLeftJoinInnerOnlyConjunctFilterIsLeafLocal(t *testing.T) {
	cat := catalog.NewInMemory()
	cust, err := cat.CreateTable(parser.ObjectName{Name: "customer"}, []catalog.Column{
		{Name: "c_custkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
		{Name: "c_mktsegment", Type: catalog.Type{Name: "bpchar", Args: []int64{10}}},
		{Name: "c_nationkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "c_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
		{Name: "c_address", Type: catalog.Type{Name: "varchar", Args: []int64{40}}},
		{Name: "c_phone", Type: catalog.Type{Name: "bpchar", Args: []int64{15}}},
		{Name: "c_acctbal", Type: catalog.Type{Name: "numeric"}},
		{Name: "c_comment", Type: catalog.Type{Name: "varchar", Args: []int64{117}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "customer_pk"}, cust, []string{"c_custkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT the canonical TPC-H column order (o_orderdate
	// first, o_comment last) — this is the exact shape that
	// surfaced the bug: a wrong-by-8 index silently lands on a
	// same-table column of a DIFFERENT, incompatible type (Time vs
	// String) instead of erroring loudly.
	orders, err := cat.CreateTable(parser.ObjectName{Name: "orders"}, []catalog.Column{
		{Name: "o_orderdate", Type: catalog.Type{Name: "timestamp"}},
		{Name: "o_orderkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
		{Name: "o_custkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
		{Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar", Args: []int64{15}}},
		{Name: "o_shippriority", Type: catalog.Type{Name: "numeric"}},
		{Name: "o_clerk", Type: catalog.Type{Name: "bpchar", Args: []int64{15}}},
		{Name: "o_orderstatus", Type: catalog.Type{Name: "bpchar", Args: []int64{1}}},
		{Name: "o_totalprice", Type: catalog.Type{Name: "numeric"}},
		{Name: "o_comment", Type: catalog.Type{Name: "varchar", Args: []int64{79}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "orders_pk"}, orders, []string{"o_orderkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "order_customer_fkidx"}, orders, []string{"o_custkey"}, false, "btree", true); err != nil {
		t.Fatal(err)
	}

	stmt := parseOne(t, `select c_custkey, count(o_orderkey) as c_count from customer left outer join orders on c_custkey = o_custkey and o_comment not like '%special%requests%' group by c_custkey`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	f := findFilterReferencingColumn(t, node, "o_comment")
	if f == nil {
		t.Fatalf("expected a Filter wrapper referencing o_comment somewhere in the plan; tree: %s", describePlanTree(node))
	}
	if !f.LeafLocal {
		t.Fatalf("Filter wrapping the LEFT JOIN's inner-only o_comment conjunct must be LeafLocal=true (M0077-0001 convention), got false")
	}
	bop, ok := f.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("Filter.Predicate = %T, want *BinaryOp", f.Predicate)
	}
	cr, ok := bop.Left.(*ColumnRef)
	if !ok {
		t.Fatalf("Filter.Predicate.Left = %T, want *ColumnRef", bop.Left)
	}
	// The index must resolve o_comment against the Filter's OWN
	// child output schema (leaf-local), not the FROM-cumulative
	// customer++orders schema.
	childSchema := f.Child.Output()
	if cr.Index < 0 || cr.Index >= len(childSchema) || childSchema[cr.Index].Name != "o_comment" {
		t.Fatalf("o_comment ColumnRef.Index=%d resolves to %v in the Filter's child schema %#v; want it to resolve to \"o_comment\"",
			cr.Index, safeSchemaColName(childSchema, cr.Index), childSchema)
	}
}

func safeSchemaColName(s Schema, idx int) string {
	if idx < 0 || idx >= len(s) {
		return "<out of range>"
	}
	return s[idx].Name
}

// findFilterReferencingColumn walks the plan tree looking for a
// *Filter whose Predicate directly references a ColumnRef named
// `name` (by schema-name lookup against the Filter's own child
// output, since the whole point under test is whether Index is
// leaf-local or FROM-cumulative).
func findFilterReferencingColumn(t *testing.T, n Node, name string) *Filter {
	t.Helper()
	if n == nil {
		return nil
	}
	switch x := n.(type) {
	case *Filter:
		if bop, ok := x.Predicate.(*BinaryOp); ok {
			if cr, ok := bop.Left.(*ColumnRef); ok && cr.Name == name {
				return x
			}
		}
		return findFilterReferencingColumn(t, x.Child, name)
	case *Project:
		return findFilterReferencingColumn(t, x.Child, name)
	case *Sort:
		return findFilterReferencingColumn(t, x.Child, name)
	case *Limit:
		return findFilterReferencingColumn(t, x.Child, name)
	case *Aggregate:
		return findFilterReferencingColumn(t, x.Child, name)
	case *Join:
		if f := findFilterReferencingColumn(t, x.Left, name); f != nil {
			return f
		}
		return findFilterReferencingColumn(t, x.Right, name)
	case *NestedLoopIndexJoin:
		if f := findFilterReferencingColumn(t, x.Outer, name); f != nil {
			return f
		}
		return findFilterReferencingColumn(t, x.Inner, name)
	}
	return nil
}
