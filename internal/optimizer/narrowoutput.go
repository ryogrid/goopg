package optimizer

import "os"

// narrowBuild resolves GOOPG_NARROW_BUILD at process start. Default ON since
// P4-A §18 step 5: narrowing the build side changes plans (fewer batches at a
// fixed work_mem), and the step-4 gate — value-identical corpus at 64/4/512 MB,
// TPC-DS sweep neutral, zero row-shape panics — cleared it. `=0` opts back out
// to the un-narrowed arm. Read once, in the server: putting it on a client
// command line sets it where nothing reads it (handover §2).
var narrowBuild = narrowBuildFromEnv(os.Getenv("GOOPG_NARROW_BUILD"))

// narrowBuildFromEnv is the flag's polarity, factored out so tests and the
// flag-provenance table resolve the same default the process starts with
// (flaglabels.go's contract: no literal restating a default elsewhere).
// Opt-out polarity (`=0` disables), matching GOOPG_PGSHAPED_DP.
func narrowBuildFromEnv(v string) bool { return v != "0" }

// narrowBuildInput narrows a hash-join build side (node, layout) pair to the
// statement's needed columns. Take2 P4-01, rev 10 step 3: the call site is
// `joinInputsFor`, immediately after `createPlanNode(innerPath)` and before
// the layout/schema panic, so everything downstream sees a consistent pair
// and the pre-existing panic guards the helper on the first mistake.
//
// The `kind == "PathHashJoin"` guard lives HERE rather than at the call site
// so no future caller can narrow a merge join's inputs by forgetting it:
// only a hash join has a resident build side, and narrowing a streaming
// merge input would pay the projection for no memory saving (P4-A §18).
//
// Every refusal returns the pair untouched: flag explicitly off (`=0`),
// a non-hash join, no node, no path/rel, or an unknown needed set
// (NeededColsKnown false — the collector declined, which must not be read
// as "keep nothing"). The pre-flip behaviour is one export away, so any
// future gate measures the flag rather than the commit.
//
// Take2 P4-01 Slice 3 (planner-p4-01-target DESIGN, "Slice 3+"): the keep-set
// comes from the parent-aware joinrel derivation — the union needed above the
// scan/join tree plus the quals at and above the parent — via `joinKeepSet`
// below, instead of the statement-wide NeededCols re-derivation. The three
// Slice-2 guards carry over UNCHANGED: the hash-only kind refusal above, the
// NeededColsKnown-false decline above, and the coordinate-identity
// precondition inside `buildKeepSet` (the Target arm runs exactly where it is
// provably identical to the fallback). Where the derivation does not apply
// (unknown sets, uncollectable quals, exotic subtrees) it reports unknown and
// the caller falls back bit-identically, so the only observable change is the
// SOURCE of a narrower list. `narrowPlanOutput`'s ascending/unique/in-range
// guard is untouched: the derived positions come from `neededKeepSet` over
// the built node's OWN schema, ascending by construction.
func narrowBuildInput(kind string, innerNode Node, innerLay outputLayout, innerPath *Path) (Node, outputLayout) {
	if !narrowBuild || kind != "PathHashJoin" {
		return innerNode, innerLay
	}
	if innerNode == nil || innerPath == nil || innerPath.Rel == nil || !innerPath.Rel.NeededColsKnown {
		return innerNode, innerLay
	}
	if keep, ok := joinKeepSet(innerNode, innerPath); ok {
		return narrowPlanOutput(innerNode, innerLay, keep)
	}
	if keep, ok := buildKeepSet(innerNode, innerPath); ok {
		return narrowPlanOutput(innerNode, innerLay, keep)
	}
	return narrowPlanOutput(innerNode, innerLay, neededKeepSet(innerNode.Output(), innerPath.Rel.NeededCols))
}

// joinKeepSet derives the hash-build keep-set from the inner rel's Slice-3
// derived tlist (JoinKeep, stamped by deriveJoinKeeps), reporting false
// ("unknown", fall back) wherever no derivation applies. Positions come from
// `neededKeepSet` over the BUILT node's own schema — never leaf positions —
// so no coordinate-identity precondition is needed beyond what
// `narrowPlanOutput` already enforces: a name-matched subset of this schema
// is ascending, unique and in range by construction, and an index-ordered
// subset (the IndexOnlyScan hazard) resolves by name to the right columns.
//
// Name-keyed matching over-states only in the safe direction (F4): within
// one inner subtree two same-named columns of different sources are kept
// together rather than told apart, and a name read above the tree keeps
// every same-named copy — over-keep, never wrong-narrowing. The DROP side
// (a name kept by nothing above or at the parent) is exact only because the
// derivation's three inputs are each fail-closed: the above-tree set
// declines on any unenumerated upper construct, and the qual sets decline
// on any unenumerated expression (visitColumnRefsByName).
func joinKeepSet(innerNode Node, innerPath *Path) ([]int, bool) {
	if innerPath == nil || innerPath.Rel == nil {
		return nil, false
	}
	rel := innerPath.Rel
	if !rel.JoinKeepKnown || rel.JoinKeep == nil {
		return nil, false
	}
	if innerNode == nil {
		return nil, false
	}
	keep := neededKeepSet(innerNode.Output(), rel.JoinKeep)
	if len(keep) == 0 {
		// The derivation names nothing in this schema — either the inner
		// truly contributes nothing above (which a Project cannot express)
		// or the sets disagree at the edges. Decline to the Slice-2 arms,
		// which keep the statement-wide set: over-keep, never a wrong drop.
		return nil, false
	}
	return keep, true
}

// deriveJoinKeeps stamps per-joinrel keep-sets (JoinKeep) over the CHOSEN
// path tree rooted at p — the single-scanjoin-target rule (F1): one target
// from the union needed above the scan/join tree, joinrel tlists derived,
// NEVER parent-stamped onto shared paths. Called once per createPlan entry
// (createPlanAtSearchRootRange), before the recursion builds nodes.
//
// Inputs: the union needed above the tree (the root rel's OutputCols,
// stamped only on eligible problems) and the fail-closed qual walks. The
// walk carries each subtree's ancestor-qual names top-down; at every hash
// join it stamps the INNER rel with out ∪ ancestors ∪ at-parent. Levels
// whose quals cannot be enumerated, whose subtree is not scan/hash-only,
// or whose sets are unknown get NO stamp and fall back bit-identically.
//
// Idempotent and overwrite-only (never accumulates), so re-planning a tree
// recomputes the same lists. Rels are per-relset singletons with one
// position in the chosen tree; PATHS are never written (a parent-stamped
// set would serve the second parent the first parent's projection, since
// addPathsToJoinrel reads shared CheapestTotal paths).
func deriveJoinKeeps(p *Path) {
	if p == nil || p.Rel == nil {
		return
	}
	out, outKnown := p.Rel.OutputCols, p.Rel.OutputColsKnown
	needed, neededKnown := p.Rel.NeededCols, p.Rel.NeededColsKnown
	if !outKnown || out == nil || !neededKnown || needed == nil {
		return
	}
	deriveJoinKeepsAt(p, out, nil, false)
}

// deriveJoinKeepsAt is the top-down walk: anc holds the union of qual
// column names at every strict ancestor join of p, and poisoned means an
// ancestor's quals (or kind) could not be inventoried — no stamps below.
func deriveJoinKeepsAt(p *Path, out map[string]bool, anc map[string]bool, poisoned bool) {
	if p == nil {
		return
	}
	switch p.Kind {
	case PathHashJoin:
		if len(p.Children) != 2 || p.Children[0] == nil || p.Children[1] == nil {
			return
		}
		at, ok := collectJoinQualNames(p)
		outer, inner := p.Children[0], p.Children[1]
		if !poisoned && ok && joinSubtreeNarrowable(inner) {
			keep := make(map[string]bool, len(out)+len(anc)+len(at))
			for name := range out {
				keep[name] = true
			}
			for name := range anc {
				keep[name] = true
			}
			for name := range at {
				keep[name] = true
			}
			if inner.Rel != nil {
				inner.Rel.JoinKeep, inner.Rel.JoinKeepKnown = keep, true
			}
		}
		childAnc, childPoison := anc, poisoned || !ok
		if ok {
			childAnc = unionNameSets(anc, at)
		}
		deriveJoinKeepsAt(outer, out, childAnc, childPoison)
		deriveJoinKeepsAt(inner, out, childAnc, childPoison)
	case PathMergeJoin:
		// HASH-BUILD-ONLY carries forward (F2): merge inputs are OUT of
		// scope for the first cut (goopg merge join order lives in the
		// HashKeys list order; projecting a merge input can drop/shift a
		// sort-key column sortInnerAndOuter/absorbMergeSort assume present),
		// so a merge join poisons its subtree — no stamps at or below it,
		// and its quals are not collected as ancestors either. Merge-input
		// narrowing needs its own slice with a sort-key-preservation proof.
		return
	case PathNestLoop, PathMemoize, PathPrebuilt,
		PathBitmapHeapScan, PathBitmapIndexScan, PathBitmapAnd, PathBitmapOr,
		PathAgg, PathGather, PathGatherMerge:
		// F3-conservative: a nested-loop (parameterised probe internals no
		// qual walk can inventory), a memoize/bitmap/agg/gather node, or
		// anything unrecognised poisons its subtree — no stamps at or below
		// it. Levels above it are unaffected (it sits below them). A
		// prebuilt path reaches this arm only as the walk's recursion
		// terminator: it has no path children to descend into, and
		// joinSubtreeNarrowable already treats it as a narrowable boundary
		// leaf, so nothing is lost by not descending.
		return
	case PathSort:
		// Transparent: an absorbed merge sort is never emitted as a node
		// (the merge executor re-sorts), so its keys need not survive.
		for _, c := range p.Children {
			deriveJoinKeepsAt(c, out, anc, poisoned)
		}
	case PathSeqScan, PathIndexScan:
		// Leaves: nothing to stamp, nothing to descend into.
	default:
		return
	}
}

// collectJoinQualNames returns every column name the join path p reads from
// its inputs — the HashKeys operands and Residual clauses — and whether the
// answer is COMPLETE. Incomplete (false) means "decline this level": the
// walk is fail-closed via visitColumnRefsByName (unenumerated expression,
// inner-scope plan, OuterColumnRef, CTIDExpr or MergeWholeRowRef each veto),
// mirroring the coverage translateToLayout needs — anything uninventoried
// keeps its column (the Slice-2 precedent, F3).
func collectJoinQualNames(p *Path) (map[string]bool, bool) {
	dst := make(map[string]bool, 8)
	complete := true
	add := func(e Expr) {
		if e == nil || !complete {
			return
		}
		if !visitColumnRefsByName(e, func(name string) { dst[name] = true }) {
			complete = false
		}
	}
	for _, ri := range p.HashKeys {
		if ri == nil {
			continue
		}
		add(ri.leftKey)
		add(ri.rightKey)
		add(ri.clause)
	}
	for _, ri := range p.Residual {
		if ri == nil {
			continue
		}
		add(ri.clause)
	}
	if !complete {
		return nil, false
	}
	return dst, true
}

// joinSubtreeNarrowable reports whether the build-side subtree rooted at p
// may carry a derived keep: every node in it must be a shape whose internal
// column needs are either below the narrow point (scan filters, absorbed
// sorts, prebuilt interiors) or inventoried above it (hash join quals).
// Anything else — merge/NL/bitmap/memoize/agg/gather/sort — declines THIS
// level (levels inside still derive via the walk). Name-keyed, so self-joins
// over-keep rather than misattribute (F4).
//
// A PathPrebuilt is a narrowable BOUNDARY leaf, not a poison node. It wraps
// an already-built executor Node — a base-table leaf (bare or Filter-wrapped
// with its leaf-local restriction), a self-contained derived-table / VALUES /
// function / CTE leaf, or a searched sub-joinlist subtree — and the walk never
// descends into it (a prebuilt path has no path children, and a derived
// table's interior was planned by its own planSelect call with its own
// above-tree set, so outer names are never pushed inward). Stamping the build
// that CONTAINS a prebuilt is still safe: the keep is applied ABOVE the built
// subtree by column NAME over its own output schema (joinKeepSet), so the
// prebuilt's interior — leaf-local filters, derived-table bodies, subproblem
// interiors — runs below the narrowing Project exactly as it does under the
// Slice-2 NeededCols arm, which already narrows over prebuilt subtrees. Every
// reader above the narrow point is inventoried or guarded exactly as for scan
// leaves: ancestor/at join quals (fail-closed collectJoinQualNames), the
// above-root residual (the seam's residual-hits-pad fallback), upper consumers
// (the out seed, which over-states on any doubt), late pushdown duplicates
// (positional name validation, declining on a dropped column), and the
// translateToLayout plan-time panic as the final tripwire. LATERAL leaves
// never reach this walk: the seam declines lateral chains before any search
// runs, and a lateral rangevar declines the needed set the narrowing requires.
// Correlated (outer-ref-reading) statements never reach it either: the seam
// marks them corrAbove, declining parent-aware narrowing there, because the
// unnest machinery reads body-local columns above the body tree (group keys,
// probe keys) that no statement-level collector can see.
func joinSubtreeNarrowable(p *Path) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case PathSeqScan, PathIndexScan, PathPrebuilt:
		return true
	case PathHashJoin:
		if len(p.Children) != 2 {
			return false
		}
		return joinSubtreeNarrowable(p.Children[0]) && joinSubtreeNarrowable(p.Children[1])
	default:
		return false
	}
}

// exprHasOuterRef reports whether e reads an outer query level at the
// CURRENT statement scope — a resolved *OuterColumnRef outside any inner
// subplan. Inner subplans are stepped over silently (scopeIgnore): an outer
// ref inside a subquery body belongs to that body's own scope, and flagging
// the enclosing statement for it would decline narrowing the body never
// affects. An unwalkable expression declines (true): an uninventoried reader
// must keep its columns, the Slice-2 precedent (F3).
//
// Take2 P4-01 Slice 3: the correlated-statement decline. A WHERE/ON reference
// to an outer level means post-hoc machinery above this problem's tree reads
// body-local columns no statement-level collector can see — the unnest
// rewrite's decorrelated group keys and probe keys (TPC-H Q2's ps_partkey).
// Such a problem narrows by the Slice-2 arms only.
func exprHasOuterRef(e Expr) bool {
	if e == nil {
		return false
	}
	found := false
	ok := walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			if _, isOuter := n.(*OuterColumnRef); isOuter {
				found = true
				return false
			}
			return true
		},
	})
	return found || !ok
}

// exprHasOuterRefList reports whether any expression in the list reads an
// outer query level at the current statement scope (see exprHasOuterRef).
func exprHasOuterRefList(es []Expr) bool {
	for _, e := range es {
		if exprHasOuterRef(e) {
			return true
		}
	}
	return false
}

// unionNameSets returns the union of two name sets; either may be nil.
func unionNameSets(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for name := range a {
		out[name] = true
	}
	for name := range b {
		out[name] = true
	}
	return out
}

// searchedResidualHitsPad reports whether the above-root residual references
// a boundary-padded (dropped) column of the searched tree. The seam falls
// the search back when it does (03 §4.2): the residual is evaluated above
// the subtree in binding coordinates, so a padded reference would read a
// NULL. Name-keyed, erring toward fallback; Slice-2 pads (columns outside
// the needed set) can never match a residual reference, so this only fires
// on the narrower Slice-3 keeps. `needed` guards the unwalkable-residual
// case: when the residual cannot be enumerated, fall back only if a
// statement-needed column was padded (a Slice-3-shaped hole), never on
// Slice-2 holes alone.
func searchedResidualHitsPad(residual Expr, searched Node, needed map[string]bool) bool {
	if residual == nil || searched == nil {
		return false
	}
	padded := boundaryPaddedNames(searched)
	if len(padded) == 0 {
		return false
	}
	refs := make(map[string]bool, 8)
	if !visitColumnRefsByName(residual, func(name string) { refs[name] = true }) {
		for name := range padded {
			if needed[name] {
				return true
			}
		}
		return false
	}
	for name := range refs {
		if padded[name] {
			return true
		}
	}
	return false
}

// boundaryPaddedNames returns the names of every NULL-padded boundary slot
// in the searched tree — the columns narrowing dropped below the search
// root, republished as typed NULLs to keep positions aligned. Walks every
// tagged boundary projection in the tree (nested sub-joinlists publish
// their own), reading the pad positions' schema names.
func boundaryPaddedNames(n Node) map[string]bool {
	var out map[string]bool
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if p, isProj := n.(*Project); isProj && isSearchedTree(n) {
			outSchema := p.Output()
			for i, tg := range p.Targets {
				if _, isPad := tg.(*NullConst); !isPad {
					continue
				}
				if i < 0 || i >= len(outSchema) {
					continue
				}
				if name := outSchema[i].Name; name != "" {
					if out == nil {
						out = make(map[string]bool, 4)
					}
					out[name] = true
				}
			}
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return out
}

// buildKeepSet derives the hash-build keep-set from the inner path's Slice-1
// Target, reporting false ("unknown", fall back) wherever the Target does not
// apply. The fallback is today's NeededCols derivation, unchanged.
//
// The second arm below — the coordinate-identity precondition — is
// load-bearing, and it is a tightening, not a loosening, of the decline
// contract. `scanPathTarget` records positions into the REL's leaf output, but
// not every scan path builds a node with that schema: `IndexOnlyScan` emits
// its covered columns in INDEX order (createplanindex.go:351-365), so leaf
// positions applied to it would keep an in-range-but-shifted column — exactly
// the permutation wrong-answer class the P4-01b lesson forbids, and one the
// ascending/unique/in-range guard in `narrowPlanOutput` cannot catch (a
// shifted position is still ascending, unique and in range, while the
// name-based derivation gets it right). A narrowed scan therefore falls back
// rather than misselect.
//
// Conversely the Target arm runs only where it is provably identical to the
// fallback: `scanPathTarget` IS `neededKeepSet` over this same leaf schema and
// set, so on a coordinate-identical node the two derivations are the same
// function of the same inputs, and the keep list is the same one.
func buildKeepSet(innerNode Node, innerPath *Path) ([]int, bool) {
	if innerPath == nil || !innerPath.TargetKnown || innerPath.Target == nil {
		return nil, false
	}
	if innerPath.Rel == nil || innerPath.Rel.baseLeaf == nil {
		// No leaf schema to check coordinates against (a unit-built path, or
		// any non-scan shape carrying a target it should not have): decline
		// rather than index a schema that is not there.
		return nil, false
	}
	if innerNode == nil || !sameOutputColumns(innerNode.Output(), innerPath.Rel.baseLeaf.Output()) {
		return nil, false
	}
	return innerPath.Target, true
}

// sameOutputColumns reports whether two schemas carry the same columns in the
// same order — the coordinate-identity precondition `buildKeepSet` needs
// before a leaf-position list may index a built node. Names IN ORDER (not a
// set) is the comparison: a full-coverage index emitting every column in index
// order is the same set as the leaf but a different coordinate space, and a
// set comparison would admit it.
func sameOutputColumns(a, b Schema) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// Build-side output narrowing — take2 P4-01, rev 10 step 2.
//
// A join's build side carries every column of every relation beneath it, and a
// goopg hash entry costs `48 × columns + 24` bytes (hashsize.EntryBytes),
// whatever those columns hold. Dropping the ones no part of the statement
// references shrinks the hash table proportionally.
//
// This file provides the transformation and its flag. The call site is
// `joinInputsFor`, behind GOOPG_NARROW_BUILD (P4-A §18 step 3; default ON
// since step 5 — the step-4 value gate was clean at all three work_mem
// budgets and the TPC-DS sweep neutral, so the flag now selects the OLD
// behaviour, not the new one).
//
// WHY A `Project` AND NOT A NARROWED SCAN (rev 7). `projectOp` sizes its output
// row from the SAME list its schema comes from (`o.out = acquireRow(len(o.targets))`,
// `schema: plan.Output()`), so it narrows row and schema together, by
// construction. `newSeqScanOp` instead holds the width in two places —
// `schema: p.Output()` against `cols: p.Table.Columns` — and P4-01b moved one of
// them, which is how TPC-H Q2 and Q5 came to return 0 rows and Q18 the right
// count with the wrong tuples.
//
// WHY THE LAYOUT MOVES WITH IT (rev 8). `joinInputsFor` panics when a child's
// layout and schema disagree (createplanjoin.go:289). The layout is
// `output column -> binding coordinate`, so narrowing is the same subset applied
// to both — which is why this returns the pair rather than just the node.

// narrowPlanOutput returns n projected down to the `keep` output columns,
// together with the correspondingly narrowed layout.
//
// `keep` holds indices into n.Output(), and must be ASCENDING and unique — the
// caller derives it by scanning the schema in order, and both the schema and the
// layout are positional, so an out-of-order keep set would silently permute the
// child's columns rather than narrow them.
//
// Returns (n, lay) unchanged when nothing can be dropped, so the caller does not
// have to special-case the common path and no Project is emitted for a no-op.
func narrowPlanOutput(n Node, lay outputLayout, keep []int) (Node, outputLayout) {
	if n == nil || len(keep) == 0 || len(keep) >= len(lay) {
		return n, lay
	}
	out := n.Output()
	if len(lay) != len(out) {
		// The caller's precondition, and the same disagreement
		// createplanjoin.go:289 panics on. Decline rather than produce a pair
		// that is wrong in a second way.
		return n, lay
	}

	targets := make([]Expr, len(keep))
	schema := make(Schema, len(keep))
	newLay := make(outputLayout, len(keep))
	prev := -1
	for i, c := range keep {
		if c <= prev || c < 0 || c >= len(out) {
			// Out of order, duplicated or out of range: any of these would
			// permute or corrupt rather than narrow.
			return n, lay
		}
		prev = c
		col := out[c]
		targets[i] = &ColumnRef{
			Index: c,
			Name:  col.Name,
			Type:  col.Type,
			// Carried, not dropped: self-joins disambiguate by this
			// (SchemaColumn's doc names Q21's three lineitem aliases), and a
			// Project that loses it makes those columns indistinguishable.
			SourceTableIdx: col.SourceTableIdx,
		}
		schema[i] = col
		newLay[i] = lay[c]
	}
	return &Project{Child: n, Targets: targets, schema: schema}, newLay
}

// scanPathTarget computes a scan path's Slice-1 Target (planner-p4-01-target
// DESIGN, "Slice 1"): the ascending leaf-output positions of the statement's
// needed columns, read off rel at path-creation time.
//
// The second return is false ("unknown", decline) when the needed set carries
// no information (NeededColsKnown false or a nil set — the P4-01b lesson-1
// ordering hazard: any scan path created before `stampNeededColsOnRels` runs
// must record unknown rather than a wrong list) or when the rel carries no
// leaf schema to take positions from. The range loop below is the cheap
// invariant check at path creation: neededKeepSet derives its indices from
// this same schema, so a violation can never fire on valid input — and on
// invalid input it declines rather than panics, since no user query may panic.
func scanPathTarget(rel *RelOptInfo) ([]int, bool) {
	if rel == nil || !rel.NeededColsKnown || rel.NeededCols == nil || rel.baseLeaf == nil {
		return nil, false
	}
	out := rel.baseLeaf.Output()
	keep := neededKeepSet(out, rel.NeededCols)
	if keep == nil {
		// neededKeepSet returns nil only for a nil needed set, excluded
		// above; a known-but-empty set yields a non-nil empty slice. This
		// arm can never fire — decline rather than invent.
		return nil, false
	}
	for _, c := range keep {
		if c < 0 || c >= len(out) {
			return nil, false
		}
	}
	return keep, true
}

// neededKeepSet returns the ascending indices of n's output columns whose names
// are in `needed`.
//
// The join keys are preserved automatically and that is load-bearing:
// `neededColumnNames` walks the WHOLE statement, WHERE included, so any column a
// join key references is in the set by construction. There is no separate
// key-preservation pass to forget, and no ordering hazard between the two.
//
// Returns nil when `needed` is nil — "the collector declined", which must not be
// read as "keep nothing".
func neededKeepSet(out Schema, needed map[string]bool) []int {
	if needed == nil {
		return nil
	}
	keep := make([]int, 0, len(out))
	for i, col := range out {
		if needed[col.Name] {
			keep = append(keep, i)
		}
	}
	return keep
}
