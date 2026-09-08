package optimizer

// upperorderedinput.go — planner-refactor take3 C-07 (P3-06), the SEAM half.
//
// # The blocker this file removes
//
// `create_ordered_paths` (planner.c:5308) offers the input path as-is when
// `pathkeys_contained_in(input->pathkeys, root->sort_pathkeys)`. goopg's
// `createOrderedPaths` (upperordered.go) has that arm, and until this file it
// could not fire: the input above the seam is a finished NODE, the only
// Node->Path bridge is `newPrebuiltPath` (path.go), and that bridge sets
// `Kind/Rel/Rows/ParallelSafe` and leaves `Pathkeys` nil. `pathkeysContainedIn(
// nil, keys)` is false for every non-empty key list, so the Sort arm was the
// only arm production could take, and C-12's own file header recorded it:
// "the `upper.ordered.input` producer below never fires today".
//
// That is also what made C-07's second half — widening `addOrderedIndexPaths`'
// useful-column set with `pathkeys_useful_for_ordering` — inert. The widening
// really does add an `index.ordered` path (measured: useful set `[w]` -> `[w x]`,
// pathlist 1 -> 2), and no plan moved, because nothing downstream could ever
// prefer it: the ordering it delivers died at this seam.
//
// # Why the derivation is a Node walk and not a translated Path field
//
// The obvious move — carry the winning `*Path`'s `Pathkeys` out of
// `planJoinlistSearch` — crosses a COORDINATE BOUNDARY. A sub-problem is
// republished in binding order by `createPlanAtSearchRootRange`, and pathkeys
// derived in the search's inner space are meaningless outside it unless
// translated. This workstream has been bitten twice by exactly that
// (`translateToLayout` is easy; the boundary map is a TOTALITY invariant that
// panics on any hole), so nothing here translates. Two rules instead:
//
//  1. VALIDATE, NEVER TRANSLATE (`validatedSearchPathkeys`). At the boundary
//     the winning path's pathkeys are checked against the schema the root
//     actually publishes: a key survives only if it is a `*ColumnRef` whose
//     `Index` addresses a real output column and whose `Name` /
//     `SourceTableIdx` are that column's. The first key that fails TRUNCATES
//     the list. A prefix is always a sound ordering claim — rows ordered by
//     (a, b) are ordered by (a) — so truncation degrades to "less ordering
//     claimed", never to a wrong claim, and the degenerate answer (nil) is the
//     pre-C-07 behaviour exactly.
//
//  2. DESCEND ONLY THROUGH SCHEMA-IDENTICAL WRAPPERS (`inputNodePathkeys`).
//     `createOrderedPaths`' `keys` are resolved against its input's OUTPUT
//     schema (planner.go:1806, and upperordered.go §4.1 for why the Sort's own
//     key translation is a no-op for an upper rel). So a derived pathkey is
//     comparable to them only if it is expressed in that same output schema.
//     The walk therefore steps down only through nodes that are BOTH
//     order-preserving AND publish their child's schema unchanged, and it
//     re-checks the schema at every step rather than trusting the node kind.
//     Anything else stops the walk with nil.
//
// Between them, a wrong ordering claim would need a node to lie about its own
// output schema. Nothing weaker is assumed.
//
// # What still does not reach here
//
// `addOrderedIndexPaths` runs only inside the PG-shaped join search, and
// `tryPGShapedJoinSearch` declines at `nrels < 2` (joinsearchseam.go:228), so
// the canonical shape the C-07 widening serves — `SELECT ... FROM t ORDER BY
// t.pk`, one relation — never reaches the ordered-index producer at all. This
// file makes the seam carry ordering; it does not open that gate. See the
// ledger row `c07-single-rel-never-reaches-ordered-index-producer`.

// validatedSearchPathkeys is rule 1 above: the prefix of `keys` that `out`
// provably supports, in `out`'s own coordinates.
//
// PG has no analogue because PG never re-coordinates a path's output: a
// `Path`'s pathkeys and its parent rel's target live in one space for the whole
// planner run. goopg's search boundary republishes in binding order, so the
// claim has to be re-earned against the published schema rather than assumed.
//
// A non-`*ColumnRef` key stops the list rather than being skipped, for
// `build_index_pathkeys`' reason (pathkeysindex.go): an index on (a, b, c)
// whose `b` is unusable delivers (a), NOT (a, c) — rows are ordered by c only
// within equal b. The same holds for any ordering claim.
func validatedSearchPathkeys(keys []PathKey, out Schema) []PathKey {
	if len(keys) == 0 || len(out) == 0 {
		return nil
	}
	kept := make([]PathKey, 0, len(keys))
	for _, pk := range keys {
		cr, ok := pk.Expr.(*ColumnRef)
		if !ok {
			break
		}
		if cr.Index < 0 || cr.Index >= len(out) {
			break
		}
		col := out[cr.Index]
		// Name is the identity the boundary preserves; an empty name on
		// either side is a column nobody can address by name, so it cannot be
		// confirmed and the claim stops there.
		if col.Name == "" || cr.Name == "" || col.Name != cr.Name {
			break
		}
		// SourceTableIdx separates a self-join's two copies of the same
		// column name. Zero on the schema side means "not recorded", which is
		// not evidence of agreement — but it is also the value every
		// non-base-rel column carries, so it is accepted rather than treated
		// as a mismatch; a recorded value on both sides must agree.
		if col.SourceTableIdx != 0 && cr.SourceTableIdx != 0 && col.SourceTableIdx != cr.SourceTableIdx {
			break
		}
		kept = append(kept, pk)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// schemaCoordinatesAgree reports whether two schemas address the same columns
// at the same positions — the check rule 2 re-runs at every step of the walk
// instead of trusting that a node kind is schema-preserving.
func schemaCoordinatesAgree(a, b Schema) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].SourceTableIdx != b[i].SourceTableIdx {
			return false
		}
	}
	return true
}

// inputNodePathkeys is `input_path->pathkeys` for the one input goopg has above
// the seam: a finished Node. It is what lets `addOrderedPaths`' input arm fire.
//
// Two sources of an ordering claim, both expressed in the node's own output
// coordinates by construction:
//
//   - a `*Sort`: its `Keys` are written against its child's output, and a Sort
//     publishes its child's schema, so they are already in the walk's space.
//     This is the redundant-Sort case — a tree that ends in a Sort delivering
//     the ORDER BY order had a SECOND Sort stacked over it.
//   - a searched subtree root: the pathkeys the search's winner claimed,
//     already validated against the published schema at the boundary
//     (`validatedSearchPathkeys`). This is the case C-07's widening feeds:
//     an `index.ordered` path that wins the search now delivers its ordering
//     to the ORDERED upper rel instead of losing it.
//
// The walk descends through `*Filter` and `*Limit`, which are order-preserving
// (a filter removes rows, a limit truncates a prefix — neither reorders the
// rows it keeps) and schema-preserving. `*Project` is deliberately NOT in the
// list even when its output happens to agree column-for-column: a Project is
// where coordinates are re-assigned, and admitting it would put this walk back
// in the business of translation that the file header rules out.
func inputNodePathkeys(input Node) []PathKey {
	if input == nil {
		return nil
	}
	out := input.Output()
	if len(out) == 0 {
		return nil
	}
	for n := input; n != nil; {
		// The searched-root claim is checked first: a `*Sort` or `*Filter`
		// can BE a search root, and the search's own claim is the stronger
		// statement (it survived validation against this exact schema).
		if keys := searchedTreePathkeys(n); len(keys) > 0 {
			if schemaCoordinatesAgree(out, n.Output()) {
				return keys
			}
			return nil
		}
		switch t := n.(type) {
		case *Sort:
			if !schemaCoordinatesAgree(out, t.Output()) {
				return nil
			}
			return pathkeysForSortKeys(t.Keys)
		case *Filter:
			if t.Child == nil || !schemaCoordinatesAgree(out, t.Child.Output()) {
				return nil
			}
			n = t.Child
		case *Limit:
			if t.Child == nil || !schemaCoordinatesAgree(out, t.Child.Output()) {
				return nil
			}
			n = t.Child
		default:
			return nil
		}
	}
	return nil
}
