package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPulledSublinksFoldUnderJoinCollapseLimit pins M0146-0227 slice 2:
// pull_up_sublinks nests each converted sublink's JoinExpr over the previous
// one, and deconstruct_recurse folds every (larg, rarg) pair under
// join_collapse_limit. TPC-DS Q69 has three FROM relations and three
// two-relation sublink bodies: 3+2, 5+2 and 7+2 > 8, so PG plans the first
// seven relations as one sub-problem and joins the third body last. goopg
// appended every pulled leaf to one flat joinlist and searched nine relations.
func TestPulledSublinksFoldUnderJoinCollapseLimit(t *testing.T) {
	jl := joinlist{leafItem(0), leafItem(1), leafItem(2)}
	body := func(jt parser.JoinType) *jtPulledBody {
		return &jtPulledBody{jointype: jt, leafScans: []Node{&SeqScan{}, &SeqScan{}}}
	}
	bodies := []*jtPulledBody{body(parser.JoinSemi), body(parser.JoinAnti), body(parser.JoinAnti)}
	got, next := foldPulledBodyLeaves(jl, 3, bodies, 8)
	if next != 9 {
		t.Fatalf("next leaf = %d, want 9", next)
	}
	if len(got) != 2 || got[0].isLeaf() || got[1].isLeaf() {
		t.Fatalf("joinlist = %v, want two sub-problems (7 relations, then the third body)", got)
	}
	if n := got[0].sub.nrels(); n != 7 {
		t.Errorf("first sub-problem has %d relations, want 7", n)
	}
	if n := got[1].sub.nrels(); n != 2 || got[1].sub[0].rel != 7 {
		t.Errorf("second sub-problem = %v, want leaves 7 and 8", got[1].sub)
	}
	// Under the limit the bodies stay flat, as before.
	flat, _ := foldPulledBodyLeaves(joinlist{leafItem(0), leafItem(1), leafItem(2)}, 3, bodies[:2], 8)
	if len(flat) != 7 {
		t.Errorf("two bodies: joinlist has %d items, want the 7 flat leaves", len(flat))
	}
	// A nested pull-up keeps the flat append.
	nested := []*jtPulledBody{body(parser.JoinSemi), body(parser.JoinAnti), body(parser.JoinAnti)}
	nested[2].parent = nested[1]
	keep, _ := foldPulledBodyLeaves(joinlist{leafItem(0), leafItem(1), leafItem(2)}, 3, nested, 8)
	if len(keep) != 9 {
		t.Errorf("nested bodies: joinlist has %d items, want 9 flat leaves", len(keep))
	}
}

// TestSubproblemLeafVarKeepsBaseRelStats pins M0146-0227 slice 2's second
// half: a Var of a base relation inside a searched sub-problem leaf is still
// that relation's Var to examine_variable (find_base_rel), so a join clause
// against it reads the relation's statistics, tuples and rows. Q69's
// catalog_sales anti join, once catalog_sales sat in a join_collapse_limit
// sub-problem, otherwise sized its join with no statistics and halved PG's
// 91 rows.
func TestSubproblemLeafVarKeepsBaseRelStats(t *testing.T) {
	tbl := &catalog.Table{
		Name:    "catalog_sales",
		Columns: []catalog.Column{{Name: "cs_ship_customer_sk", Type: catalog.Type{Name: "int4"}}},
		Stats:   &catalog.TableStats{RowCount: 360397, Columns: []catalog.ColumnStats{{NDistinct: 43000}}},
	}
	s := jslCtx(t, 2)
	s.relInfos = make([]baseRelInfo, 2)
	s.relInfos[1] = baseRelInfo{bindingIdx: -1, sourceIdx: -1, baseRows: 447, filteredRows: 447,
		subproblemBaseVars: map[int16]subproblemBaseVar{7: {table: tbl, tuples: 360397, rows: 447}}}
	key := &ColumnRef{Name: "cs_ship_customer_sk", SourceTableIdx: 7, Type: catalog.Type{Name: "int4"}}
	v := s.examineJoinVar(key, rB)
	if v.stats == nil || v.stats.NDistinct != 43000 {
		t.Fatalf("stats = %+v, want catalog_sales' cs_ship_customer_sk statistics", v.stats)
	}
	if v.tuples != 360397 || v.rows != 447 {
		t.Errorf("tuples/rows = %v/%v, want the base relation's 360397/447", v.tuples, v.rows)
	}
}
