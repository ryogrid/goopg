package optimizer

import (
	"math"

	"github.com/goopg/goopg/internal/parser"
)

// semiAntiJoinFactors is PG's SemiAntiJoinFactors (pathnodes.h): what a
// SEMI/ANTI nested loop needs to price its early exit. `apply` is false for
// every other join type, and the zero value selects the full-rescan model.
//
//   - outerMatchFrac: the fraction of outer rows that have at least one match
//     (the SEMI-arm selectivity of the join quals);
//   - matchCount: the average number of inner matches for an outer row that
//     has any, never below 1.
type semiAntiJoinFactors struct {
	apply          bool
	outerMatchFrac float64
	matchCount     float64
}

// semiAntiJoinFactorsFor is `compute_semi_anti_join_factors`
// (postgres/src/backend/optimizer/path/costsize.c), called once per join pair
// the way `add_paths_to_joinrel` calls it (joinpath.c), and handed to every
// nested-loop candidate for the pair.
//
//	jselec   = clauselist_selectivity(joinquals, SEMI/ANTI)
//	nselec   = clauselist_selectivity(joinquals, INNER)
//	avgmatch = max(1, nselec * innerrel->rows / jselec)   (1 when jselec == 0)
//
// ANTI is an outer join, so its pushed-down quals are not join quals
// (`RINFO_IS_PUSHED_DOWN`, the same test `pushedDownSelectivity` makes). PG's
// clauselist_selectivity is a product of per-clause selectivities here, and
// goopg's per-clause estimators are the sizer's own
// (`joinClauseSelectivityForJoin` for the SEMI arm, `joinClauseSelectivityExt`
// for INNER), so the two numbers agree with the joinrel size. PG applies no
// foreign-key selectivity in this function, and neither does this.
//
// Only SEMI and ANTI are handled. PG also takes this branch for an inner join
// whose inner rel is proven unique (`extra->inner_unique`); goopg's nested loop
// does not yet (ledgered with M0145-0008l).
func (s *searchCtx) semiAntiJoinFactorsFor(outer, inner *RelOptInfo, jt parser.JoinType, clauses []*restrictInfo) semiAntiJoinFactors {
	if s == nil || outer == nil || inner == nil || (jt != parser.JoinSemi && jt != parser.JoinAnti) {
		return semiAntiJoinFactors{}
	}
	joinrelids := outer.Relids | inner.Relids
	jselec, nselec := 1.0, 1.0
	for _, ri := range oneClausePerEquivClass(clauses) {
		if ri == nil {
			continue
		}
		if jt == parser.JoinAnti && !relsSubset(ri.relids, joinrelids) {
			continue
		}
		js, _ := s.joinClauseSelectivityForJoin(ri, jt, outer, inner)
		jselec *= js
		ns, _ := s.joinClauseSelectivityExt(ri)
		nselec *= ns
	}
	jselec, nselec = clampSelectivity(jselec), clampSelectivity(nselec)
	avgmatch := 1.0
	if jselec > 0 {
		avgmatch = math.Max(1.0, nselec*inner.Rows/jselec)
	}
	return semiAntiJoinFactors{apply: true, outerMatchFrac: jselec, matchCount: avgmatch}
}

// nestloopCostSemiAnti is `initial_cost_nestloop` + `final_cost_nestloop`
// (costsize.c) for a SEMI or ANTI join: the executor stops scanning the inner
// at the first match, so the inner is not charged in full for every outer row.
//
// Terms, in PG's order:
//
//	outer_matched   = rint(outer_rows * outer_match_frac)
//	outer_unmatched = outer_rows - outer_matched
//	inner_scan_frac = 2 / (match_count + 1)
//	ntuples         = outer_matched * inner_rows * inner_scan_frac
//
// When every join qual is an index qual of a parameterised inner
// (`has_indexed_join_quals`, `indexed`), a matched outer row scans
// inner_scan_frac of the inner, and an unmatched one costs about as much as
// returning the first tuple of a scan (inner_rescan_run / inner_rows). It
// evaluates no quals, so it adds nothing to ntuples. Otherwise an unmatched
// outer row scans the whole inner and every one of its pairs is processed.
// The first scan is charged in full once, attributed to the first unmatched
// row (or the first matched one if there are none).
//
// The per-tuple CPU (`cpu_tuple_cost` + the qual cost per tuple, which is
// goopg's `cpuOperatorCost * numQuals` surrogate, as `qualEvalCost`) rides
// `ntuples`, the pairs PROCESSED. The caller must not add `qualEvalCost` on
// top: this function charges it. The Material build (`matBuild`) stays the
// caller's, exactly as around `nestloopCost`.
func nestloopCostSemiAnti(cp costParams, outer, inner Cost, outerRows, innerRows, innerRescanStartup, innerRescanTotal float64, f semiAntiJoinFactors, indexed bool, numQuals int) Cost {
	startup := outer.Startup + inner.Startup
	run := outer.Total - outer.Startup
	// initial_cost_nestloop charges the rescan STARTUP for every rescan
	// before the join type is considered, on the unclamped outer row count.
	if outerRows > 1 {
		run += (outerRows - 1) * innerRescanStartup
	}
	outerPathRows := math.Max(outerRows, 1)
	innerPathRows := math.Max(innerRows, 1)
	innerRun := inner.Total - inner.Startup
	innerRescanRun := innerRescanTotal - innerRescanStartup

	matched := math.RoundToEven(outerPathRows * f.outerMatchFrac)
	unmatched := outerPathRows - matched
	scanFrac := 2.0 / (f.matchCount + 1.0)
	ntuples := matched * innerPathRows * scanFrac
	if indexed {
		run += innerRun * scanFrac
		if matched > 1 {
			run += (matched - 1) * innerRescanRun * scanFrac
		}
		run += unmatched * innerRescanRun / innerPathRows
	} else {
		ntuples += unmatched * innerPathRows
		run += innerRun
		if unmatched >= 1 {
			unmatched--
		} else {
			matched--
		}
		if matched > 0 {
			run += matched * innerRescanRun * scanFrac
		}
		if unmatched > 0 {
			run += unmatched * innerRescanRun
		}
	}
	if numQuals < 0 {
		numQuals = 0
	}
	run += (cp.cpuTupleCost + cp.cpuOperatorCost*float64(numQuals)) * ntuples
	return Cost{Startup: startup, Total: startup + run}
}

// hasIndexedJoinQuals is `has_indexed_join_quals` (costsize.c): the nested
// loop has no join quals left to evaluate itself (`joinrestrictinfo == NIL`,
// goopg's empty `residual`), and its inner is a parameterised plain index scan
// or a bitmap heap scan over a single bitmap index scan. Any other inner,
// including a Memoize wrapper, is "not a simple indexscan" and false.
//
// PG additionally checks each parameterised clause against the index clauses.
// goopg's empty residual already says the probe enforces every join clause.
func hasIndexedJoinQuals(inner *Path, residual []*restrictInfo) bool {
	if inner == nil || len(residual) > 0 || inner.RequiredOuter == 0 {
		return false
	}
	switch inner.Kind {
	case PathIndexScan:
		return true
	case PathBitmapHeapScan:
		return len(inner.Children) == 1 && inner.Children[0] != nil && inner.Children[0].Kind == PathBitmapIndexScan
	}
	return false
}
