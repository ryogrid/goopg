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
// Take2 P4-01 Slice 2 (planner-p4-01-target DESIGN, "Slice 2"): the keep-set
// comes from the inner path's Slice-1 Target — the ordered emitted-column
// list computed from NeededCols at path-creation time — via `buildKeepSet`
// below, instead of being re-derived from NeededCols by name at plan time.
// Where the Target does not apply (unknown, or a narrowed scan whose schema
// is not the Target's coordinate space) the derivation falls back to today's
// NeededCols path bit-identically, so the only observable change is the
// SOURCE of an identical list. `narrowPlanOutput`'s ascending/unique/in-range
// guard is untouched: a Target in emitted-schema order passes it without
// loosening, and anything else declines with the pair untouched.
func narrowBuildInput(kind string, innerNode Node, innerLay outputLayout, innerPath *Path) (Node, outputLayout) {
	if !narrowBuild || kind != "PathHashJoin" {
		return innerNode, innerLay
	}
	if innerNode == nil || innerPath == nil || innerPath.Rel == nil || !innerPath.Rel.NeededColsKnown {
		return innerNode, innerLay
	}
	if keep, ok := buildKeepSet(innerNode, innerPath); ok {
		return narrowPlanOutput(innerNode, innerLay, keep)
	}
	return narrowPlanOutput(innerNode, innerLay, neededKeepSet(innerNode.Output(), innerPath.Rel.NeededCols))
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
