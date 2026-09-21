package optimizer

import (
	"fmt"
	"os"
)

// M0141-S2b-1: the ORDERED upper rel adjudicates the DISTINCT upper rel's
// surviving PathDistinct candidates — the same mechanism
// `electOrderedGrouping` (upperorderedgrouping.go) already runs for
// GROUP_AGG, mirrored here. Before this, the caller (planner.go, the
// `s.Distinct` arm) picked createDistinctPaths' own ORDER-BY-blind cheapest
// winner and then asked only whether THAT ONE node's static shape
// (`distinctOutputSatisfiesOrder`, *Distinct only) happened to already
// deliver the requested order — a Unique-over-sorted winner that matched
// could never be recognized, and a query where Hashed wins the ORDER-BY-blind
// contest but Unique would avoid a whole extra top-level Sort could never be
// found. This file lets BOTH `addDistinctPaths` candidates (hashed, unique)
// compete on the ORDERED rel directly, exactly as grouping's Hashed/Sorted
// PathAgg candidates already do.
//
// distinctEmissionPathkeys is simpler than groupingEmissionPathkeys: DISTINCT
// never renames or reorders columns (`p.Distinct.schema` is always the
// pre-distinct child's own schema, createplansimple.go:259/261), so there is
// no input-to-output coordinate boundary to cross — only a translation from
// "which executor contract does this candidate's node run under" to pathkeys.

// traceDistinctDecline emits which specific distinctEmissionPathkeys/
// electOrderedDistinct check declined a candidate, reusing
// GOOPG_PGSHAPED_DP_TRACE (dpTrace) — same convention as traceGroupDecline.
// Diagnostic only: never changes which candidate is chosen.
func traceDistinctDecline(reason string, cand *Path) {
	if !dpTrace || cand == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "DPDISTINCT decline reason=%s unique=%v\n", reason, cand.Unique)
}

// distinctEmissionPathkeys is the emission order a PathDistinct candidate
// delivers its deduplicated rows in, expressed in the DISTINCT node's own
// output coordinates (identical to its input coordinates — see above). nil
// means "untranslatable": the caller falls through to the legacy
// `distinctOutputSatisfiesOrder` + unconditional-Sort behavior.
//
// Both shapes `addDistinctPaths` builds today are ALREADY fully ordered,
// by construction of their respective executors, never by a `Path.Pathkeys`
// stamp the hashed arm happens to carry:
//
//   - Unique (`cand.Unique`): `distinctOnOp` (operators_distinct.go) streams
//     its child's row order verbatim — the child is always the producer's
//     own full-column Sort (`addDistinctPaths`' `sortInput`,
//     `distinctAllColKeys`), so `cand.Pathkeys` (stamped from that Sort) is
//     trusted only after verifying it actually has the shape that Sort always
//     builds: one ascending, nulls-last key per output column, in position
//     order 0..n-1. A future `addDistinctPaths` change that reuses a
//     differently-ordered pre-sorted child would still be caught correctly
//     here (or safely declined if the shape check no longer holds).
//   - Hashed (`!cand.Unique`): `distinctOp` (operators_distinct.go) hash-dedups
//     then UNCONDITIONALLY re-sorts every row ascending, nulls-last, over
//     every output column in position order (`sort.Slice`, comparing column 0
//     upward) — a fixed executor contract this candidate carries with no
//     `Pathkeys` stamp at all (`addDistinctPaths` never sets one for this
//     arm), so the order is derived here directly rather than read off the
//     Path.
func distinctEmissionPathkeys(distinctNode *Distinct, cand *Path) []PathKey {
	if distinctNode == nil || cand == nil || cand.Kind != PathDistinct || cand.Distinct == nil {
		return nil
	}
	outCols := distinctNode.Output()
	if cand.Unique {
		if len(cand.Pathkeys) != len(outCols) {
			traceDistinctDecline("unique-pathkeys-len-mismatch", cand)
			return nil
		}
		for i, pk := range cand.Pathkeys {
			if !pk.SortAsc || pk.NullsFirst {
				traceDistinctDecline(fmt.Sprintf("unique-pathkey[%d]-not-asc-nullslast", i), cand)
				return nil
			}
			cr, ok := pk.Expr.(*ColumnRef)
			if !ok || cr.Index != i {
				traceDistinctDecline(fmt.Sprintf("unique-pathkey[%d]-not-positional-columnref", i), cand)
				return nil
			}
		}
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPDISTINCT translated ok keys=%d unique=true\n", len(cand.Pathkeys))
		}
		return cand.Pathkeys
	}
	emitted := make([]PathKey, len(outCols))
	for i, c := range outCols {
		emitted[i] = PathKey{
			Expr:       &ColumnRef{Index: i, Name: c.Name, Type: c.Type},
			SortAsc:    true,
			NullsFirst: false,
		}
	}
	if dpTrace {
		fmt.Fprintf(os.Stderr, "DPDISTINCT translated ok keys=%d unique=false\n", len(emitted))
	}
	return emitted
}

// electOrderedDistinct runs the same ordered-level loop electOrderedGrouping
// runs for GROUP_AGG: every surviving DISTINCT `PathDistinct` candidate is
// offered on the ORDERED upper rel — as-is when its translated emission
// order already delivers the ORDER BY, under a `create_sort_path` otherwise —
// through the same `addOrderedPaths` + `setCheapest`/
// `getCheapestFractionalPath` tournament grouping uses. It returns
// (winner, true), or (nil, false) to run the legacy
// `distinctOutputSatisfiesOrder` + unconditional-Sort fallback at the call
// site.
//
// Unlike grouping there is no aliasing to preserve on decline or on election:
// `distinctNode` (the caller's local `spec`) is never referenced again after
// this call either way (same "spec was just built at the wrapper site"
// property `createDistinctPaths`' own header states), so there is no
// `*node = *built` copy-back step. There is also no snapshot/restore of a
// shared rel to undo on decline — the ORDERED rel this function builds is a
// private, throwaway registry entry (see below), never `u`'s own, so a
// decline simply discards it.
func electOrderedDistinct(u *upperRels, distinctNode *Distinct, keys []SortKey, stmtPos int, cp costParams, tupleFraction, limitTuples float64) (Node, bool) {
	decline := func(reason string) (Node, bool) {
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPDISTINCT loop-decline reason=%s\n", reason)
		}
		return nil, false
	}
	if u == nil || len(keys) == 0 || distinctNode == nil {
		return decline("gate-precondition")
	}
	distinctRel := fetchUpperRel(u, UpperDistinct, 0, tupleFraction)
	var cands []*Path
	for _, p := range distinctRel.Pathlist {
		if p != nil && p.Kind == PathDistinct {
			cands = append(cands, p)
		}
	}
	// M0145-0006 slice 4: PG has no minimum — `create_ordered_paths`
	// iterates the whole input pathlist, `foreach(lc, input_rel->pathlist)`
	// (`postgres/src/backend/optimizer/plan/planner.c:5337`), so a LONE
	// `PathDistinct` is offered on the ORDERED rel exactly like one of many.
	// The `< 2` form was this function's remaining divergence from that
	// loop, and it is removed for the same reason and by the same argument
	// M0144-0011a-2 used on the grouping twin — the two are sibling paths
	// and a gate one of them dropped must not survive in the other
	// (pattern_sibling_paths_must_agree).
	//
	// A lone candidate is the COMMON case here, not a corner one: the
	// supply side is fine — `addDistinctPaths` always files both the hashed
	// and the unique-over-sorted candidate — but `add_path` dominance
	// usually prunes one of them before this function sees the pathlist.
	// The gate was therefore refusing the election on nearly every DISTINCT
	// statement, which is why the earlier reading of this gate as a
	// candidate-SUPPLY gap was wrong.
	if len(cands) < 1 {
		return decline(fmt.Sprintf("cands<1(%d)", len(cands)))
	}
	translated := make([][]PathKey, len(cands))
	anyTranslated := false
	for i, c := range cands {
		translated[i] = distinctEmissionPathkeys(distinctNode, c)
		if translated[i] != nil {
			anyTranslated = true
		}
	}
	if !anyTranslated {
		return decline("anyTranslated=false")
	}

	// A DEDICATED, throwaway ORDERED rel — never `fetchUpperRel(u, ...)`.
	// Unlike electOrderedGrouping (always the FIRST thing to touch the
	// statement's `UpperOrdered` rel), the DISTINCT wrapper runs AFTER the
	// generic ORDER BY block has already called `createOrderedPaths` once
	// for the SAME keys over the PRE-distinct child (planner.go's own
	// comment: "Applied after sorting so ORDER BY is respected") — sharing
	// `u`'s `(UpperOrdered, 0)` entry would let `setCheapest` pick that
	// stale, semantically unrelated candidate (built over a Node that is
	// not even `distinctNode`) right back out from under this election.
	// Caught live: M0141-S2b-1's own TPC-DS SF0.25 verification run hit
	// exactly this — `getCheapestFractionalPath` returned a `*Sort` whose
	// child was the pre-distinct scan, tripping the shape-gate below.
	ordered := fetchUpperRel(newUpperRels(), UpperOrdered, 0, tupleFraction)
	sizeUpperRelFromNode(ordered, distinctNode)

	sortKeys := pathkeysForSortKeys(keys)
	for i, c := range cands {
		offer := *c
		offer.Pathkeys = translated[i]
		addOrderedPaths(ordered, &offer, sortKeys, cp, limitTuples)
	}
	setCheapest(ordered)

	best := getCheapestFractionalPath(ordered, tupleFraction)
	if best == nil {
		return decline("best=nil")
	}
	built, _ := createPlanNode(best)
	switch b := built.(type) {
	case *Sort:
		switch b.Child.(type) {
		case *Distinct, *DistinctOn:
		default:
			return decline("sort-child-not-distinct")
		}
		b.pos = stmtPos
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPDISTINCT elected shape=Sort-over-%T\n", b.Child)
		}
	case *Distinct, *DistinctOn:
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPDISTINCT elected shape=bare-%T\n", built)
		}
	default:
		return decline("winner-shape-unexpected")
	}
	return built, true
}
