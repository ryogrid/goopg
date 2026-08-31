package optimizer

// M0128-P2.4 — plan creation arms for bitmap scan paths. These translate
// the path kinds added in this slice back into the executor plan nodes
// P2.3 already supports. Design: docs/design/0128-0001-bitmap-heap-scan.md §3.5.

import "fmt"

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
		panic(fmt.Sprintf("createPlan: PathBitmapHeapScan over relset %#04x whose leaf is not a rebuildable base scan", uint16(p.Rel.Relids)))
	}
	if len(p.Children) == 0 || p.Children[0] == nil {
		panic(fmt.Sprintf("createPlan: PathBitmapHeapScan over relset %#04x has no bitmap qual child", uint16(p.Rel.Relids)))
	}

	// Build the bitmap qual subtree (outer) from the child path.
	outer, _ := createPlanNode(p.Children[0])

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

	bhs := &BitmapHeapScan{
		pos:   id.pos,
		Table: id.table,
		Alias: id.alias,
		// BitmapQual: the original index qual + partial-index predicates,
		// re-evaluated on lossy pages or when the index AM requires recheck.
		BitmapQual: bitmapQual,
		Outer:      outer,
		schema:     id.schema,
	}
	return rewrap(bhs), nil
}

// createBitmapIndexScanPlan translates a PathBitmapIndexScan into a
// *BitmapIndexScan executor plan node (leaf of a bitmap tree).
func createBitmapIndexScanPlan(p *Path) Node {
	if p.Rel == nil {
		panic("createPlan: PathBitmapIndexScan with no RelOptInfo")
	}
	id, rewrap, ok := scanLeafFor(p.Rel.baseLeaf)
	if !ok {
		panic(fmt.Sprintf("createPlan: PathBitmapIndexScan over relset %#04x whose leaf is not a rebuildable base scan", uint16(p.Rel.Relids)))
	}
	if p.IndexInfo == nil {
		panic(fmt.Sprintf("createPlan: PathBitmapIndexScan over relset %#04x names no index", uint16(p.Rel.Relids)))
	}

	// Build Key/Keys from the index clauses, matching createIndexScanPlan's
	// pattern for binding index columns positionally.
	var key Expr
	var keys []Expr
	pred := bitmapQualExprs(p)
	for i, c := range p.IndexClauses {
		if c.indexCol != i {
			panic(fmt.Sprintf("createPlan: bitmap index clause %d of %s claims index column %d; the index-column order was lost",
				i, p.IndexInfo.Name, c.indexCol))
		}
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: bitmap index clause %d of %s has no probe expression", i, p.IndexInfo.Name))
		}
		if i == 0 {
			key = c.key
		}
		keys = append(keys, c.key)
	}
	// When no index clauses are pushed, Key/Keys stay nil (full index scan).
	// Pred is the remaining index qual for recheck purpose (prefix of composite
	// key that didn't have corresponding clauses). For a full scan with no
	// clauses, Pred is nil.

	bis := &BitmapIndexScan{
		pos:    id.pos,
		Table:  id.table,
		Alias:  id.alias,
		Index:  p.IndexInfo,
		Key:    key,
		Keys:   keys,
		Pred:   pred,
		schema: id.schema,
	}
	// Returned BARE: the leaf's `*Filter` wrappers must NOT be rebuilt here.
	//
	// `BitmapHeapScan.Outer` is required to be a bitmap PRODUCER — the executor
	// asserts `outerOp.(bitmapProducer)` — and a `*Filter` is not one. Rewrapping
	// therefore produced `Filter{BitmapIndexScan}` as the Outer and failed at
	// execution with "BitmapHeapScan outer is not a bitmap producer", for every
	// bitmap path over a FILTERED leaf. It was latent only because bitmap paths
	// never won on cost; the moment they did, nine TPC-H queries failed.
	//
	// It is also what PG does. A `Bitmap Index Scan` node carries `Index Cond:`
	// and nothing else; the relation's other quals appear as `Filter:` on the
	// `Bitmap Heap Scan` above it. `createBitmapHeapScanPlan` rewraps there,
	// which is where the leaf's wrappers belong and where they already go.
	//
	// `rewrap` is deliberately still taken from `scanLeafFor` above rather than
	// dropped from the call: the identity it returns alongside is what supplies
	// pos/table/alias/schema, and a future edit that needs the wrappers should
	// find them here rather than re-derive them.
	_ = rewrap
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
