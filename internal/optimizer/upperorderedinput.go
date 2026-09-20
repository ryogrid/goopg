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

// validatedSearchCandidateKeys is rule 1 generalized from the single winning
// path (`stampSearchPathkeys`'s own call) to every candidate a search rel's
// Pathlist carries (M0141-S2b-2b). PG has no analogue here either, for the
// same reason the file header gives for `validatedSearchPathkeys`: goopg's
// search boundary re-coordinates once per search, and a losing candidate's
// ordering claim is just as much a claim made in the search's inner space as
// the winner's — nothing about NOT being chosen by `setCheapest` exempts a
// candidate from re-earning its claim against the schema `out` actually
// publishes.
//
// Returns one entry per `candidates`, parallel-indexed (nil where that
// candidate's own claim validates to nothing) — never a shorter slice, so a
// caller can zip it against `candidates` by index without a length check.
func validatedSearchCandidateKeys(candidates []*Path, out Schema) [][]PathKey {
	if len(candidates) == 0 {
		return nil
	}
	keys := make([][]PathKey, len(candidates))
	for i, c := range candidates {
		if c == nil {
			continue
		}
		keys[i] = validatedSearchPathkeys(c.Pathkeys, out)
	}
	return keys
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
// rows it keeps) and schema-preserving.
//
// M0144-0011a-2 adds a THIRD, narrower descent: a `*Project` that is a
// positional identity (`projectIsPositionalIdentity`). PG states the general
// rule outright — "Projection does not change the sort order",
// `postgres/src/backend/optimizer/util/pathnode.c:2936-2937`, where
// `create_projection_path` copies `subpath->pathkeys` unchanged — and it can
// state it in that generality because a PG pathkey names an
// EquivalenceClass, not an output position. goopg's pathkeys name POSITIONS,
// so the general rule does not transfer: a Project is exactly where positions
// are re-assigned, and the previous comment here refused every Project for
// that reason.
//
// The identity case is the part that does transfer without any translation.
// When target `j` is `ColumnRef{Index: j}` for every `j` and the column counts
// agree, the projection re-assigns nothing — it can only RENAME. A rename does
// not move a row and does not move a column, so the claim below it is already
// expressed in the right coordinates; only its labels belong to the child's
// vocabulary. `relabelPathkeysTo` restamps those labels from `out` at the SAME
// index, which is a relabel and not a mapping (and is cosmetic in any case:
// `exprEqual` compares a `*ColumnRef` on `Index` alone, exprwalk.go:668). Any
// other Project still stops the walk.
//
// This is the residue M0144-0011a measured and could not reach: TPC-DS SF0.25
// Q21 reaches the ORDERED step as `Project{Aggregate}` — a pure rename of the
// two aggregate outputs to `inv_before`/`inv_after` — so the `*Aggregate` arm
// below was never consulted and a redundant Sort survived 0011a.
func inputNodePathkeys(input Node) []PathKey {
	if input == nil {
		return nil
	}
	out := input.Output()
	if len(out) == 0 {
		return nil
	}
	// Set once a positional-identity `*Project` has been crossed. From that
	// point the published schema's LABELS may differ from the node's own,
	// while its POSITIONS provably do not — so the per-step agreement check
	// weakens to the column count and the delivered claim is relabelled.
	renamed := false
	agrees := func(s Schema) bool {
		if renamed {
			return len(s) > 0 && len(s) == len(out)
		}
		return schemaCoordinatesAgree(out, s)
	}
	deliver := func(keys []PathKey) []PathKey {
		if !renamed {
			return keys
		}
		return relabelPathkeysTo(keys, out)
	}
	for n := input; n != nil; {
		// The searched-root claim is checked first: a `*Sort` or `*Filter`
		// can BE a search root, and the search's own claim is the stronger
		// statement (it survived validation against this exact schema).
		if keys := searchedTreePathkeys(n); len(keys) > 0 {
			if agrees(n.Output()) {
				return deliver(keys)
			}
			return nil
		}
		switch t := n.(type) {
		case *Sort:
			if !agrees(t.Output()) {
				return nil
			}
			return deliver(pathkeysForSortKeys(t.Keys))
		case *Filter:
			if t.Child == nil || !agrees(t.Child.Output()) {
				return nil
			}
			n = t.Child
		case *Limit:
			if t.Child == nil || !agrees(t.Child.Output()) {
				return nil
			}
			n = t.Child
		case *Aggregate:
			if !agrees(t.Output()) {
				return nil
			}
			return deliver(aggregateEmissionPathkeys(t))
		case *Project:
			if !projectIsPositionalIdentity(t) {
				return nil
			}
			renamed = true
			n = t.Child
		default:
			return nil
		}
	}
	return nil
}

// aggregateEmissionPathkeys is M0144-0011a: the `*Aggregate` arm of
// `inputNodePathkeys`' walk — the third source of an ordering claim at this
// seam, alongside the `*Sort` top and the searched-subtree root.
//
// # Why the arm has to exist
//
// PG never loses a grouping path's ordering on the way to
// `create_ordered_paths`: an `AggPath` with `aggstrategy == AGG_SORTED`
// carries its subpath's pathkeys (`create_agg_path`,
// `postgres/src/backend/optimizer/util/pathnode.c:3412-3416` sets
// `pathnode->path.pathkeys = subpath->pathkeys`), and
// `create_ordered_paths` then reads exactly that field for every member of
// `input_rel->pathlist` (`postgres/src/backend/optimizer/plan/planner.c:5337`,
// `:5344-5348`). A GroupAggregate that already emits in ORDER BY order is
// therefore taken as-is and no Sort is stacked over it.
//
// goopg's seam publishes a finished Node, and until this arm the walk's
// `default: nil` swallowed `*Aggregate` — so the ORDERED step re-seeded with
// `keys=0` and `addOrderedPaths` could only take its `create_sort_path` arm.
// That is the redundant `Sort` over `GroupAggregate` the M0144-0011 Q8 trace
// measured as the slice's layer-2 first divergence
// (`analysis/m0144/m0144-0011-q8-slice-trace.md`).
//
// # Why it is a derivation and not a copy
//
// The node's own `GroupExprs` and its child Sort's `Keys` are written in the
// aggregate's INPUT coordinates; `createOrderedPaths`' `keys` are resolved
// against the aggregate's OUTPUT schema. This is the one coordinate boundary
// the file header's rule 2 does not let the walk step over by descending, so
// the arm translates positionally instead — and only where the translation is
// a tautology rather than a mapping: the aggregate's group-prefix output
// layout (`[groups|aggs|grouping|passthrough]`, plan.go:1310-1359) puts group
// key `j` at output position `j`, which is VERIFIED per key
// (`out[j].Name == groupExprName(groups[j])`), never assumed.
//
// It is the node-level twin of `groupingEmissionPathkeys`
// (upperorderedgrouping.go), which does the same job for an unbuilt `*Path`
// candidate inside `electOrderedGrouping`'s loop; the two must decline on the
// same shapes (pattern: sibling paths must agree). Every decline below mirrors
// one of that function's, for the reason stated there:
//
//   - non-sorted strategy: a hashed aggregate emits in no order at all.
//   - non-simple mode / grouping sets / no group keys: mirrors the executor's
//     sorted-agg guard (operators_join_agg.go), which runs sorted aggregation
//     only for Simple-mode, set-free, grouped aggregation.
//   - `GroupKeyOrder != nil`: the EXPLAIN-only index-remapped permutation —
//     its group-key positions are not the written ones this translation reads.
//   - child neither `*Sort` nor `*GatherMerge`: nothing else at this position
//     delivers a stated order (a Gather Merge emits the merged order of its
//     sorted inputs — the same delivery contract a Sort gives, and the one PG
//     relies on when it feeds Gather Merge paths into `create_ordered_paths`).
//   - the child's leading sort keys are not the group keys positionally, or a
//     group expression is not a bare `*ColumnRef`, or the output column at
//     that position does not carry the group key's name: the emission order
//     cannot be named in output coordinates, so nothing is claimed.
//
// A sorted aggregate emits groups in the order they complete, which is the
// lexicographic order of its sorted input restricted to distinct group values.
// Trailing child sort keys beyond the group list cannot change that order, so
// they are allowed and simply not claimed.
//
// Returns nil ("no claim") on every decline — the pre-M0144-0011a answer
// exactly, which stacks the Sort as before.
func aggregateEmissionPathkeys(agg *Aggregate) []PathKey {
	if agg == nil {
		return nil
	}
	if agg.Strategy != AggStrategySorted || agg.Mode != AggModeSimple ||
		agg.GroupingSets != nil || len(agg.GroupExprs) == 0 ||
		agg.GroupKeyOrder != nil {
		return nil
	}
	var childKeys []SortKey
	switch c := agg.Child.(type) {
	case *Sort:
		childKeys = c.Keys
	case *GatherMerge:
		childKeys = c.Keys
	default:
		return nil
	}
	groups := agg.GroupExprs
	if len(childKeys) < len(groups) {
		return nil
	}
	out := agg.Output()
	if len(out) < len(groups) {
		return nil
	}
	for j, g := range groups {
		// Bare group keys only: the output side names positions, and only a
		// column has a name. Both sides are input-coordinate here, so
		// `exprEqual`'s positional `Index` equality is the match.
		if _, ok := g.(*ColumnRef); !ok {
			return nil
		}
		if !exprEqual(childKeys[j].Expr, g) {
			return nil
		}
		// An empty name is a column nobody can address by name, so the claim
		// cannot be confirmed and stops there.
		if out[j].Name == "" || out[j].Name != groupExprName(g) {
			return nil
		}
	}
	emitted := make([]PathKey, len(groups))
	for j := range groups {
		emitted[j] = PathKey{
			Expr:       &ColumnRef{Index: j, Name: out[j].Name, Type: out[j].Type},
			SortAsc:    !childKeys[j].Desc,
			NullsFirst: childKeys[j].NullsFirst,
		}
	}
	return emitted
}

// projectIsPositionalIdentity reports whether `p` re-assigns no output
// position — target `j` is the child's column `j`, for every `j`, over an
// equally wide child. Such a Project can only RENAME (and re-type-label) its
// columns; it cannot move, drop, duplicate or compute one.
//
// This is the whole soundness argument for the walk's `*Project` arm. PG needs
// none of it — `create_projection_path` copies `subpath->pathkeys` for ANY
// projection (`postgres/src/backend/optimizer/util/pathnode.c:2936-2937`),
// because a PG pathkey names an EquivalenceClass and is therefore immune to
// output-position changes. goopg's pathkeys name positions, so the general
// rule cannot be ported; the identity special case can, because under it
// "PG's position" and "goopg's position" coincide by construction.
//
// Deliberately refused, each with its own reason:
//
//   - `IsolatedScope`: its `Targets`' `ColumnRef`s are inner-indexed then
//     outer-relabelled (see the field's doc comment on `*Project`), so an
//     `Index == j` test is not reading the coordinate it appears to read.
//   - a narrowing projection (`len(out) < len(child)`): positions 0..len(out)-1
//     would still be an identity and a claim on them would still be sound, but
//     the walk's per-step agreement check is a column-count test and would have
//     to grow a second mode to express "prefix". Filed rather than guessed —
//     see the M0144-0011a-2 ledger row.
//   - a permutation (`Index != j`): sound only with a position MAP, which is
//     precisely the translation this file's header rules out.
//   - any non-`*ColumnRef` target: a computed column is a new value, and an
//     ordering claim on the input value says nothing about it.
func projectIsPositionalIdentity(p *Project) bool {
	if p == nil || p.Child == nil || p.IsolatedScope {
		return false
	}
	out := p.Output()
	child := p.Child.Output()
	if len(out) == 0 || len(out) != len(child) || len(p.Targets) != len(out) {
		return false
	}
	for j, t := range p.Targets {
		cr, ok := t.(*ColumnRef)
		if !ok || cr.Index != j {
			return false
		}
	}
	return true
}

// relabelPathkeysTo restamps a claim's column labels from `out` at the SAME
// index — the only adjustment a positional-identity `*Project` can require.
//
// It is NOT a translation: no index changes hands. It exists so the claim the
// seam publishes is spelled in the vocabulary of the schema the seam actually
// publishes (Q21's `inv_before`, not the aggregate's `sum`), which keeps the
// DPPATH trace and any future name-sensitive consumer honest. Today it is
// cosmetic — `exprEqual` compares a `*ColumnRef` on `Index` alone
// (exprwalk.go:668), so `pathkeysContainedIn` would agree either way.
//
// A key that cannot be restamped TRUNCATES the list, for
// `validatedSearchPathkeys`' reason: an ordering by (a, b, c) whose `b` is
// unusable delivers (a), never (a, c).
func relabelPathkeysTo(keys []PathKey, out Schema) []PathKey {
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
		if col.Name == "" {
			break
		}
		kept = append(kept, PathKey{
			Expr: &ColumnRef{
				Index:          cr.Index,
				Name:           col.Name,
				Type:           col.Type,
				SourceTableIdx: col.SourceTableIdx,
			},
			SortAsc:    pk.SortAsc,
			NullsFirst: pk.NullsFirst,
		})
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
