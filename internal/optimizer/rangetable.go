package optimizer

// C-20b (take3 08 §9.2) — the range table at the search boundary, and the
// column-IDENTITY check `boundaryMap`'s permutation check cannot make.
//
// PG oracle: `RangeTblEntry` and the `Query.rtable` list
// (postgres/src/include/nodes/parsenodes.h:1038), addressed by `Var.varno` /
// `Var.varattno` (postgres/src/include/nodes/primnodes.h:274) and turned into
// tuple-slot positions exactly once, by `set_plan_references` /
// `fix_upper_expr` (postgres/src/backend/optimizer/plan/setrefs.c).
//
// # What this file is, and what it is not
//
// It is NOT "the range table goopg was missing". goopg has had one since
// M0071-0009: `rangeBinding` (planner.go) is one entry per FROM item, in FROM
// order, carrying the relation identity (`table`/`alias`), the statement-wide
// RTE identity (`rtid`), and `sourceIdx` — a per-statement monotonic
// identifier from 1 that is stamped onto every column the binding produces as
// `SchemaColumn.SourceTableIdx` (plan.go:40). That field IS PG's `varno` in
// everything except how it is consumed.
//
// What goopg lacks is PG's `Var`. `ColumnRef.Index` (plan.go:429) is a
// POSITION in the pre-search binding concatenation, and `SourceTableIdx` rides
// along beside it as a disambiguation hint that a handful of by-name rebinders
// consult when `Name` alone collides. Nothing resolves a reference THROUGH the
// range table, and the executor matches: expression evaluation is a flat slice
// lookup into the child's materialised slot. That — not any missing range
// table — is why coordinates have to be translated at all, and it is why
// C-20b could not delete `boundaryMap`'s assertions: while the address is a
// position, those panics are the DETECTOR for the wrong-answer class, not a
// symptom of its cause. searchedtree.go says the same thing from the other
// side ("not replaceable by this and remain the primary guard").
//
// So this file does the inverse of the deletion the item asked for. It uses
// the range table to make a check the boundary could not previously make.
//
// # The gap it closes
//
// `boundaryMap` proves the root's layout is a PERMUTATION of the binding
// concatenation: no hole, nothing out of range, no duplicate. It says nothing
// about WHICH column sits at a position. A layout that is a valid permutation
// but assigns the wrong binding coordinate to a column passes every existing
// check and produces precisely the failure this item names — the right number
// of rows, carrying a neighbouring relation's values.
//
// `assertBoundaryColumnIdentity` closes that by asking the range table what
// SHOULD be at each binding coordinate and comparing it with what the built
// node actually emits there.
//
// # Why it is not tautological
//
// For an untouched base-rel leaf it very nearly is: `baseRelLayout`
// (createplanjoin.go) derives the layout by matching the emitted column's Name
// against the recorded leaf, so name agreement at that level is by
// construction. The check earns its place ABOVE the leaves, because the two
// sides stop sharing a derivation there:
//
//   - `lay` is composed structurally — the join arm concatenates its children's
//     layouts — while `n.Output()` is whatever schema the built node actually
//     carries, and a `*Join`'s schema is a CACHED field that a later pass can
//     leave stale (`reresolveJoinByName` refreshes it for exactly that reason,
//     and deliberately does not for SEMI/ANTI);
//   - the narrowing arms (`narrowBuildInput`, `narrowMergeInput`,
//     `IndexOnlyCovered`) rewrite node and layout in tandem, and "in tandem" is
//     an invariant, not a fact;
//   - `SourceTableIdx` distinguishes self-join instances that `Name` cannot, so
//     a swapped side in a `nation n1, nation n2` shape is visible here and
//     invisible to every name-based check in the planner.
//
// # Abstention discipline
//
// Transcribed from `assertSearchedTreeNeedsNoReconcile` (searchedtree.go),
// which is the model: a check that agrees where it can see and says nothing
// where it cannot is worth more than one that guesses. This one abstains on
//
//   - an empty `Name` on either side — plan.go:429 calls Name "for diagnostics"
//     and it IS empty on some construction paths, so an unnamed column is one
//     the check cannot see;
//   - a binding coordinate the range table has no entry for — a `fill`ed slot
//     (M0134-0187) is licensed by construction, and a real hole is already
//     `boundaryMap`'s panic;
//   - a coordinate two different base leaves claim. That is a producer bug in
//     its own right, but it is `boundaryMap`'s duplicate panic to report, and
//     an identity check with an ambiguous oracle has no opinion.
//
// `SourceTableIdx` is compared only when BOTH sides carry one: zero means
// "unknown / derived" (plan.go:40), not "relation zero".

import "fmt"

// boundaryRangeTable is the search's range table, keyed the way the boundary
// needs to read it: binding coordinate -> the column the range table says lives
// there.
//
// It is reconstructed from the chosen path's own `RelOptInfo`s rather than
// threaded in from `planSelect`. That is the correct source, not a workaround:
// the coordinate the check must validate is the one the SEARCH used, and
// `rel.baseLeaf` / `rel.baseOffset` (path.go) are that — the projection of
// `rangeBinding{table, alias, offset}` onto the search's relid space that
// `buildInitialRels` (joinsearch.go:423) copies out of the binding list. It
// also keeps `createPlanAtSearchRootRange`'s signature alone, whose only
// production caller (relfromjoinlist.go) belongs to another work item.
type boundaryRangeTable struct {
	// cols maps a binding coordinate to the leaf column at it. A coordinate
	// claimed by two different leaves is deleted from the map and recorded in
	// `ambiguous` rather than resolved — see the abstention list above.
	cols      map[int]SchemaColumn
	ambiguous map[int]bool
}

// at answers "what does the range table say lives at this binding coordinate",
// and false when it has no unambiguous answer.
func (rt boundaryRangeTable) at(coord int) (SchemaColumn, bool) {
	if rt.cols == nil || rt.ambiguous[coord] {
		return SchemaColumn{}, false
	}
	c, ok := rt.cols[coord]
	return c, ok
}

// rangeTableFromPath collects the range table reachable from a chosen path:
// every level-1 rel below it contributes `len(baseLeaf.Output())` coordinates
// starting at its `baseOffset`.
//
// A rel is reached once per path that references it and the same rel is shared
// by many paths, so a repeat with an IDENTICAL column is the normal case and is
// simply idempotent. Only a genuine disagreement — two leaves claiming one
// coordinate with different columns — marks the coordinate ambiguous.
func rangeTableFromPath(p *Path) boundaryRangeTable {
	rt := boundaryRangeTable{cols: make(map[int]SchemaColumn)}
	var walk func(*Path)
	seen := make(map[*RelOptInfo]bool)
	walk = func(p *Path) {
		if p == nil {
			return
		}
		if rel := p.Rel; rel != nil && rel.baseLeaf != nil && !seen[rel] {
			seen[rel] = true
			for i, col := range rel.baseLeaf.Output() {
				coord := rel.baseOffset + i
				if prev, dup := rt.cols[coord]; dup && !sameBoundaryColumn(prev, col) {
					if rt.ambiguous == nil {
						rt.ambiguous = make(map[int]bool)
					}
					rt.ambiguous[coord] = true
					continue
				}
				rt.cols[coord] = col
			}
		}
		for _, c := range p.Children {
			walk(c)
		}
	}
	walk(p)
	return rt
}

// sameBoundaryColumn is the identity test, in one place so the collector and
// the assertion cannot drift apart. Names must agree; source identities must
// agree only when both are known.
func sameBoundaryColumn(a, b SchemaColumn) bool {
	if a.Name != b.Name {
		return false
	}
	return a.SourceTableIdx == 0 || b.SourceTableIdx == 0 || a.SourceTableIdx == b.SourceTableIdx
}

// assertBoundaryColumnIdentity checks that every column the search root emits
// is the column the range table says belongs at the binding coordinate the
// layout assigns it.
//
// It runs on the join tree the arms built, BELOW the boundary `Project` — the
// same scoping as `assertSearchedTreeNeedsNoReconcile`, and for the same
// reason: the Project's target list is the coordinate map itself, not a
// reference into it, so an assertion about references should not be asked
// about it.
//
// The panic is deliberate and matches the milestone's convention
// (`markSearchedTree`, `boundaryMap`): a mismatch here means the plan about to
// run reads a different relation's column than every expression above the root
// believes it reads, which is a wrong answer with a correct row count.
func assertBoundaryColumnIdentity(n Node, lay outputLayout, rt boundaryRangeTable) {
	if n == nil || len(rt.cols) == 0 {
		return
	}
	out := n.Output()
	for pos, coord := range lay {
		if pos >= len(out) {
			// `createPlanAtSearchRootRange` already checks the lengths agree
			// and panics with a better message; do not race it to a worse one.
			return
		}
		want, ok := rt.at(coord)
		if !ok {
			continue
		}
		got := out[pos]
		if got.Name == "" || want.Name == "" {
			continue
		}
		if got.Name != want.Name {
			panic(fmt.Sprintf("createPlan: search root output column %d carries binding coordinate %d, where the range table has %q, but the built node emits %q there; the layout and the emitted schema disagree about which relation's column this is",
				pos, coord, want.Name, got.Name))
		}
		if got.SourceTableIdx != 0 && want.SourceTableIdx != 0 && got.SourceTableIdx != want.SourceTableIdx {
			panic(fmt.Sprintf("createPlan: search root output column %d carries binding coordinate %d, where the range table has %q from FROM item %d, but the built node emits %q from FROM item %d; two instances of one relation were swapped",
				pos, coord, want.Name, want.SourceTableIdx, got.Name, got.SourceTableIdx))
		}
	}
}
