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
//     columns is not movable into the inner rel and would stay a join qual)
//     and names nothing outside the probe's required_outer;
//   - a conjunct that restates an index key's equality (`inner.k = outer.x`
//     where the probe binds k to x) is enforced by the Index Cond and prints
//     nowhere — PG generates one clause per equivalence class for the
//     parameterized rel and removes the one the index consumes (TPC-DS
//     Q84's `c_current_cdemo_sk = cd_demo_sk`);
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

// paramQualPlacement decides where n's residual renders. moved is false
// when it stays on the join line (Join Filter / the NLI's Filter). When
// moved, probe is what renders under the inner scan — nil when every
// conjunct restates an index key equality and nothing is left to print.
func paramQualPlacement(n optimizer.Node) (probe optimizer.Expr, moved bool) {
	pred, outer, inner, memo, ok := paramJoinParts(n)
	if !ok || pred == nil || memo || outer == nil {
		return nil, false
	}
	switch inner.(type) {
	case *optimizer.IndexScan, *optimizer.BitmapHeapScan, *optimizer.IndexOnlyScan:
	default:
		return nil, false
	}
	outerW := len(outer.Output())
	probeRels := probeRelations(inner, outer)
	keyEqs := probeKeyEqualities(inner)
	var kept []optimizer.Expr
	for _, c := range splitAndExpr(pred) {
		if restatesProbeKey(c, keyEqs, outerW, outer) {
			continue
		}
		// An inner = outer equality is a join clause of its equivalence
		// class. PG's ppi_clauses carry the class's clause for the inner
		// rel against required_outer (TPC-DS Q50's `sr_customer_sk =
		// ss_customer_sk` prints as the store_sales probe's Filter); a
		// member pair naming a relation outside required_outer is checked
		// at the join (TPC-H Q9's `supplier.s_suppkey =
		// lineitem.l_suppkey` over a lineitem probe parameterized by
		// partsupp), which the required_outer test below keeps there.
		//
		// The host-scope walk reaches an `= ANY (list)` operand, which the
		// shallow WalkExprTree does not (TPC-DS Q48's `ca_state = ANY
		// (...)` arms read as outer-only and kept the clause on the join).
		readsInner := false
		withinParams := true
		enumerated := optimizer.WalkExprHostScope(c, func(x optimizer.Expr) {
			if cr, ok := x.(*optimizer.ColumnRef); ok {
				if cr.Index >= outerW {
					readsInner = true
				}
				if cr.SourceTableIdx == 0 || !probeRels[cr.SourceTableIdx] {
					withinParams = false
				}
			}
		})
		if !enumerated || !readsInner || !withinParams {
			return nil, false
		}
		kept = append(kept, c)
	}
	return joinAndExprs(kept), true
}

// probeKeyEquality is one `index column = outer key` binding of a
// parameterized probe.
type probeKeyEquality struct {
	column string
	key    optimizer.Expr
}

// probeKeyEqualities lists the equality bindings an IndexScan /
// IndexOnlyScan probe enforces, pairing each key with the index column it
// binds the way formatIndexCond does. Range bounds and array keys are not
// equalities and are left out.
func probeKeyEqualities(inner optimizer.Node) []probeKeyEquality {
	var (
		cols            []string
		key             optimizer.Expr
		keys, rangePref []optimizer.Expr
		skip            int
	)
	switch p := inner.(type) {
	case *optimizer.IndexScan:
		if p.Index == nil {
			return nil
		}
		cols, key, keys, rangePref, skip = p.Index.Columns, p.Key, p.Keys, p.RangePrefix, p.SkipPrefix
	case *optimizer.IndexOnlyScan:
		if p.Index == nil {
			return nil
		}
		cols, key, keys = p.Index.Columns, p.Key, p.Keys
	default:
		return nil
	}
	var out []probeKeyEquality
	bind := func(i int, k optimizer.Expr) {
		if k != nil && i >= 0 && i < len(cols) {
			out = append(out, probeKeyEquality{column: cols[i], key: k})
		}
	}
	switch {
	case len(keys) > 0:
		for i, k := range keys {
			bind(skip+i, k)
		}
	case key != nil:
		bind(0, key)
	}
	for i, k := range rangePref {
		bind(i, k)
	}
	return out
}

// restatesProbeKey reports whether c is `inner.col = outer.x` (either
// operand order) where the probe binds index column col to that same outer
// column x.
func restatesProbeKey(c optimizer.Expr, keyEqs []probeKeyEquality, outerW int, outer optimizer.Node) bool {
	b, ok := c.(*optimizer.BinaryOp)
	if !ok || b.Op != parser.OpEq || len(keyEqs) == 0 {
		return false
	}
	l, lok := b.Left.(*optimizer.ColumnRef)
	r, rok := b.Right.(*optimizer.ColumnRef)
	if !lok || !rok || (l.Index >= outerW) == (r.Index >= outerW) {
		return false
	}
	in, out := l, r
	if l.Index < outerW {
		in, out = r, l
	}
	outerCols := outer.Output()
	for _, ke := range keyEqs {
		if ke.column != in.Name {
			continue
		}
		idx, src, name := -1, int16(0), ""
		switch k := ke.key.(type) {
		case *optimizer.ColumnRef:
			idx, src, name = k.Index, k.SourceTableIdx, k.Name
		case *optimizer.OuterColumnRef:
			idx, src, name = k.Index, k.SourceTableIdx, k.Name
		default:
			continue
		}
		if src == 0 && idx >= 0 && idx < len(outerCols) {
			src, name = outerCols[idx].SourceTableIdx, outerCols[idx].Name
		}
		if idx == out.Index || (src != 0 && src == out.SourceTableIdx && name == out.Name) {
			return true
		}
	}
	return false
}

func joinAndExprs(es []optimizer.Expr) optimizer.Expr {
	var out optimizer.Expr
	for _, e := range es {
		if out == nil {
			out = e
			continue
		}
		out = &optimizer.BinaryOp{Op: parser.OpAnd, Left: out, Right: e}
	}
	return out
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

// renderedParamQual is the residual placement with the probe's part as the
// inner scan's namespace sees it — outer-side columns become correlated
// references (always prefixed, PG's get_parameter rule), inner columns stay
// bare. moved=false keeps the residual on the join; moved with a nil qual
// prints nothing under the probe.
func renderedParamQual(n optimizer.Node) (optimizer.Expr, bool) {
	q, moved := paramQualPlacement(n)
	if !moved || q == nil {
		return nil, moved
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
		return nil, false
	}
	return d, true
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
