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
	"fmt"

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

// groupSimilarOrArgs is PG 18's group_similar_or_args (indxpath.c): arms of
// the form `indexkey op constant` on the same index key column with the same
// operator fold into one sub-OR, placed where the group's first arm stood;
// the others keep their order. The sub-OR is marked Nested — it stays one
// argument of the OR (EXPLAIN prints `((a = 1) OR ((b = 3) OR (b = 7)))`) and
// one item to the implication test. A column counts only when an index that
// takes ScalarArrayOp quals (amsearcharray: btree, not hash) has it as a key.
//
// ok is false when a group's operator is not equality: PG matches such a
// group as `col < ANY (…)`, which goopg's bitmap probe cannot run yet, and
// splitting it would build a BitmapOr PG does not (ledgered). The operand
// type part of PG's key (opno, inputcollid) is approximated by the key's
// literal kind (keyTypeTag).
func groupSimilarOrArgs(tbl *catalog.Table, indexes []*catalog.Index, arms []Expr) ([]Expr, bool) {
	type armKey struct {
		col, typ string
		op       parser.OpCode
	}
	keyCols := map[string]bool{}
	for _, idx := range indexes {
		if !idx.HasStorage() || idx.DeclaredHash {
			continue
		}
		for _, c := range idx.Columns {
			keyCols[c] = true
		}
	}
	keys := make([]*armKey, len(arms))
	for i, arm := range arms {
		bin, ok := arm.(*BinaryOp)
		if !ok {
			continue
		}
		cr, val, colOnRight, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
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
		keys[i] = &armKey{col: name, typ: keyTypeTag(val), op: op}
	}
	used := make([]bool, len(arms))
	out := make([]Expr, 0, len(arms))
	for i, arm := range arms {
		if used[i] {
			continue
		}
		used[i] = true
		if keys[i] == nil {
			out = append(out, arm)
			continue
		}
		group := []Expr{arm}
		for j := i + 1; j < len(arms); j++ {
			if !used[j] && keys[j] != nil && *keys[j] == *keys[i] {
				used[j] = true
				group = append(group, arms[j])
			}
		}
		if len(group) == 1 {
			out = append(out, arm)
			continue
		}
		if keys[i].op != parser.OpEq {
			return nil, false
		}
		sub := combineOr(group).(*BinaryOp)
		sub.Nested = true
		out = append(out, sub)
	}
	return out, true
}

// keyTypeTag is the operand-type part of groupSimilarOrArgs' key: an
// explicit cast's target type, else the literal's node kind. Two arms whose
// keys differ here (`tenthous = 1::int2` and `tenthous = 3::int8`) use
// different operators in PG and do not group.
func keyTypeTag(e Expr) string {
	if c, ok := e.(*CastExpr); ok {
		return "cast:" + c.TargetType
	}
	return fmt.Sprintf("%T", e)
}

// orEqualitySAOP is match_orclause_to_indexcol's conversion (indxpath.c): an
// OR whose every arm is `col = const` on the named column becomes the
// ScalarArrayOp `col = ANY ('{…}')`, returned as the IN it is priced as
// (scalararraysel, which btcostestimate applies to the converted qual). The
// constants must be keys the btree encoder takes exactly
// (restrictionKeyUsable), so the OR itself is the probe's exact recheck.
// M0148-0001c3a2.
func orEqualitySAOP(cat catalog.Catalog, tbl *catalog.Table, colName string, conj Expr) (*InExpr, bool) {
	bin, ok := conj.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil, false
	}
	var cr *ColumnRef
	var elems []Expr
	for _, arm := range flattenExprOr(conj) {
		ab, ok := arm.(*BinaryOp)
		if !ok || ab.Op != parser.OpEq {
			return nil, false
		}
		c, val, _, ok := normalizeColumnIndexKey(ab.Left, ab.Right)
		if !ok || c.Index < 0 || c.Index >= len(tbl.Columns) || tbl.Columns[c.Index].Name != colName ||
			!isConstExpr(val) || !restrictionKeyUsable(cat, tbl.Columns[c.Index], val) {
			return nil, false
		}
		cr = c
		elems = append(elems, val)
	}
	if len(elems) < 2 {
		return nil, false
	}
	return &InExpr{pos: conj.Pos(), Operand: cr, List: elems}, true
}

// saopPricedClause is the clause a ScalarArrayOp index clause is priced by:
// the IN conjunct itself, or for an OR of equalities the ScalarArrayOp it
// was converted to (orEqualitySAOP).
func saopPricedClause(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, c indexPathClause) Expr {
	if c.indexCol >= 0 && c.indexCol < len(idx.Columns) {
		if in, ok := orEqualitySAOP(cat, tbl, idx.Columns[c.indexCol], c.local); ok {
			return in
		}
	}
	return c.local
}

// bitmapSAOPOnColumn matches a ScalarArrayOp index qual on index column pos
// for a bitmap path: a `col IN (consts)` / `col = ANY (…)` conjunct
// (match_saopclause_to_indexcol), or an OR whose every arm is `col = const`
// on that column (match_orclause_to_indexcol, which turns it into
// `col = ANY ('{…}')`). The elements must be constants the key encoder takes
// exactly (restrictionKeyUsable), so the conjunct is the recheck (`local`).
// selExpr is the clause to price: the IN itself, or the ScalarArrayOp an OR
// becomes (scalararraysel, as btcostestimate prices the index qual).
// M0148-0001c3a2.
func bitmapSAOPOnColumn(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, pos int, conjuncts []Expr) (*indexPathClause, Expr) {
	if idx.DeclaredHash || pos < 0 || pos >= len(idx.Columns) {
		return nil, nil
	}
	colName := idx.Columns[pos]
	for _, conj := range conjuncts {
		if in, ok := conj.(*InExpr); ok {
			if in.Subquery != nil || in.Plan != nil || len(in.List) == 0 || in.Negated || in.NotEqualAny || in.AllOp ||
				(in.AnyOp != parser.OpUnknown && in.AnyOp != parser.OpEq) {
				continue
			}
			cr, ok := in.Operand.(*ColumnRef)
			if !ok || cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName {
				continue
			}
			usable := true
			for _, e := range in.List {
				if _, isNull := e.(*NullConst); isNull {
					continue
				}
				if !isConstExpr(e) || !restrictionKeyUsable(cat, tbl.Columns[cr.Index], e) {
					usable = false
					break
				}
			}
			if usable {
				return &indexPathClause{indexCol: pos, saop: in.List, local: conj}, conj
			}
			continue
		}
		if in, ok := orEqualitySAOP(cat, tbl, colName, conj); ok {
			return &indexPathClause{indexCol: pos, saop: in.List, local: conj}, in
		}
	}
	return nil, nil
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
		arms, ok := groupSimilarOrArgs(tbl, indexes, flattenExprOr(conj))
		if !ok {
			continue
		}
		// The other restriction clauses, without this OR: as an index
		// clause of an arm it would only repeat the arms (PG removes the
		// rinfo from inner_other_clauses once it is grouped; ungrouped, an
		// OR over several columns matches no index column anyway).
		others := make([]Expr, 0, len(conjuncts)-1)
		for _, c := range conjuncts {
			if c != conj {
				others = append(others, c)
			}
		}
		var bitmapquals []*Path
		for _, arm := range arms {
			if b, ok := arm.(*BinaryOp); ok && b.Nested {
				qs := s.bitmapPathsForOrGroup(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, b, others)
				if qs == nil {
					bitmapquals = nil
					break
				}
				bitmapquals = append(bitmapquals, qs...)
				continue
			}
			q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, arm, others)
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

// bitmapPathsForOrGroup is make_bitmap_paths_for_or_group (indxpath.c): the
// group matched as a whole (one ScalarArrayOp arm), or its arms one by one,
// whichever is cheaper. With no other restriction clause a whole-group match
// is taken outright, as PG takes it.
func (s *searchCtx) bitmapPathsForOrGroup(
	rel *RelOptInfo, tbl *catalog.Table, indexes []*catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node, group *BinaryOp, others []Expr,
) []*Path {
	var joint []*Path
	jointCost := 0.0
	if q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, group, others); q != nil {
		joint, jointCost = []*Path{q}, bitmapqualTotalCost(q)
	}
	if joint != nil && len(others) == 0 {
		return joint
	}
	var split []*Path
	splitCost := 0.0
	for _, arm := range flattenExprOr(group) {
		q := s.bitmapOrArmPath(rel, tbl, indexes, relPages, relTuples, T, totalPages, maxEntries, leaf, arm, others)
		if q == nil {
			split = nil
			break
		}
		split = append(split, q)
		splitCost += bitmapqualTotalCost(q)
	}
	switch {
	case split == nil:
		return joint
	case joint == nil:
		return split
	case jointCost < splitCost:
		return joint
	}
	return split
}

// bitmapqualTotalCost is `bitmapqual->total_cost` as
// make_bitmap_paths_for_or_group reads it: an index leaf's IndexPath
// total_cost (cost_index's whole-scan cost), a BitmapAnd's
// cost_bitmap_and_node total.
func bitmapqualTotalCost(p *Path) float64 {
	if p.Kind == PathBitmapIndexScan {
		return p.IndexPathTotalCost
	}
	return p.Cost.Total
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
		// A sub-OR inside an AND arm needs the recursive
		// generate_bitmap_or_paths call (deferred); a Nested group is one
		// ScalarArrayOp clause (bitmapSAOPOnColumn).
		if b, ok := c.(*BinaryOp); ok && b.Op == parser.OpOr && !b.Nested {
			return nil
		}
		inArm[c] = true
	}
	// all_clauses: the arm's clauses first, so an arm clause wins its index
	// column over a restriction clause on the same column. conjuncts holds
	// the relation's other restriction clauses (not the OR itself).
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
// one of its arms.
func bitmapOrRecheck(orPath *Path, leaf Node) ([]Expr, map[Expr]bool) {
	qual, ok := bitmapArmSubqual(orPath)
	if !ok || len(qual) != 1 {
		panic("createPlan: BitmapOr path with an inexact arm qual")
	}
	drop := map[Expr]bool{}
	for _, c := range extractFilterConjuncts(leaf) {
		if predicateImpliedBy(qual[0], c) {
			drop[c] = true
		}
	}
	return qual, drop
}

// predClass is predtest.c's PredClass.
type predClass int

const (
	predAtom predClass = iota
	predAnd
	predOr
)

// predicateClassify is predicate_classify (predtest.c) over goopg's binary
// AND / OR trees: a same-kind child is flattened into its parent's items,
// except a Nested clause, which is one item (a group_similar_or_args sub-OR
// is one argument of its OR in PG, and its ScalarArrayOp index qual an OR of
// its elements).
func predicateClassify(e Expr) (predClass, []Expr) {
	b, ok := e.(*BinaryOp)
	if !ok || (b.Op != parser.OpAnd && b.Op != parser.OpOr) {
		return predAtom, nil
	}
	var items []Expr
	var walk func(x Expr)
	walk = func(x Expr) {
		if xb, ok := x.(*BinaryOp); ok && xb.Op == b.Op && !xb.Nested {
			walk(xb.Left)
			walk(xb.Right)
			return
		}
		items = append(items, x)
	}
	walk(b.Left)
	walk(b.Right)
	if b.Op == parser.OpAnd {
		return predAnd, items
	}
	return predOr, items
}

// predicateImpliedBy is predicate_implied_by_recurse (predtest.c, strong
// form): does clause imply predicate? The AND / OR rules are PG's verbatim —
// notably OR => OR holds only when each clause arm implies ONE predicate
// arm, which is why PG keeps `(u1 = 1) OR (u2 = 3) OR (u2 = 7)` in the Filter
// under a recheck whose second arm is the group `(u2 = 3) OR (u2 = 7)`. The
// atom-vs-atom test is equal() plus goopg's narrow operator prover
// (provePartialIndexPredicate) for predicate_implied_by_simple_clause.
func predicateImpliedBy(clause, predicate Expr) bool {
	pclass, pitems := predicateClassify(predicate)
	cclass, citems := predicateClassify(clause)
	switch cclass {
	case predAnd:
		switch pclass {
		case predAnd:
			for _, p := range pitems {
				if !predicateImpliedBy(clause, p) {
					return false
				}
			}
			return true
		case predOr:
			for _, p := range pitems {
				if predicateImpliedBy(clause, p) {
					return true
				}
			}
			for _, c := range citems {
				if predicateImpliedBy(c, predicate) {
					return true
				}
			}
			return false
		default:
			for _, c := range citems {
				if predicateImpliedBy(c, predicate) {
					return true
				}
			}
			return false
		}
	case predOr:
		if pclass == predOr {
			for _, c := range citems {
				implied := false
				for _, p := range pitems {
					if predicateImpliedBy(c, p) {
						implied = true
						break
					}
				}
				if !implied {
					return false
				}
			}
			return true
		}
		for _, c := range citems {
			if !predicateImpliedBy(c, predicate) {
				return false
			}
		}
		return true
	default:
		switch pclass {
		case predAnd:
			for _, p := range pitems {
				if !predicateImpliedBy(clause, p) {
					return false
				}
			}
			return true
		case predOr:
			for _, p := range pitems {
				if predicateImpliedBy(clause, p) {
					return true
				}
			}
			return false
		default:
			return exprEqual(clause, predicate) || provePartialIndexPredicate(predicate, clause)
		}
	}
}
