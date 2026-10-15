package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestForeignKeyMatchesThroughEquivalenceClass pins M0146-0202 on TPC-H Q9's
// shape. lineitem (rel 0) carries lineitem_partsupp_fk (l_partkey, l_suppkey)
// -> partsupp (rel 1); supplier is rel 2. At lineitem | {partsupp, supplier}
// the join applies one clause per class: `l_partkey = ps_partkey` and
// `l_suppkey = s_suppkey`. PG's get_foreign_key_join_selectivity consumes any
// clause of the FK column's class (costsize.c:5718-5733), so the FK still
// fires at 1/|partsupp|. goopg paired `l_suppkey = s_suppkey` with supplier,
// found the FK uncovered and fell back to eqjoinsel (1/200000 x 1/10000).
func TestForeignKeyMatchesThroughEquivalenceClass(t *testing.T) {
	c := catalog.NewInMemory()
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: 200000}, catalog.ColumnStats{NDistinct: 10000})
	lineitem.ForeignKeys = []catalog.ForeignKey{{
		Name: "lineitem_partsupp_fk", Columns: []string{"l_partkey", "l_suppkey"},
		RefTable: "partsupp", RefColumns: []string{"ps_partkey", "ps_suppkey"},
	}}
	partsupp := jsTable(t, c, "partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 800000, catalog.ColumnStats{NDistinct: 200000}, catalog.ColumnStats{NDistinct: 10000})
	supplier := jsTable(t, c, "supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}},
	}, 10000, catalog.ColumnStats{NDistinct: 10000})

	s, err := newSearchCtx(3, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: lineitem, baseRows: 6000000},
		{table: partsupp, baseRows: 800000},
		{table: supplier, baseRows: 10000},
	}
	eq := func(lrel int, lname string, rrel int, rname string, ec int) *restrictInfo {
		l, r := jsCol(0, lname), jsCol(9, rname)
		return &restrictInfo{
			clause: &BinaryOp{Op: parser.OpEq, Left: l, Right: r},
			relids: relsetOf(lrel) | relsetOf(rrel), leftKey: l, rightKey: r,
			leftRelids: relsetOf(lrel), rightRelids: relsetOf(rrel), isEquijoin: true, ecID: ec,
		}
	}
	partkey := eq(0, "l_partkey", 1, "ps_partkey", 0)
	suppS := eq(0, "l_suppkey", 2, "s_suppkey", 1)
	suppPS := eq(0, "l_suppkey", 1, "ps_suppkey", 1)
	s.clauses = &restrictInfoList{all: []*restrictInfo{partkey, suppS, suppPS}}

	outer := newRelOptInfo(relsetOf(0), 6000000, 40)
	inner := newRelOptInfo(relsetOf(1)|relsetOf(2), 13034, 40)
	est := s.superkeyJoinSelectivity(c, outer, inner, []*restrictInfo{partkey, suppS}, parser.JoinInner)
	if !est.fired || len(est.residual) != 0 || est.sel != 1.0/800000.0 {
		t.Fatalf("EC-matched FK = %+v; want both applied clauses consumed at 1/|partsupp|", est)
	}

	// Without the class stand-in the FK is uncovered: the supplier clause
	// alone pairs lineitem with supplier.
	s.clauses = &restrictInfoList{all: []*restrictInfo{partkey, suppS}}
	if est := s.superkeyJoinSelectivity(c, outer, inner, []*restrictInfo{partkey, suppS}, parser.JoinInner); est.fired {
		t.Fatalf("no class member pairs l_suppkey with partsupp, yet the FK fired: %+v", est)
	}
}
