package optimizer

// M0148-0001c3a2 — BitmapOr paths over OR restriction clauses: PG's
// generate_bitmap_or_paths / build_paths_for_OR (indxpath.c) and the
// BitmapOr arm of create_bitmap_subplan (createplan.c).
//
// For a restriction conjunct `A1 OR A2 OR …`, every arm must be matched by at
// least one bitmap index path built from the arm's own clauses (plus the
// relation's other restriction clauses, which may complete the index-column
// prefix); the arm's paths are combined by choose_bitmap_and and the arms are
// ORed into one BitmapOr. Design:
// docs/design/0100-0149/0148-0001c3a2-bitmap-or-paths.md.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// flattenExprOr flattens a planner Expr's top-level OR tree into its arms, as
// PG's canonicalize_qual leaves an OR clause flat. Non-OR expressions are
// returned in a singleton slice.
func flattenExprOr(expr Expr) []Expr {
	if expr == nil {
		return nil
	}
	bin, ok := expr.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []Expr{expr}
	}
	return append(flattenExprOr(bin.Left), flattenExprOr(bin.Right)...)
}

// combineOr builds a left-associated OR tree over the given arms (combineAnd's
// OR twin). nil/empty input returns nil.
func combineOr(arms []Expr) Expr {
	if len(arms) == 0 {
		return nil
	}
	out := arms[0]
	for _, a := range arms[1:] {
		out = &BinaryOp{pos: out.Pos(), Op: parser.OpOr, Left: out, Right: a}
	}
	return out
}

// bitmapOrArmsWouldGroup reports whether PG 18's group_similar_or_args
// (indxpath.c) would fold two or more of the arms into one sub-OR: arms of the
// form `indexkey op constant` on the same index key column with the same
// operator. PG then matches that sub-OR as one ScalarArrayOp index clause
// (`col = ANY ('{…}')`, make_bitmap_paths_for_or_group), a carrier goopg's
// bitmap probe does not have yet, so such an OR gets no BitmapOr here at all
// rather than a per-arm one PG would not build. The operand-type part of PG's
// key (opno, inputcollid) is not compared: treating two arms as similar when
// PG would not only declines more.
func bitmapOrArmsWouldGroup(tbl *catalog.Table, indexes []*catalog.Index, arms []Expr) bool {
	type armKey struct {
		col string
		op  parser.OpCode
	}
	keyCols := map[string]bool{}
	for _, idx := range indexes {
		if !idx.HasStorage() {
			continue
		}
		for _, c := range idx.Columns {
			keyCols[c] = true
		}
	}
	seen := map[armKey]bool{}
	for _, arm := range arms {
		bin, ok := arm.(*BinaryOp)
		if !ok {
			continue
		}
		cr, _, colOnRight, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
		if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) {
			continue
		}
		name := tbl.Columns[cr.Index].Name
		if !keyCols[name] {
			continue
		}
		op := bin.Op
		if colOnRight {
			op = swapInequalityOp(op)
		}
		k := armKey{col: name, op: op}
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

// bitmapOrHeapPaths is the restriction half of generate_bitmap_or_paths
// (create_index_paths passes rel->baserestrictinfo): one BitmapHeapScan over a
// BitmapOr per OR conjunct of the leaf whose every arm an index can serve.
func (s *searchCtx) bitmapOrHeapPaths(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node,
) []*Path {
	conjuncts := extractFilterConjuncts(leaf)
	var out []*Path
	for _, conj := range conjuncts {
		if bin, ok := conj.(*BinaryOp); !ok || bin.Op != parser.OpOr {
			continue
		}
		arms := flattenExprOr(conj)
		if bitmapOrArmsWouldGroup(tbl, indexes, arms) {
			continue
		}
		var bitmapquals []*Path
		for _, arm := range arms {
			q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, arm, conjuncts)
			if q == nil {
				// "If nothing matched this arm, we can't do anything with
				// this OR clause."
				bitmapquals = nil
				break
			}
			bitmapquals = append(bitmapquals, q)
		}
		if len(bitmapquals) < 2 {
			continue
		}
		orCost, orSel := costBitmapOrCost(s.cp, bitmapquals)
		orPath := &Path{
			Kind:              PathBitmapOr,
			Rel:               rel,
			Rows:              clampRowEst(orSel * relTuples),
			Cost:              orCost,
			BitmapSelectivity: orSel,
			Children:          bitmapquals,
			DisabledNodes:     disabledNodesFor(false, bitmapquals...),
			// create_bitmap_or_path (pathnode.c): `rel->consider_parallel`.
			ParallelSafe: rel.ParallelSafeForPath(),
		}
		out = append(out, s.bitmapHeapPathOver(rel, tbl, orPath, relTuples, T, totalPages, maxEntries, leaf))
	}
	return out
}

// bitmapOrArmPath is one arm of generate_bitmap_or_paths: build_paths_for_OR
// over the arm's clauses (its AND items, or the arm itself) with the
// relation's other restriction clauses as context, then choose_bitmap_and over
// the result. Returns nil when no index serves the arm.
//
// An index qualifies only when its path takes at least one index clause from
// the arm itself — build_paths_for_OR's `clauseset.nonempty` test; an index
// matched by the other clauses alone would scan the same rows for every arm.
// Every index clause must also be exact (recorded in `local`), since the
// arm's recheck qual is built from them (bitmapArmSubqual). PG's other
// build_paths_for_OR routes are deferred (design doc §Not covered): a sub-OR
// inside an AND arm (the recursive generate_bitmap_or_paths call) and a
// partial index whose predicate the arm proves with no index clause
// (useful_predicate).
func (s *searchCtx) bitmapOrArmPath(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node, arm Expr, conjuncts []Expr,
) *Path {
	armClauses := flattenExprAnd(arm)
	inArm := make(map[Expr]bool, len(armClauses))
	for _, c := range armClauses {
		if b, ok := c.(*BinaryOp); ok && b.Op == parser.OpOr {
			return nil
		}
		inArm[c] = true
	}
	// all_clauses: the arm's clauses first, so an arm clause wins its index
	// column over a restriction clause on the same column.
	ctx := make([]Expr, 0, len(armClauses)+len(conjuncts))
	ctx = append(ctx, armClauses...)
	ctx = append(ctx, conjuncts...)
	var indlist []*Path
	for _, idx := range indexes {
		if !idx.HasStorage() {
			continue
		}
		child := s.buildBitmapIndexChild(rel, tbl, idx, relPages, relTuples, totalPages, leaf, ctx)
		if child == nil {
			continue
		}
		usesArm, exact := false, true
		for _, c := range child.IndexClauses {
			if c.ri != nil || c.local == nil {
				exact = false
				break
			}
			if inArm[c.local] {
				usesArm = true
			}
		}
		if !usesArm || !exact {
			continue
		}
		_, child.Rows = computeBitmapPages(child.Rows, relTuples, T,
			indexPagesForPath(s.cat, child, tbl, relTuples), totalPages, s.cp.effectiveCacheSize, maxEntries)
		indlist = append(indlist, child)
	}
	if len(indlist) == 0 {
		return nil
	}
	// choose_bitmap_and over the arm's paths: an AND of several when that is
	// cheaper, else the single cheapest.
	heaps := make([]*Path, len(indlist))
	for i, c := range indlist {
		heaps[i] = &Path{Kind: PathBitmapHeapScan, Rel: rel, Children: []*Path{c}}
	}
	if and := s.chooseBitmapAnd(heaps, rel, tbl, relTuples, T, totalPages, maxEntries); and != nil {
		return and.Children[0]
	}
	var best *Path
	bestCost := 0.0
	for _, h := range heaps {
		c := bitmapScanCostEst(s.cp, h, rel.Rows, T, indexPagesForPath(s.cat, h, tbl, relTuples), totalPages, maxEntries, relQualOpCount(rel)).Total
		if best == nil || c < bestCost {
			best, bestCost = h.Children[0], c
		}
	}
	return best
}

// bitmapArmSubqual is create_bitmap_subplan's `*qual` for one bitmap tree
// node: a Bitmap Index Scan's index clauses plus its partial-index predicate
// when those clauses do not already imply it; a BitmapAnd's children's quals
// concatenated without duplicates; a BitmapOr's arms ORed into one clause.
// ok is false when a clause is not exact (no `local` conjunct), which the
// BitmapOr producer never admits.
func bitmapArmSubqual(p *Path) ([]Expr, bool) {
	switch p.Kind {
	case PathBitmapIndexScan:
		var qual []Expr
		for _, c := range p.IndexClauses {
			if c.ri != nil || c.local == nil {
				return nil, false
			}
			qual = append(qual, c.local)
		}
		if p.PartialPredicate != nil {
			implied := false
			for _, q := range qual {
				if provePartialIndexPredicate(p.PartialPredicate, q) {
					implied = true
					break
				}
			}
			if !implied {
				qual = append(qual, p.PartialPredicate)
			}
		}
		return qual, true
	case PathBitmapAnd:
		var qual []Expr
		for _, ch := range p.Children {
			sub, ok := bitmapArmSubqual(ch)
			if !ok {
				return nil, false
			}
			for _, e := range sub {
				dup := false
				for _, have := range qual {
					if exprEqual(have, e) {
						dup = true
						break
					}
				}
				if !dup {
					qual = append(qual, e)
				}
			}
		}
		return qual, true
	case PathBitmapOr:
		arms := make([]Expr, 0, len(p.Children))
		for _, ch := range p.Children {
			sub, ok := bitmapArmSubqual(ch)
			if !ok || len(sub) == 0 {
				// A qual-free arm makes the OR constant TRUE; the producer
				// never builds one.
				return nil, false
			}
			arms = append(arms, combineAnd(sub))
		}
		return []Expr{combineOr(arms)}, true
	}
	return nil, false
}

// bitmapOrRecheck is create_bitmap_scan_plan for a BitmapOr qual: the
// Recheck Cond (bitmapqualorig) is the ORed arm quals, and a leaf conjunct the
// recheck provably implies leaves the Filter (qpqual) — `thousand = 42`
// repeated in every arm, or the OR clause itself when each arm's qual covers
// its arm.
func bitmapOrRecheck(orPath *Path, leaf Node) ([]Expr, map[Expr]bool) {
	qual, ok := bitmapArmSubqual(orPath)
	if !ok {
		panic("createPlan: BitmapOr path with an inexact arm qual")
	}
	var armQuals [][]Expr
	for _, ch := range orPath.Children {
		sub, _ := bitmapArmSubqual(ch)
		armQuals = append(armQuals, sub)
	}
	drop := map[Expr]bool{}
	for _, c := range extractFilterConjuncts(leaf) {
		if orOfAndsImplies(armQuals, c) {
			drop[c] = true
		}
	}
	return qual, drop
}

// orOfAndsImplies is predicate_implied_by(clause, OR(AND(arm)…)) at goopg's
// prover fidelity (predtest.c): an OR implies a clause when every arm does.
func orOfAndsImplies(armQuals [][]Expr, clause Expr) bool {
	if len(armQuals) == 0 {
		return false
	}
	for _, items := range armQuals {
		if !andItemsImply(items, clause) {
			return false
		}
	}
	return true
}

// andItemsImply reports whether the AND of items implies clause: some item
// implies it outright, or — clause being an OR — the items imply one of its
// arms, or — clause being an AND — every one of its items
// (predicate_implied_by_recurse's AND/OR rules).
func andItemsImply(items []Expr, clause Expr) bool {
	for _, it := range items {
		if exprEqual(it, clause) || provePartialIndexPredicate(clause, it) {
			return true
		}
	}
	if bin, ok := clause.(*BinaryOp); ok {
		switch bin.Op {
		case parser.OpOr:
			for _, a := range flattenExprOr(clause) {
				if andItemsImply(items, a) {
					return true
				}
			}
		case parser.OpAnd:
			for _, a := range flattenExprAnd(clause) {
				if !andItemsImply(items, a) {
					return false
				}
			}
			return true
		}
	}
	return false
}
