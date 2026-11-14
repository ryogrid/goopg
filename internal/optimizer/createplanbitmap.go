package optimizer

// M0128-P2.4 — plan creation arms for bitmap scan paths. These translate
// the path kinds added in this slice back into the executor plan nodes
// P2.3 already supports. Design: docs/design/0128-0001-bitmap-heap-scan.md §3.5.

import (
	"fmt"

	"github.com/goopg/goopg/internal/parser"
)

// createBitmapHeapScanPlan translates a PathBitmapHeapScan into a
// *BitmapHeapScan executor plan node. It follows the same scanLeafFor
// pattern as createIndexScanPlan (createplanindex.go): peel the leaf's
// *Filter wrappers, build the replacement scan, and rewrap.
func createBitmapHeapScanPlan(p *Path) (Node, error) {
	if p.Rel == nil {
		panic("createPlan: PathBitmapHeapScan with no RelOptInfo")
	}
	id, rewrap, ok := scanLeafFor(p.Rel.baseLeaf)
	if !ok {
		panic(fmt.Sprintf("createPlan: PathBitmapHeapScan over relset %#08x whose leaf is not a rebuildable base scan", uint32(p.Rel.Relids)))
	}
	if len(p.Children) == 0 || p.Children[0] == nil {
		panic(fmt.Sprintf("createPlan: PathBitmapHeapScan over relset %#08x has no bitmap qual child", uint32(p.Rel.Relids)))
	}

	// Build the bitmap qual subtree (outer) from the child path.
	outer, _ := createPlanNode(p.Children[0])

	// M0148-0001c3a2: a BitmapOr qual's recheck is its ORed arm quals, and
	// the leaf conjuncts those imply leave the Filter (bitmapOrRecheck).
	// A BitmapAnd's recheck is its children's quals (create_bitmap_subplan's
	// AND arm) — whenever every index clause in the tree is exact; an
	// inexact one keeps the older single-index handling below.
	if k := p.Children[0].Kind; k == PathBitmapOr || (k == PathBitmapAnd && bitmapTreeExact(p.Children[0])) {
		bitmapQual, drop := bitmapOrRecheck(p.Children[0], p.Rel.baseLeaf)
		bhs := &BitmapHeapScan{
			pos: id.pos, Table: id.table, Alias: id.alias, RTID: id.rtid,
			BitmapQual: bitmapQual, Outer: outer, schema: id.schema,
		}
		return rewrapLeafDropping(p.Rel.baseLeaf, bhs, drop), nil
	}

	// Build the BitmapQual expression list from the index clauses.
	// Each index clause becomes part of the recheck condition.
	bitmapQual := bitmapQualExprs(p.Children[0])

	// Collect partial-index predicates from the bitmap tree leaves.
	// PG's create_bitmap_subplan appends indpred to bitmapqualorig for
	// every partial-index leaf, so the predicate is re-evaluated against
	// every heap tuple — the safety net for lossy bitmap pages, and the
	// correct filter when the predicate-implication prover is absent.
	partialPreds := collectBitmapPartialPredicates(p.Children[0])
	if len(partialPreds) > 0 {
		bitmapQual = append(bitmapQual, partialPreds...)
	}

	// A restriction probe's index quals came out of the leaf's own Filter:
	// they are the recheck list, and the reinstated Filter drops them (PG's
	// bitmapqualorig vs qpqual, createplan.c create_bitmap_scan_plan) — the
	// index-scan arm's rule (createIndexScanPlan). Their ColumnRefs are
	// leaf-local, the scan row the recheck evaluates against.
	var drop map[Expr]bool
	if child := p.Children[0]; child.Kind == PathBitmapIndexScan {
		for _, c := range child.IndexClauses {
			if c.ri == nil && c.local != nil {
				if drop == nil {
					drop = map[Expr]bool{}
				}
				drop[c.local] = true
				bitmapQual = append(bitmapQual, c.local)
				// M0148-0001c3a4 (i): the column quals merged into the
				// array are index quals too.
				for _, q := range c.saopColQuals {
					if q.local != nil {
						drop[q.local] = true
						bitmapQual = append(bitmapQual, q.local)
					}
				}
			}
		}
	}

	bhs := &BitmapHeapScan{
		pos:   id.pos,
		Table: id.table,
		Alias: id.alias,
		RTID:  id.rtid,
		// BitmapQual: the original index qual + partial-index predicates,
		// re-evaluated on lossy pages or when the index AM requires recheck.
		BitmapQual: bitmapQual,
		Outer:      outer,
		schema:     id.schema,
	}
	if drop != nil {
		return rewrapLeafDropping(p.Rel.baseLeaf, bhs, drop), nil
	}
	return rewrap(bhs), nil
}

// createBitmapIndexScanPlan translates a PathBitmapIndexScan into a
// *BitmapIndexScan executor plan node (leaf of a bitmap tree).
func createBitmapIndexScanPlan(p *Path) Node {
	if p.Rel == nil {
		panic("createPlan: PathBitmapIndexScan with no RelOptInfo")
	}
	id, _, ok := scanLeafFor(p.Rel.baseLeaf)
	if !ok {
		panic(fmt.Sprintf("createPlan: PathBitmapIndexScan over relset %#08x whose leaf is not a rebuildable base scan", uint32(p.Rel.Relids)))
	}
	if p.IndexInfo == nil {
		panic(fmt.Sprintf("createPlan: PathBitmapIndexScan over relset %#08x names no index", uint32(p.Rel.Relids)))
	}
	pred := bitmapQualExprs(p)
	bis := &BitmapIndexScan{
		pos: id.pos, Table: id.table, Alias: id.alias, Index: p.IndexInfo,
		Pred: pred, schema: id.schema,
	}

	// The clause list is an equality prefix on index columns 0..n-1, then
	// at most one of: a ScalarArrayOp on column n (M0148-0001c3a2), or
	// bounds and / or a NullTest on column n (M0146-0061 leading ranges,
	// M0148-0001c3a1 NullTests, M0148-0001c3a2 bounds behind the prefix).
	// Anything else is a producer bug.
	clauses := p.IndexClauses
	var prefix []Expr
	for _, c := range clauses {
		if c.op != parser.OpUnknown || c.nullTest != IndexNullTestNone || len(c.saop) > 0 {
			break
		}
		if c.indexCol != len(prefix) {
			panic(fmt.Sprintf("createPlan: bitmap index clause %d of %s claims index column %d; the index-column order was lost",
				len(prefix), p.IndexInfo.Name, c.indexCol))
		}
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: bitmap index clause %d of %s has no probe expression", len(prefix), p.IndexInfo.Name))
		}
		prefix = append(prefix, c.key)
	}
	rest := clauses[len(prefix):]
	np := len(prefix)
	if len(rest) == 0 {
		// Equality on a prefix (or none: a key-less full index scan).
		if np > 0 {
			bis.Key, bis.Keys = prefix[0], prefix
		}
		return bis
	}
	bis.Keys = prefix
	if len(rest[0].saop) > 0 {
		if len(rest) != 1 || rest[0].indexCol != np {
			panic(fmt.Sprintf("createPlan: bitmap ScalarArrayOp clause of %s does not follow its equality prefix", p.IndexInfo.Name))
		}
		bis.SAOPKeys = append([]Expr(nil), rest[0].saop...)
		bis.SAOPOp = rest[0].saopOp
		bis.SAOPColQuals = lowerSAOPColQuals(rest[0], map[Expr]bool{})
		return bis
	}
	sawBound := false
	var condOrder []IndexCondItem
	// M0148-0001c3a4 (j): a skip scan's bounds sit on a later column.
	rangeCol := np
	if sc := clausesSkipRange(clauses); sc > 0 && np == 0 {
		rangeCol, bis.SkipPrefix = sc, sc
	}
	for _, c := range rest {
		if c.indexCol != rangeCol {
			panic(fmt.Sprintf("createPlan: bitmap range clause of %s bounds index column %d, not %d", p.IndexInfo.Name, c.indexCol, rangeCol))
		}
		if c.nullTest != IndexNullTestNone {
			condOrder = append(condOrder, IndexCondNullTest)
			if bis.LeadingNullTest != IndexNullTestNone {
				// M0148-0001c3a2: a further NullTest on the column.
				bis.ExtraNullTests = append(bis.ExtraNullTests, ExtraIndexNullTest{Kind: c.nullTest, AfterBounds: sawBound})
				continue
			}
			bis.LeadingNullTest, bis.NullTestAfterBounds = c.nullTest, sawBound
			continue
		}
		if c.key == nil || len(c.saop) > 0 {
			panic(fmt.Sprintf("createPlan: bitmap range clause of %s has no bound", p.IndexInfo.Name))
		}
		switch c.op {
		case parser.OpGt, parser.OpGe:
			if bis.LowKey != nil {
				panic(fmt.Sprintf("createPlan: bitmap range probe of %s carries two lower bounds", p.IndexInfo.Name))
			}
			bis.LowKey, bis.LowOp = c.key, c.op
			condOrder = append(condOrder, IndexCondLow)
		case parser.OpLt, parser.OpLe:
			if bis.HighKey != nil {
				panic(fmt.Sprintf("createPlan: bitmap range probe of %s carries two upper bounds", p.IndexInfo.Name))
			}
			bis.HighKey, bis.HighOp = c.key, c.op
			condOrder = append(condOrder, IndexCondHigh)
		default:
			panic(fmt.Sprintf("createPlan: bitmap range probe of %s mixes an equality clause into its bounds", p.IndexInfo.Name))
		}
		sawBound = true
	}
	if len(condOrder) > 1 {
		bis.IndexCondOrder = condOrder
	}
	// Returned BARE: the leaf's `*Filter` wrappers must NOT be rebuilt here.
	//
	// `BitmapHeapScan.Outer` is required to be a bitmap PRODUCER — the executor
	// asserts `outerOp.(bitmapProducer)` — and a `*Filter` is not one. Rewrapping
	// therefore produced `Filter{BitmapIndexScan}` as the Outer and failed at
	// execution with "BitmapHeapScan outer is not a bitmap producer", for every
	// bitmap path over a FILTERED leaf. It is also what PG does: a `Bitmap
	// Index Scan` node carries `Index Cond:` and nothing else; the relation's
	// other quals appear as `Filter:` on the `Bitmap Heap Scan` above it.
	return bis
}

// bitmapQualExprs extracts the bitmap qual expression list from a bitmap
// index path's index clauses. These are the expressions that must be
// re-evaluated against heap tuples on lossy pages.
func bitmapQualExprs(p *Path) []Expr {
	if p == nil || len(p.IndexClauses) == 0 {
		return nil
	}
	exprs := make([]Expr, 0, len(p.IndexClauses))
	for _, c := range p.IndexClauses {
		if c.ri != nil && c.ri.clause != nil {
			exprs = append(exprs, c.ri.clause)
		}
	}
	return exprs
}

// collectBitmapPartialPredicates walks a bitmap path tree (IndexScan / And / Or)
// and collects all resolved partial-index predicates from the leaves. These are
// appended to the BitmapHeapScan's BitmapQual so the executor re-evaluates them
// against every heap tuple — the safety net for lossy pages (M0129-S5.4).
func collectBitmapPartialPredicates(p *Path) []Expr {
	if p == nil {
		return nil
	}
	switch p.Kind {
	case PathBitmapIndexScan:
		if p.PartialPredicate != nil {
			return []Expr{p.PartialPredicate}
		}
	case PathBitmapAnd, PathBitmapOr:
		var preds []Expr
		for _, child := range p.Children {
			preds = append(preds, collectBitmapPartialPredicates(child)...)
		}
		return preds
	}
	return nil
}

// buildBitmapAndOrPlan translates a PathBitmapAnd or PathBitmapOr into the
// corresponding BitmapAnd/BitmapOr executor plan node.
func buildBitmapAndOrPlan(p *Path, isAnd bool) Node {
	if len(p.Children) == 0 {
		var kind string
		if isAnd {
			kind = "AND"
		} else {
			kind = "OR"
		}
		panic(fmt.Sprintf("createPlan: PathBitmap%s with no inputs", kind))
	}
	inputs := make([]Node, 0, len(p.Children))
	var schema Schema
	for _, child := range p.Children {
		n, _ := createPlanNode(child)
		inputs = append(inputs, n)
		if schema == nil {
			schema = n.Output()
		}
	}
	if isAnd {
		return &BitmapAnd{
			Inputs: inputs,
			schema: schema,
		}
	}
	return &BitmapOr{
		Inputs: inputs,
		schema: schema,
	}
}
