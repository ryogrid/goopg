package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R77 (plan-parity-fix-take2): price splice for legacy-built SEMI/ANTI
// NLI nodes.
//
// Lineage: R73 (display seam — no SEMI path filed, NLI carrier-UNSET),
// R74 (fork at unnestExistsExpr), R75 (admission needs no new
// machinery), R76 (rewrite-site probe prices 490456.34 via production
// helpers). The search never sees the unnested relset, so the node the
// rewrite builds carries no price and EXPLAIN prints the childless
// fallback. This pass stamps an arm-priced cost onto the FINAL tree's
// carrier-unset SEMI/ANTI NLI nodes — closing the seam by
// construction, and giving the upper elections (whose seeds read
// legacyDisplayCostOf) a number the arms actually compared.
//
// Placement: end of planSelectWithSettings (covers subplans through
// the shared funnel; idempotent via the carrier-set skip). The walk
// mirrors walkRewriteNLI's descent exactly, so it stamps precisely
// the rewrite-built population — nothing the rewrite declined.
//
// Fail-closed throughout: any unresolvable input (table, stats,
// index, keys), no filed path of the matching jointype, or any
// producer error leaves the node untouched (today's display).
// No join-order change (the node stays where unnest put it), no
// cost-term change (nestloopCost via addNLIPaths, as-is).
func stampSemiProbePrices(n Node, cat catalog.Catalog, cp costParams) {
	switch x := n.(type) {
	case *NestedLoopIndexJoin:
		if (x.Type == JoinTypeSemi || x.Type == JoinTypeAnti) && carrierUnset(x) {
			priceSemiProbe(x, cat, cp)
		}
		stampSemiProbePrices(x.Outer, cat, cp)
	case *Join:
		stampSemiProbePrices(x.Left, cat, cp)
		stampSemiProbePrices(x.Right, cat, cp)
	case *Filter:
		stampSemiProbePrices(x.Child, cat, cp)
	case *Project:
		stampSemiProbePrices(x.Child, cat, cp)
	case *Sort:
		stampSemiProbePrices(x.Child, cat, cp)
	case *Limit:
		stampSemiProbePrices(x.Child, cat, cp)
	case *Aggregate:
		stampSemiProbePrices(x.Child, cat, cp)
	case *WindowAgg:
		stampSemiProbePrices(x.Child, cat, cp)
	}
}

// carrierUnset reports whether the node carries no search price.
func carrierUnset(n Node) bool {
	c, ok := n.(PlanCostCarrier)
	if !ok {
		return true
	}
	_, set := c.PlanCostInfo()
	return !set
}

// priceSemiProbe prices one NLI node through the existing NLI arm and
// stamps the filed path's cost. All inputs re-derive from the final
// node + catalog; the fabricated relsets ({0} outer, {1} inner) are
// consistent by construction, exactly as the R75/R76 probes.
func priceSemiProbe(x *NestedLoopIndexJoin, cat catalog.Catalog, cp costParams) {
	outerNode := x.Outer
	idx, key, keys, ok := nliInnerProbe(x.Inner)
	if !ok || idx == nil || len(idx.Columns) == 0 {
		return
	}
	// Keys[i] binds Index.Columns[i] (IndexScan contract); single Key
	// binds Columns[0]. A key gadfly (past-the-prefix or unbound
	// leading column) declines — the probe the executor runs is not
	// the probe this would price.
	boundCols := idx.Columns
	if len(keys) > 0 {
		if len(keys) > len(idx.Columns) {
			return
		}
		boundCols = idx.Columns[:len(keys)]
	} else if key != nil {
		boundCols = idx.Columns[:1]
	} else {
		return
	}
	outerSet, innerSet := RelSet(1<<0), RelSet(1<<1)
	outerRows := float64(EstimateRows(outerNode))
	if outerRows < 1 {
		outerRows = 1
	}
	// The joinrel rows are the node's own legacy estimate (what EXPLAIN
	// prints today) — costs are the slice, rows do not move.
	semiRows := float64(EstimateRows(x))
	if semiRows < 1 {
		semiRows = 1
	}
	var tbl *catalog.Table
	switch in := x.Inner.(type) {
	case *IndexScan:
		tbl = in.Table
	case *IndexOnlyScan:
		tbl = in.Table
	}
	if tbl == nil {
		return
	}
	relTuples := outerRows
	if tbl != nil && tbl.Stats != nil && tbl.Stats.RowCount > 0 {
		relTuples = float64(tbl.Stats.RowCount)
	}
	var bound []paramIndexClause
	for i, col := range boundCols {
		var outerKey Expr
		if len(keys) > 0 {
			outerKey = keys[i]
		} else {
			outerKey = key
		}
		if outerKey == nil {
			return
		}
		bound = append(bound, paramIndexClause{
			innerCol: col, innerKey: &ColumnRef{Name: col},
			outerKey: outerKey, outerRels: outerSet,
		})
	}
	clauses := indexPathClauses(idx, bound)
	if len(clauses) == 0 {
		return
	}
	probed := boundPrefixClauses(bound, clauses)
	fullyBound := len(clauses) == len(idx.Columns)
	sel := parameterizedIndexSelectivity(tbl, idx, probed, relTuples, fullyBound)
	innerRel := newRelOptInfo(innerSet, relTuples, 32)
	probeRows := parameterizedBaserelRows(innerRel, idx,
		parameterizedIndexSelectivity(tbl, idx, bound, relTuples, fullyBound), fullyBound)
	relPages := baseRelPages(tbl, relTuples)
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	probeCost := costIndexScan(cp, indexScanInputs{
		relPages: relPages, relTuples: relTuples,
		indexPages: indexPages, indexTuples: indexTuples, treeHeight: treeHeight,
		selectivity: sel,
		uniqueEqualityOnAllKeys: idx != nil && idx.Unique && fullyBound,
		correlation:             indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages:         float64(relPages),
		loopCount:               outerRows,
		numQualOps:              float64(len(bound) - len(clauses)),
	})
	innerRel.Rows = probeRows
	addPath(innerRel, &Path{
		Kind: PathIndexScan, Rel: innerRel, Rows: probeRows, Cost: probeCost,
		IndexInfo: idx, IndexClauses: clauses, RequiredOuter: outerSet,
	}, "semi-splice")
	setCheapest(innerRel)
	outerRel := newRelOptInfo(outerSet, outerRows, 32)
	outerPC := legacyDisplayCostOf(outerNode)
	addPath(outerRel, &Path{
		Cost: Cost{Startup: outerPC.StartupCost, Total: outerPC.TotalCost},
		Rows: outerRows,
	}, "semi-splice")
	setCheapest(outerRel)
	joinrel := newRelOptInfo(outerSet|innerSet, semiRows, 32)
	sj := &SpecialJoinInfo{
		SynLefthand: outerSet, SynRighthand: innerSet,
		MinLefthand: outerSet, MinRighthand: innerSet,
		Jointype: parser.JoinSemi,
	}
	if x.Type == JoinTypeAnti {
		sj.Jointype = parser.JoinAnti
	}
	if err := addPathsToJoinrel(nil, joinrel, outerRel, innerRel, nil, cp, sj); err != nil {
		return
	}
	for _, p := range joinrel.Pathlist {
		if p.Kind != PathNestLoop {
			continue
		}
		if p.Jointype != sj.Jointype {
			continue
		}
		stampPlanCost(x, p)
		return
	}
}
