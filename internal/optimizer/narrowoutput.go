package optimizer

import "os"

// narrowBuild resolves GOOPG_NARROW_BUILD at process start. Default OFF:
// narrowing the build side changes plans (fewer batches at a fixed
// work_mem), so it lands behind a flag and is measured (P4-A §18 step 4)
// before any default flip (step 5). Read once, in the server: putting it on
// a client command line sets it where nothing reads it (handover §2).
var narrowBuild = narrowBuildFromEnv(os.Getenv("GOOPG_NARROW_BUILD"))

// narrowBuildFromEnv is the flag's polarity, factored out so tests and the
// flag-provenance table resolve the same default the process starts with
// (flaglabels.go's contract: no literal restating a default elsewhere).
func narrowBuildFromEnv(v string) bool { return v == "1" }

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
// Every refusal returns the pair untouched: flag off (the shipped default),
// a non-hash join, no node, no path/rel, or an unknown needed set
// (NeededColsKnown false — the collector declined, which must not be read
// as "keep nothing"). With the flag off the shipped path is untouched, so
// this step's gate measures the flag rather than the commit.
func narrowBuildInput(kind string, innerNode Node, innerLay outputLayout, innerPath *Path) (Node, outputLayout) {
	if !narrowBuild || kind != "PathHashJoin" {
		return innerNode, innerLay
	}
	if innerNode == nil || innerPath == nil || innerPath.Rel == nil || !innerPath.Rel.NeededColsKnown {
		return innerNode, innerLay
	}
	return narrowPlanOutput(innerNode, innerLay, neededKeepSet(innerNode.Output(), innerPath.Rel.NeededCols))
}

// Build-side output narrowing — take2 P4-01, rev 10 step 2.
//
// A join's build side carries every column of every relation beneath it, and a
// goopg hash entry costs `48 × columns + 24` bytes (hashsize.EntryBytes),
// whatever those columns hold. Dropping the ones no part of the statement
// references shrinks the hash table proportionally.
//
// This file provides the transformation only. Nothing calls it yet; the call
// site is `joinInputsFor`, behind GOOPG_NARROW_BUILD, in the next slice.
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
