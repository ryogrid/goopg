package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005aj — where EXPLAIN puts a nested-loop index join's residual.
//
// PG enforces every join clause movable to a parameterized inner path INSIDE
// that inner scan: get_baserel_parampathinfo collects them as the path's
// ppi_clauses, and create_scan_plan appends them to the scan's qual, so they
// print as the inner scan's `Filter:` (after its own restriction quals), with
// the outer side's Vars as Params that deparse qualified — TPC-H Q21's
// `Filter: ((l_receiptdate > l_commitdate) AND (l_suppkey <> l1.l_suppkey))`
// under a Nested Loop Anti Join.
//
// goopg's parameterized nested loops (NestedLoopIndexJoin, and the Lateral
// nested-loop Join over a parameterized probe) already evaluate the residual
// Predicate at the same point: once per candidate row the parameterized probe returns for the
// current outer row, before the join emits (or, for SEMI/ANTI/LEFT, counts
// the row as a match). So the residual IS the probe's ppi_clauses, and the
// renderers attribute it to the inner scan when:
//
//   - every conjunct reads the inner relation (a conjunct naming only outer
//     columns is not movable into the inner rel and would stay a join qual);
//   - the inner is a bare parameterized scan (IndexScan / BitmapHeapScan /
//     IndexOnlyScan) with no Memoize between — with a cache, the residual
//     runs ABOVE the cache here, while PG would key the cache on the extra
//     outer values it reads.
//
// Rendering only: no plan field changes, so every pass that remaps the
// Predicate's coordinates keeps working unmodified.

// paramJoinParts returns the pieces the rule reads from either parameterized
// nested-loop form: the NestedLoopIndexJoin, or a Lateral nested-loop Join
// whose Right is the parameterized probe (the searched NLI R25 emits). ok is
// false for every other node.
func paramJoinParts(n optimizer.Node) (pred optimizer.Expr, outer, inner optimizer.Node, memo bool, ok bool) {
	switch p := n.(type) {
	case *optimizer.NestedLoopIndexJoin:
		return p.Predicate, p.Outer, p.Inner, p.InnerMemo != nil, true
	case *optimizer.Join:
		if p.Lateral && p.Algo == optimizer.JoinAlgoNestedLoop {
			return p.Predicate, p.Left, p.Right, false, true
		}
	}
	return nil, nil, nil, false, false
}

// innerParamQual returns n's residual when it renders as the inner scan's
// Filter, else nil (it then stays on the join line).
func innerParamQual(n optimizer.Node) optimizer.Expr {
	pred, outer, inner, memo, ok := paramJoinParts(n)
	if !ok || pred == nil || memo || outer == nil {
		return nil
	}
	switch inner.(type) {
	case *optimizer.IndexScan, *optimizer.BitmapHeapScan, *optimizer.IndexOnlyScan:
	default:
		return nil
	}
	outerW := len(outer.Output())
	probeRels := probeRelations(inner, outer)
	for _, c := range splitAndExpr(pred) {
		// An inner = outer column equality is an equivalence-class clause:
		// PG's ppi_clauses carry ONE clause per equivalence class
		// (generate_join_implied_equalities for the inner rel, the one the
		// index probe uses), and the join re-checks any other member pair
		// as a Join Filter — TPC-H Q9's `supplier.s_suppkey =
		// lineitem.l_suppkey`, Q21's `orders.o_orderkey = l2.l_orderkey`.
		// A residual that carries one stays on the join whole: the
		// executor keeps one rejection counter, so a split rendering
		// could not report both lines' counts honestly (ledgered).
		if isInnerOuterColumnEquality(c, outerW) {
			return nil
		}
		readsInner := false
		withinParams := true
		optimizer.WalkExprTree(c, func(x optimizer.Expr) {
			if cr, ok := x.(*optimizer.ColumnRef); ok {
				if cr.Index >= outerW {
					readsInner = true
				}
				if cr.SourceTableIdx == 0 || !probeRels[cr.SourceTableIdx] {
					withinParams = false
				}
			}
		})
		if !readsInner || !withinParams {
			return nil
		}
	}
	return pred
}

// probeRelations is the set of relations a parameterized probe reads: its
// own table and every outer relation its index keys are parameterized by
// (PG's required_outer). join_clause_is_movable_into admits a clause into
// the probe's ppi_clauses only when it references nothing else — TPC-DS
// Q19's `substr(ca_zip) <> substr(s_zip)` stays a Join Filter because the
// store probe is parameterized by store_sales alone.
//
// A key's outer reference indexes the OUTER row; when it carries no source
// id of its own, the outer row's column at that position names the relation.
func probeRelations(inner, outer optimizer.Node) map[int16]bool {
	rels := map[int16]bool{}
	optimizer.WalkPlanExprs(inner, func(e optimizer.Expr) {
		optimizer.WalkExprTree(e, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.ColumnRef:
				if r.SourceTableIdx != 0 {
					rels[r.SourceTableIdx] = true
				}
			case *optimizer.OuterColumnRef:
				if r.SourceTableIdx != 0 {
					rels[r.SourceTableIdx] = true
				}
			}
		})
	})
	for _, c := range inner.Output() {
		if c.SourceTableIdx != 0 {
			rels[c.SourceTableIdx] = true
		}
	}
	outerCols := outer.Output()
	addRef := func(src int16, idx int) {
		if src == 0 && idx >= 0 && idx < len(outerCols) {
			src = outerCols[idx].SourceTableIdx
		}
		if src != 0 {
			rels[src] = true
		}
	}
	for _, k := range probeKeyExprs(inner) {
		optimizer.WalkExprTree(k, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.ColumnRef:
				addRef(r.SourceTableIdx, r.Index)
			case *optimizer.OuterColumnRef:
				addRef(r.SourceTableIdx, r.Index)
			}
		})
	}
	return rels
}

// probeKeyExprs returns the index-key expressions a parameterized probe binds
// from the outer row.
func probeKeyExprs(inner optimizer.Node) []optimizer.Expr {
	var out []optimizer.Expr
	add := func(es ...optimizer.Expr) {
		for _, e := range es {
			if e != nil {
				out = append(out, e)
			}
		}
	}
	switch p := inner.(type) {
	case *optimizer.IndexScan:
		add(p.Key, p.LowKey, p.HighKey)
		add(p.Keys...)
		add(p.SAOPKeys...)
	case *optimizer.IndexOnlyScan:
		add(p.Key, p.LowKey, p.HighKey)
		add(p.Keys...)
	case *optimizer.BitmapHeapScan:
		if bis, ok := p.Outer.(*optimizer.BitmapIndexScan); ok {
			add(bis.Key)
			add(bis.Keys...)
		}
	}
	return out
}

// renderedParamQual is the residual as the inner scan's namespace sees it —
// outer-side columns become correlated references (always prefixed, PG's
// get_parameter rule), inner columns stay bare — or nil when the residual
// stays on the join.
func renderedParamQual(n optimizer.Node) optimizer.Expr {
	q := innerParamQual(n)
	if q == nil {
		return nil
	}
	_, outer, _, _, _ := paramJoinParts(n)
	outerW := len(outer.Output())
	d, ok := optimizer.CloneExprReplacingColumnRefs(q, func(cr *optimizer.ColumnRef) optimizer.Expr {
		if cr.Index >= outerW {
			return nil
		}
		return &optimizer.OuterColumnRef{Level: 1, Index: cr.Index, Name: cr.Name, Type: cr.Type, SourceTableIdx: cr.SourceTableIdx}
	})
	if !ok || d == nil {
		return nil
	}
	return d
}

// paramInnerChild reports whether c is n's parameterized inner child.
func paramInnerChild(n, c optimizer.Node) bool {
	_, _, inner, _, ok := paramJoinParts(n)
	return ok && inner != nil && c == inner
}

func splitAndExpr(e optimizer.Expr) []optimizer.Expr {
	if b, ok := e.(*optimizer.BinaryOp); ok && b.Op == parser.OpAnd {
		return append(splitAndExpr(b.Left), splitAndExpr(b.Right)...)
	}
	return []optimizer.Expr{e}
}

// isInnerOuterColumnEquality reports whether c is `col = col` with one column
// on each side of the parameterized join (a mergejoinable equivalence-class
// clause rather than a general join qual).
func isInnerOuterColumnEquality(c optimizer.Expr, outerW int) bool {
	b, ok := c.(*optimizer.BinaryOp)
	if !ok || b.Op != parser.OpEq {
		return false
	}
	l, lok := b.Left.(*optimizer.ColumnRef)
	r, rok := b.Right.(*optimizer.ColumnRef)
	if !lok || !rok {
		return false
	}
	return (l.Index >= outerW) != (r.Index >= outerW)
}
