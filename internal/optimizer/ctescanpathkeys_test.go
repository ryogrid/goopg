package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCTEScanPathkeysConvertBodyOrdering pins M0146-0005b's port of PG's
// set_cte_pathlist / convert_subquery_pathkeys: a CTE whose body is sorted on
// (a, b, c) gives its scan the pathkeys of that ordering in THIS query's
// expressions — the join-clause expressions for a and b — and stops at c,
// which nothing in this query names (an ordering prefix is still true, a
// gapped one is not).
func TestCTEScanPathkeysConvertBodyOrdering(t *testing.T) {
	cat := catalog.NewInMemory()
	int4 := catalog.Type{Name: "int4"}
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: int4}, {Name: "b", Type: int4}, {Name: "c", Type: int4},
	})
	if err != nil {
		t.Fatal(err)
	}
	sch := tableSchema(tbl)
	col := func(i int, name string) *ColumnRef { return &ColumnRef{Index: i, Name: name, Type: int4} }
	body := &Sort{Child: &SeqScan{Table: tbl, schema: sch}, Keys: []SortKey{
		{Expr: col(0, "a")}, {Expr: col(1, "b")}, {Expr: col(2, "c")},
	}}
	scan := &CTEScan{Name: "v", Child: body, schema: sch}

	// Binding coordinates: the CTE scan at [0,3), a second relation at [3,6).
	spans := []leafSpan{{0, 3}, {3, 6}}
	eq := func(l, r *ColumnRef) Expr { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
	s := &searchCtx{clauses: buildRestrictInfos([]Expr{
		eq(col(0, "a"), col(3, "x")),
		eq(col(1, "b"), col(4, "y")),
	}, 0, spans)}
	rel := &RelOptInfo{Relids: RelSet(1)}

	keys := s.cteScanPathkeys(rel, 99, scan)
	if len(keys) != 2 {
		t.Fatalf("got %d pathkeys, want 2 (a, b — c is unreferenced here): %v", len(keys), keys)
	}
	for i, want := range []string{"a", "b"} {
		cr, ok := keys[i].Expr.(*ColumnRef)
		if !ok || cr.Name != want || cr.Index != i || !keys[i].SortAsc {
			t.Errorf("pathkey %d = %+v, want ascending %s at binding index %d", i, keys[i], want, i)
		}
	}

	// A body ordered first on a column nothing references gives no ordering.
	body.Keys = []SortKey{{Expr: col(2, "c")}, {Expr: col(0, "a")}}
	if keys := s.cteScanPathkeys(rel, 99, scan); len(keys) != 0 {
		t.Errorf("leading unmapped key must yield no pathkeys, got %v", keys)
	}
}
