package planner

// M0127-P5.9-f — the decorrelated aggregate is a foreign coordinate scope.
//
// TPC-H Q17's WHERE carries a correlated scalar aggregate
// (`l_quantity < (select 0.2*avg(l_quantity) from lineitem where l_partkey =
// p_partkey)`), so `whereEligibleForPreDPUnnest` (predp.go) declines the
// pre-DP position and the legacy order runs: join search FIRST, then
// `unnestSubquery` (unnest.go) splices a hash join on top whose INNER side is
// a HashAggregate over a CLONE of lineitem.
//
// That splice puts a second planning scope inside the join tree, and two
// independent defects lived on the seam. Both were invisible with
// `GOOPG_PGSHAPED_DP` off and both broke Q17 with it on, so this file plans the
// shape at BOTH flag settings and asserts the two trees agree on every
// coordinate that matters.
//
// Defect 1 — `buildBindingsPosMap`'s `collect` DESCENDED into the decorrelated
// `*Aggregate` (bushy.go) while `applyJoinTreePosMap` has always STOPPED at
// one. With the flag on, the outer side is a searched subtree and records no
// scan entries, which left the aggregate's lineitem clone as the first and only
// `lineitem` entry — at offset 25. Every outer lineitem binding was then
// remapped to `25 + col`, and the residual's `l_quantity/4` became
// `l_quantity/29` against a 27-wide composed slot. Execution raised
// "column ref l_quantity/29 out of VirtualSlot range 27". Flag OFF hid it only
// by accident: the untagged outer join recorded `lineitem` at offset 0 first,
// and "first occurrence wins" discarded the clone.
//
// Defect 2 — the splice's `RightKey` was built with the inner-relative index
// `0` while its `Predicate` and `LeftKey` used merged coordinates. The executor
// evaluates both keys against a MERGED slot (`mergedKeySlot`,
// operators_join_agg.go), so `0` addressed the outer side's first column: Q17
// hashed `part.p_partkey` against `lineitem.l_orderkey` and the join matched
// nothing. `reresolveJoinByName` silently repaired it on every path that
// reached it, which is why it never showed — until defect 1's fix made the
// remap decline on this shape and that pass stopped running.
//
// Live confirmation on a 3000-row Q17 fixture (bench-free, port 5533): before,
// flag ON errored; after defect 1's fix alone, flag ON returned 0 rows against
// the flag-OFF control's 5; with both, the two arms return identical rows and
// an identical `avg_yearly`. Design: leftdeep-joins/09 §5.21.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jsuQ17SQL is TPC-H Q17 verbatim (bench/tpch query text, whitespace-folded).
const jsuQ17SQL = `select sum(l_extendedprice) / 7.0 as avg_yearly ` +
	`from lineitem, part ` +
	`where p_partkey = l_partkey and p_brand = 'Brand#23' and p_container = 'MED BOX' ` +
	`and l_quantity < (select 0.2 * avg(l_quantity) from lineitem where l_partkey = p_partkey)`

// jsuCatalog builds Q17's two relations at their real TPC-H widths — 16 and 9.
// The widths are the point: the reported indexes (4, 16, 25, 26) are only
// meaningful against lineitem++part == 25 columns, and the row counts are far
// enough apart that the search's choice is not a coin flip.
func jsuCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cs := make([]catalog.Column, len(cols))
		for i, c := range cols {
			cs[i] = catalog.Column{Name: c, Type: catalog.Type{Name: "int4"}}
		}
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cs)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true}
	}
	mk("lineitem", 3000,
		"l_orderkey", "l_partkey", "l_suppkey", "l_linenumber", "l_quantity",
		"l_extendedprice", "l_discount", "l_tax", "l_returnflag", "l_linestatus",
		"l_shipdate", "l_commitdate", "l_receiptdate", "l_shipinstruct", "l_shipmode",
		"l_comment")
	mk("part", 200,
		"p_partkey", "p_name", "p_mfgr", "p_brand", "p_type",
		"p_size", "p_container", "p_retailprice", "p_comment")
	return cat
}

// jsuPlan plans one statement with `GOOPG_PGSHAPED_DP` forced on or off.
func jsuPlan(t *testing.T, on bool) Node {
	t.Helper()
	saved := pgShapedDP
	pgShapedDP = on
	defer func() { pgShapedDP = saved }()

	stmts, err := parser.Parse(jsuQ17SQL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	n, err := Plan(stmts[0], jsuCatalog(t))
	if err != nil {
		t.Fatalf("plan (pgshaped=%v): %v", on, err)
	}
	return n
}

// jsuUnnestJoin returns the join `unnestSubquery` spliced in — the topmost
// *Join whose Right child is an *Aggregate — together with the *Filter that
// carries the rewritten residual (`l_quantity < 0.2 * avg`).
func jsuUnnestJoin(t *testing.T, n Node) (*Filter, *Join) {
	t.Helper()
	var resid *Filter
	var walk func(Node) *Join
	walk = func(cur Node) *Join {
		switch x := cur.(type) {
		case nil:
			return nil
		case *Project:
			return walk(x.Child)
		case *Sort:
			return walk(x.Child)
		case *Aggregate:
			return walk(x.Child)
		case *Filter:
			if j, ok := x.Child.(*Join); ok {
				if _, isAgg := j.Right.(*Aggregate); isAgg {
					resid = x
					return j
				}
			}
			return walk(x.Child)
		case *Join:
			if _, isAgg := x.Right.(*Aggregate); isAgg {
				return x
			}
			return walk(x.Left)
		}
		return nil
	}
	j := walk(n)
	if j == nil {
		t.Fatalf("no decorrelated join (Join with *Aggregate on the Right) in the plan")
	}
	if resid == nil {
		t.Fatalf("decorrelated join has no residual *Filter above it")
	}
	return resid, j
}

// jsuColRefIndex returns the Index of the single ColumnRef named `name`
// anywhere inside e, or -1 when it is absent (and fails on duplicates, which
// would make the assertion ambiguous).
func jsuColRefIndex(t *testing.T, e Expr, name string) int {
	t.Helper()
	found := -1
	var walk func(Expr)
	walk = func(x Expr) {
		switch v := x.(type) {
		case nil:
			return
		case *ColumnRef:
			if v.Name == name {
				if found >= 0 && found != v.Index {
					t.Fatalf("column %q appears at two different indexes (%d and %d)", name, found, v.Index)
				}
				found = v.Index
			}
		case *BinaryOp:
			walk(v.Left)
			walk(v.Right)
		case *UnaryOp:
			walk(v.Operand)
		}
	}
	walk(e)
	return found
}

// TestQ17DecorrelatedAggregateCoordinates is the P5.9-f bar in plan form: at
// BOTH flag settings the decorrelated join must publish the same coordinates,
// because both defects were flag-visible only and the flag must not change
// where a column lives.
func TestQ17DecorrelatedAggregateCoordinates(t *testing.T) {
	const (
		outerWidth = 25 // lineitem(16) ++ part(9)
		mergedWide = 27 // ++ the aggregate's (l_partkey, avg)
	)
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("pgshaped=%v", on), func(t *testing.T) {
			resid, j := jsuUnnestJoin(t, jsuPlan(t, on))

			if got := len(j.Output()); got != mergedWide {
				t.Fatalf("decorrelated join width = %d, want %d", got, mergedWide)
			}
			if got := len(j.Left.Output()); got != outerWidth {
				t.Fatalf("outer side width = %d, want %d", got, outerWidth)
			}

			// Defect 1: the residual is in OUTER coordinates. l_quantity is
			// lineitem's column 4 and lineitem is the first binding, so 4 is
			// the only correct answer. The bug returned 29 (== 25 + 4), an
			// index past the 27-wide composed slot.
			if got := jsuColRefIndex(t, resid.Predicate, "l_quantity"); got != 4 {
				t.Errorf("residual l_quantity index = %d, want 4 "+
					"(a %d-wide slot cannot address 25+4; see P5.9-f defect 1)", got, mergedWide)
			}
			// The aggregate's result sits at outerWidth+1, right after the
			// group-by column it is keyed on.
			if got := jsuColRefIndex(t, resid.Predicate, "avg"); got != outerWidth+1 {
				t.Errorf("residual avg index = %d, want %d", got, outerWidth+1)
			}

			// Defect 2: BOTH join keys are in merged coordinates, because
			// that is what the executor evaluates them against. RightKey is
			// the aggregate's group-by column at outerWidth; the old
			// inner-relative 0 silently addressed lineitem.l_orderkey.
			lk, ok := j.LeftKey.(*ColumnRef)
			if !ok {
				t.Fatalf("LeftKey is %T, want *ColumnRef", j.LeftKey)
			}
			if lk.Name != "p_partkey" || lk.Index != 16 {
				t.Errorf("LeftKey = %s/%d, want p_partkey/16", lk.Name, lk.Index)
			}
			rk, ok := j.RightKey.(*ColumnRef)
			if !ok {
				t.Fatalf("RightKey is %T, want *ColumnRef", j.RightKey)
			}
			if rk.Name != "l_partkey" || rk.Index != outerWidth {
				t.Errorf("RightKey = %s/%d, want l_partkey/%d "+
					"(merged coordinates, not inner-relative; see P5.9-f defect 2)",
					rk.Name, rk.Index, outerWidth)
			}

			// The key pair and the join Predicate must name the same two
			// columns — a single equi-pair, not two. Before the RightKey fix
			// `fillJoinHashKeys` derived a SECOND pair from the Predicate
			// (`p_partkey/16 = l_partkey/25`) alongside the bogus
			// `p_partkey/16 = l_partkey/0`, which EXPLAIN rendered as a
			// two-condition Hash Cond.
			if got := len(j.HashKeys); got > 1 {
				t.Errorf("decorrelated join has %d equi-pairs, want 1 "+
					"(a duplicate pair means the keys and the Predicate disagree)", got)
			}
			if got := jsuColRefIndex(t, j.Predicate, "p_partkey"); got != 16 {
				t.Errorf("join Predicate p_partkey index = %d, want 16", got)
			}
			if got := jsuColRefIndex(t, j.Predicate, "l_partkey"); got != outerWidth {
				t.Errorf("join Predicate l_partkey index = %d, want %d", got, outerWidth)
			}
		})
	}
}

// TestBuildBindingsPosMapStopsAtJoinInputAggregate is defect 1 stated directly
// against `buildBindingsPosMap`, independent of Q17's plan shape: an
// *Aggregate reached inside the walked tree is a distinct planning scope, so
// its scans must not enter `scanMap`. `applyJoinTreePosMap` has always
// returned at an *Aggregate ("aggregate expressions are a different scope"),
// and build and apply must stop at the same nodes — the rule that
// M0125-0012 (*Project) and RC-2 (*SetOp / *WindowAgg) already established.
func TestBuildBindingsPosMapStopsAtJoinInputAggregate(t *testing.T) {
	cat := jsuCatalog(t)
	lineitem, ok := cat.LookupTable(parser.ObjectName{Name: "lineitem"})
	if !ok {
		t.Fatalf("lookup lineitem: not found")
	}

	// A tree whose ONLY lineitem scan sits under an *Aggregate, at offset 25.
	// (Q17's exact situation once the searched outer side records no entry.)
	scan := &SeqScan{Table: lineitem}
	agg := &Aggregate{
		Child:  scan,
		schema: Schema{{Name: "l_partkey"}, {Name: "avg"}},
	}

	// Without the opacity, `collect` records lineitem at whatever offset the
	// aggregate occupies and every lineitem binding is remapped onto it. With
	// it, no entry is recorded, the map has nothing to say, and
	// buildBindingsPosMap declines — the identity, which is the truth.
	bindings := []rangeBinding{{table: lineitem, offset: 0}}
	if m := buildBindingsPosMap(agg, bindings); m != nil {
		t.Fatalf("buildBindingsPosMap descended into a join-input *Aggregate: "+
			"l_quantity/4 -> %d (want a nil map — the aggregate is a foreign scope)", m(4))
	}
}
