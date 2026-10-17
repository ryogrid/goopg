package optimizer

import "github.com/goopg/goopg/internal/parser"

// M0146-0127 — a semijoin's equalities join the equivalence classes.
//
// PG's distribute_qual_to_rels (initsplan.c) treats a SEMI join's
// mergejoinable qual like a WHERE clause: a semijoin has no nullable side, so
// the qual is "pushed down", maybe_equivalence holds and process_equivalence
// merges it into an EquivalenceClass. With `ss_item_sk = i_item_sk` in WHERE
// and `ss_item_sk IN (SELECT ss_item_sk FROM cross_items)`, the class
// {store_sales.ss_item_sk, item.i_item_sk, cross_items.ss_item_sk} lets
// generate_join_implied_equalities derive `item.i_item_sk =
// cross_items.ss_item_sk`, and join_is_legal's unique-ification arm then
// joins the unique-ified RHS to `item` first — TPC-DS Q14's
// `HashAggregate(cross_items) -> Index Scan using item_pkey -> Index Scan
// using store_sales_pkey` spine. goopg's seam closed only the WHERE
// conjuncts, before the semijoin quals joined the list, so `{cross_items} |
// {item}` was declined with no join clause and PG's order was never built.
//
// goopg's semi and anti joins emit their preserved side only (Join.Output),
// where PG's emit any RHS column an equivalence class still needs above
// them. A derived clause that reads an RHS column is therefore applied only
// where the RHS's columns exist:
//
//   - as a join clause, only at a join one of whose inputs IS the RHS
//     (restrictInfo.onlyBesideRel, clausesFor) — the unique-ified inner
//     join PG builds, or the semijoin itself. Anywhere else the derived
//     equality is implied by the semijoin qual and the WHERE chain it was
//     derived through, both of which are still applied;
//   - as a parameterised probe's key, only under an outer path that keeps
//     the RHS's columns (pathKeepsRels) — not one that already semi-joined
//     the RHS away.

// semiRHSImpliedEqualities returns, in closure order, the equalities PG's
// equivalence classes would derive between a single-relation SEMI join RHS
// and the relations outside it, plus a map from each to its RHS-side operand
// (the operand whose relids the clause must find as a whole join input).
// inner holds the conjuncts the seam's closure already ran over (WHERE
// equalities, no outer ON quals); tail holds what joined the list after it —
// the semijoin quals among them. Equalities that would equate two members
// outside the RHS, or two members inside it, are not returned (deferral
// ledger M0146-0127).
func semiRHSImpliedEqualities(inner, tail []Expr, spans []leafSpan, joinInfo []*SpecialJoinInfo, nullable RelSet) ([]Expr, map[Expr]Expr) {
	var rhsSets []RelSet
	for _, sj := range joinInfo {
		if sj != nil && sj.Jointype == parser.JoinSemi && relLevel(sj.SynRighthand) == 1 {
			rhsSets = append(rhsSets, sj.SynRighthand)
		}
	}
	if len(rhsSets) == 0 {
		return nil, nil
	}
	// rhsOf reports the SEMI RHS a column operand belongs to, or 0.
	rhsOf := func(e Expr) RelSet {
		rs, ok := relidsOfExpr(e, spans)
		if !ok {
			return 0
		}
		for _, r := range rhsSets {
			if rs == r {
				return r
			}
		}
		return 0
	}
	// split classifies an equality between two single-relation column
	// operands into its RHS-side and outside operands.
	split := func(c Expr) (rhsSide, other Expr, r RelSet, ok bool) {
		bin, isBin := c.(*BinaryOp)
		if !isBin || bin.Op != parser.OpEq {
			return nil, nil, 0, false
		}
		if _, col := bin.Left.(*ColumnRef); !col {
			return nil, nil, 0, false
		}
		if _, col := bin.Right.(*ColumnRef); !col {
			return nil, nil, 0, false
		}
		lr, lok := relidsOfExpr(bin.Left, spans)
		rr, rok := relidsOfExpr(bin.Right, spans)
		if !lok || !rok || relLevel(lr) != 1 || relLevel(rr) != 1 || lr == rr {
			return nil, nil, 0, false
		}
		if r := rhsOf(bin.Left); r != 0 && !relsOverlap(rr, r) && !relsOverlap(rr, nullable) {
			return bin.Left, bin.Right, r, true
		}
		if r := rhsOf(bin.Right); r != 0 && !relsOverlap(lr, r) && !relsOverlap(lr, nullable) {
			return bin.Right, bin.Left, r, true
		}
		return nil, nil, 0, false
	}
	var semiEqs []Expr
	for _, c := range tail {
		if _, _, _, ok := split(c); ok {
			semiEqs = append(semiEqs, c)
		}
	}
	if len(semiEqs) == 0 {
		return nil, nil
	}
	closureIn := make([]Expr, 0, len(inner)+len(semiEqs))
	closureIn = append(closureIn, inner...)
	closureIn = append(closureIn, semiEqs...)
	var derived []Expr
	out := map[Expr]Expr{}
	for _, d := range inferTransitiveEqualities(closureIn) {
		if rhsSide, _, _, ok := split(d); ok {
			derived = append(derived, d)
			out[d] = rhsSide
		}
	}
	if len(derived) == 0 {
		return nil, nil
	}
	return derived, out
}

// pathKeepsRels reports whether p's output still carries the columns of rels:
// false when p contains a semi or anti join whose non-emitted side overlaps
// them (goopg's semi/anti joins emit the preserved side only, Join.Output).
// A join whose sides cannot be attributed answers false.
func pathKeepsRels(p *Path, rels RelSet) bool {
	if p == nil || rels == 0 {
		return true
	}
	switch p.Jointype {
	case parser.JoinSemi, parser.JoinAnti, parser.JoinRightSemi, parser.JoinRightAnti:
		if len(p.Children) == 2 {
			lost := 1
			if p.Jointype == parser.JoinRightSemi || p.Jointype == parser.JoinRightAnti {
				lost = 0
			}
			c := p.Children[lost]
			if c == nil || c.Rel == nil {
				return false
			}
			if relsOverlap(c.Rel.Relids, rels) {
				return false
			}
		}
	}
	for _, c := range p.Children {
		if !pathKeepsRels(c, rels) {
			return false
		}
	}
	return true
}

// paramOuterKeepsRHS is the outer-path half of the guard above: a
// parameterised inner whose required outer includes a SEMI RHS that a
// derived clause reads may sit only under an outer path that kept that RHS's
// columns.
func (s *searchCtx) paramOuterKeepsRHS(outer *Path, innerReq RelSet) bool {
	if s == nil || s.semiDerivedRHS == 0 {
		return true
	}
	need := innerReq & s.semiDerivedRHS
	return need == 0 || pathKeepsRels(outer, need)
}
