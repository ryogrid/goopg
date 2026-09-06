package optimizer

// Join-tree LAYOUT and SEARCH-BOUNDARY translation.
//
// This file was `bushy.go` until M0127-P6.3 (2026-08-07), when the old
// subset-bitmask join DP it was named for was deleted (leftdeep-joins 08 §4).
// What survived the deletion is not the enumerator but the coordinate
// machinery that sat beside it: the pos-map family that translates a rewritten
// join tree's column indices back into the coordinates its parents were
// resolved in, and the by-NAME re-resolvers that repair a layout no index can
// address any more.
//
// The rename is the point. Everything here answers ONE question — "the tree
// below me is not the tree my ColumnRefs were numbered against; what is the
// map?" — and none of it enumerates a join order. The enumerator is
// `joinSearchOneLevel` (joinsearchlevel.go) behind the seam in
// joinsearchseam.go.
//
// # Why this family is still here at all
//
// 08 §4 splits the layout/remap machinery in two and deletes only one half.
// The per-subset half (`dpEntry.layout`, `remapKeyToLayout`,
// `mergeSubsetLayouts`) died with the DP: under the canonical relid-ordered
// layout of 02 §3 a joinrel's column order is a pure function of its relset,
// so no enumeration-time layout state survives to be merged.
//
// The SEARCH-BOUNDARY half — `buildBindingsPosMap` and `applyJoinTreePosMap` —
// was held back until the 03 §10 boundary map was proven in production. C-20b
// took that measurement (take3 08 §9.2, 2026-09-07) and the pair is GONE: over
// TPC-H (22 queries) and TPC-DS (99), on both `GOOPG_PGSHAPED_DP` arms, the
// three passes that drove it were reached up to 408 times and moved ZERO
// ColumnRefs, and `EXPLAIN` text is byte-identical without them. The tombstone
// above `remapByPosMap` carries the table.
//
// What is left in this file is therefore NOT the boundary translation any
// more. It is two things:
//
//   - `remapByPosMap` / `remapOuterRefsInSubplan`, a posMap APPLIER with no
//     posMap builder of its own — `predp.go`'s pinned-spine re-resolution
//     brings its own map; and
//   - the by-NAME re-resolvers (`reresolveJoinByName`, `reconcileNLILayout`
//     and friends), which `predp.go` and `nl_index_join.go` call directly and
//     which `assertSearchedTreeNeedsNoReconcile` runs as the searched tree's
//     independent oracle on every searched plan.
//
// `reconcileNLILayout` stays for the reason 08 §3/§9.3 gives and C-20b did not
// disturb: it is the ORACLE for that live tripwire, so deleting it removes the
// check, not the code.

import "fmt"

// `scanKey` — the (table pointer, alias) pair that keyed `buildBindingsPosMap`'s
// pre/post-rewrite leaf correspondence, so that a self-join's two instances
// (`nation n1, nation n2`) did not collapse onto one entry — went with that
// function in C-20b. The self-join disambiguation itself survives in
// `lookupColumnIndexByNameAndSource`, keyed on `SchemaColumn.SourceTableIdx`
// (goopg's `varno`) rather than on the pointer/alias pair.

// `collectMultiHashTables` + `isCanonicalKeyEquality` lived here until
// M0127-P6.2 (08 §4). They were the MultiHashJoin packer's chain detector:
// walk a left-deep all-inner hash cascade, resolve each edge's keys by column
// NAME (DFS collection order and FROM order disagree for a restructured tree),
// fail closed unless the N scans are joined by exactly N-1 keys forming a
// simple chain, then pick the widest scan as the probe. All of it died with
// the node it packed. `join_hash_keys.go` carries the surviving statement of
// the canonical-equality convention the detector relied on.

// `extraInScans` lived here until M0127-P6.2. It was the packer's admission
// guard for residual conjuncts destined for `MultiHashJoin.Filters`, and the
// fail-open that M0125-0002 commit 7 was scoped around: `allMatched` starts
// true and is only falsified from inside the callback, so a conjunct built
// entirely from kinds the pre-conversion 7-arm walker did not enumerate
// produced ZERO callbacks and was admitted on a vacuous true. The second
// result of `visitColumnRefsByName` closed it — and that inversion outlives
// this caller: `pushdown.go`'s two consumers still seed `true` and still rely
// on "the name test did not cover c" reading as NOT MATCHED.

// visitColumnRefsByName invokes fn on each named ColumnRef in e and
// reports whether the name test COVERED e — i.e. every node of e was
// enumerated, no inner-scope plan was crossed, and nothing in e reads
// row data without naming the column it reads.
//
// M0125-0002 commit 7 (the last of the series): re-based onto
// walkExprRefs, so the arm set is exprChildSlots' 32 types rather than
// this walker's historical 7. Every consumer seeds its verdict `true`
// and falsifies it only from the callback, which made an unenumerated
// kind read as "all names matched" — hence the second result. It is not
// an error signal: a caller that gets false has learned the test is not
// applicable, and each of the three call sites is a fail-CLOSED
// admission guard, so false costs an optimisation and never a wrong row.
//
// Scope policy is scopeSignal, per D3: an inner plan is neither walked
// (its indices live in another coordinate space, and its correlations
// name the parent scope) nor silently stepped over (that is exactly the
// vacuous true being removed) — it is reported, and reporting it clears
// `total`.
//
// The Visit switch names three kinds that ARE enumerated and still
// cannot be certified by a name test, because each reads row data
// without naming a column:
//
//   - a *ColumnRef whose Name is empty. Name is "for diagnostics" per
//     its own struct comment and IS empty on some construction paths;
//     the old body skipped those silently, which is the vacuous true in
//     miniature — an unnamed ref is precisely a ref the test cannot
//     check.
//   - *OuterColumnRef — names a column of a DIFFERENT scope, so
//     matching it against this subtree's scan names would be a
//     coincidence, not evidence. (Commit 2 vetoed it for the same
//     reason on the rewriting side.)
//   - *MergeWholeRowRef — the composite is materialised from ctx over
//     the whole row; no single name is testable.
//
// *CTIDExpr joins them: seqScanOp injects the scanned row's
// block/offset into its slot, so it reads the row of whichever side is
// being scanned and carries no name at all.
//
// Deliberately NOT vetoed, because they read no row column:
// *ParamRef / *ExecParamRef (bound outside the row), *TableOidExpr (a
// constant per table) and *MergeActionExpr (MERGE action state, not a
// column).
func visitColumnRefsByName(e Expr, fn func(string)) bool {
	total := true
	walkExprRefs(e, scopeSignal, exprVisitor{
		Visit: func(n Expr) bool {
			switch x := n.(type) {
			case *ColumnRef:
				if x.Name == "" {
					total = false
					return true
				}
				fn(x.Name)
			case *OuterColumnRef, *CTIDExpr, *MergeWholeRowRef:
				total = false
			}
			return true
		},
		OnScope:   func(Node) { total = false },
		OnUnknown: func(Expr) { total = false },
	})
	return total
}

// `findScanByColName` and `rewriteMultiWayChain` lived here until M0127-P6.2
// (08 §4). `rewriteMultiWayChain` was the packing pass planSelect ran after
// join-order search: detect a chain, build the `MultiHashJoin`, OID-sort its
// tables so the packed schema matched the FROM order of the binary tree it
// replaced, then hand the node to `rewriteMHJInputsWithSingleTablePredicates`
// to promote SeqScan inputs to IndexScan. Its own guard already refused to
// pack a searched tree (`isSearchedTree`, M0127-P5.9-b) because the packer
// re-sorted the leaf layout the search had costed — the order-then-rewrite
// mismatch that regressed Q9. With the PG-shaped search as the only search,
// that guard covered every production call, so the pass was already a no-op
// before it was deleted.

// `remapColumnRefsAfterRewrite` and `remapPosMapAfterRewrite` lived here until
// C-20b (take3 08 §9.2, 2026-09-07). They were the first of the three
// post-rewrite remap passes `planSelect` ran, and by the end they mutated
// NOTHING: `remapPosMapAfterRewrite` took a `posMap func(int) int`, never read
// it, and its ~110-line body contained no assignment to any `Index` field —
// every recursive call passed `nil`. The mutation it once performed was the
// MHJ posmap, deleted with the node by M0127-P6.2; what survived the deletion
// was the tree walk around it.
//
// This is the one deletion in C-20b that needed no census: "the parameter is
// never read and the body assigns nothing" is a proof, not a measurement.
// The measured half is `buildBindingsPosMap`'s family below.

// `binaryTreePosMapOf` (dead — nothing had called it for several milestones)
// and `remapExprRefsToMHJ` (a one-line alias for `remapColumnRefsAfterRewrite`,
// kept only because callers predated the rename) were deleted by M0127-P6.2
// along with the `buildMHJPosMap` they fed.

// `remapWithBindings`, `remapTopProjection` and `remapAggExprsWithBindings`
// — the three bindings-keyed posmap passes `planSelect` ran after the legacy
// rewrites — were deleted by C-20b (take3 08 §9.2, 2026-09-07), together with
// the `buildBindingsPosMap` / `applyJoinTreePosMap` pair below that built and
// applied their map, and the `mhjPosMapOf` tombstone that named them as its
// successor.
//
// They were deleted on MEASUREMENT, to the gate C-20c failed. A census over
// TPC-H (22 queries) and TPC-DS (99), on both `GOOPG_PGSHAPED_DP` arms,
// counted every pass ENTRY, every application of a non-nil map, and every
// `ColumnRef.Index` / `OuterColumnRef.Index` the map actually MOVED:
//
//	corpus  DP  withBindings  topProjection  aggExprs  MOVES
//	TPC-H    1     46 / 38        14 / 13     32 / 25    0
//	TPC-H    0     46 / 19        11 /  7     32 / 12    0
//	TPC-DS   1    408 / 101      134 / 47    214 / 54    0
//	TPC-DS   0    408 / 292      194 / 106   214 / 186   0
//
// (entries / applications). The passes ran constantly and moved nothing, and
// `EXPLAIN` text over both corpora is BYTE-IDENTICAL with them removed on all
// four arms. The single move observed anywhere was one index under
// `TestSAOPMultiTableRewriteMoves`, a test that explicitly selects the legacy
// enumerator; it passes unchanged without the pass.
//
// Why they were still running at all: each existed to repair a tree that a
// LATER pass had reordered in place — the subset-bitmask DP and the MHJ
// packer. Both were deleted at M0127-P6.2/P6.3, and the PG-shaped search that
// replaced them answers the question in the opposite direction: every
// `createPlan` arm translates its own clauses onto its own merged row as it
// builds it (P5.5-e-i), and the boundary republishes the root in pre-search
// binding order (P5.5-f-i), so there is nothing left behind to correct. The
// searched subtree was already opaque to all three (`isSearchedTree`,
// searchedtree.go). What the census adds is that the LEGACY arm has nothing
// to correct either.
//
// What did NOT go: `remapByPosMap` and `remapOuterRefsInSubplan` below stay —
// `predp.go`'s pinned-spine re-resolution is a live caller with a map of its
// own — and so do the by-NAME re-resolvers, which `predp.go` and
// `nl_index_join.go` call and which `assertSearchedTreeNeedsNoReconcile` runs
// as the searched tree's independent oracle (take3 08 §9.3: deleting that one
// removes the check, not the code).

// remapByPosMap rewrites every same-scope ColumnRef.Index in *e through
// posMap — a position map built from the MultiHashJoin's bindings, so it
// handles duplicate column names across table instances (TPC-H Q7's two
// nation scans) — and translates the Level-1 OuterColumnRefs of any inner
// plan via remapOuterRefsInSubplan.
//
// M0125-0002 commit 1 (docs/design/0125-0002-walker-conversion-and-mhj-
// composition-risk.md, D2 row 1): re-based onto exprwalk.go's
// rewriteExprRefsInPlace. The 18-arm hand-written type switch is gone;
// child structure now comes from the single primitive exprChildSlots, so a
// 33rd Expr type is a build-time failure (exprwalk_exhaustive_test.go)
// instead of the silent no-op that made TPC-DS Q76 return 0 rows instead
// of 100 — `WHERE ss_customer_sk IS NULL` kept its pre-rewrite index
// because there was no *IsNullExpr arm, so IS NULL was evaluated against a
// date_dim column that is never NULL (round-2 README §2, the RC-1a class).
//
// Behaviour is deliberately UNCHANGED, which is why this is the one
// M0125-0002 commit that expects an empty plan diff; remap_arms_test.go's
// §2.6 pins are the proof. Three choices carry that equivalence:
//
//   - Driver is rewriteExprRefsInPlace, NOT cloneExprRefs: containers are
//     mutated in place and a ColumnRef is copied only when its index
//     actually moves. A whole-tree clone would replace nodes an identity
//     remap must leave shared.
//   - scopePolicy is scopeIgnore, so inner plans are not reached through
//     the driver at all. The two kinds of inner plan here need OPPOSITE
//     treatment and a policy cannot tell them apart: InExpr.Plan was
//     already remapped by the caller and must not be touched, while
//     Exists/Subquery/ArraySubquery/MultiAssignSubq* must have their
//     Level-1 outer refs translated. Rewrite below owns that split.
//   - An unenumerated type PANICS rather than being skipped — the
//     `default:` this walker never had.
func remapByPosMap(e *Expr, posMap func(int) int) {
	if e == nil || *e == nil {
		return
	}
	var unknown Expr
	ok := rewriteExprRefsInPlace(e, scopeIgnore, exprRewriter{
		// Called BOTTOM-UP, once per node. Only the types that need work
		// BEYOND same-scope child descent appear here; every other type is
		// handled entirely by the driver's slot walk, so this switch is
		// neither recursive nor required to be exhaustive. That is why the
		// census pin in exprwalk_inventory_test.go DEMOTES to
		// `nonRecursiveClassifier` rather than disappearing: the recursion
		// and the exhaustiveness both moved to exprChildSlots.
		Rewrite: func(x Expr) Expr {
			switch n := x.(type) {
			case *ColumnRef:
				newIdx := posMap(n.Index)
				if newIdx == n.Index {
					// Share on a no-op remap. Identity maps are common
					// enough for this to matter, and
					// TestRemapByPosMap_IdentityMapSharesNodes pins it.
					return x
				}
				// Copy on change: expression nodes are shared between
				// plan fragments, so mutating Index in place would
				// retro-remap a fragment that was already correct.
				cl := *n
				cl.Index = newIdx
				return &cl

			// ---- inner plans, handled here rather than by the policy ---
			// EXISTS/NOT EXISTS subqueries are never unnested into a join
			// by this point (M0071-0009's Semi/Anti unnesting only fires
			// for equality-correlated IN/=ANY shapes) — the inner Plan is
			// evaluated in place at filter/leaf time with the outer row
			// supplied via ctx.OuterRows, indexed by the correlated
			// OuterColumnRef's Index. That Index was resolved against the
			// PRE-rewrite (OID-sorted) outer schema; after the
			// MultiHashJoin rewrite reorders columns it must be translated
			// through the same posMap or it silently reads the wrong outer
			// column (AI-20260707-000712-005 / TPC-H Q21: read l_comment
			// where l_suppkey was meant, producing a numeric-cast error on
			// text). The subquery's Args are PARAM_EXEC-style arguments
			// evaluated against the CURRENT outer row, so they are
			// same-scope slots and the driver already descended them.
			case *ExistsExpr:
				remapOuterRefsInSubplan(n.Plan, 1, posMap)
			case *SubqueryExpr:
				remapOuterRefsInSubplan(n.Plan, 1, posMap)
			case *ArraySubqueryExpr:
				remapOuterRefsInSubplan(n.Plan, 1, posMap)
			case *MultiAssignSubqRow:
				// Plan is a Node, not an Expr — an inner scope, handled
				// the same way as SubqueryExpr.
				remapOuterRefsInSubplan(n.Plan, 1, posMap)
			case *MultiAssignSubqElem:
				// Reached through the statically-typed Row field, which
				// the driver steps over (slotSubqRow under scopeIgnore).
				if n.Row != nil {
					remapOuterRefsInSubplan(n.Row.Plan, 1, posMap)
				}
			}
			return x
		},
		OnUnknown: func(x Expr) { unknown = x },
	})
	if !ok {
		// scopeIgnore never vetoes, so a false result can only mean
		// OnUnknown fired. PG-faithful: expression_tree_walker_impl and
		// expression_tree_mutator_impl both close with
		// `elog(ERROR, "unrecognized node type: %d")`
		// (postgres/src/backend/nodes/nodeFuncs.c:2667 and :3743), which
		// the server's recover() surfaces as XX000. Silence is not an
		// option here: a subtree the remap stepped over keeps its
		// pre-rewrite indices inside an otherwise-remapped predicate, and
		// the predicate then reads a different table's column — a wrong
		// answer, not a missed optimisation.
		panic(fmt.Sprintf("remapByPosMap: unrecognized expression type %T — teach "+
			"exprChildSlots (internal/planner/exprwalk.go) about it", unknown))
	}
}

// remapOuterRefsInSubplan walks a correlated subquery's inner plan
// (ExistsExpr.Plan / SubqueryExpr.Plan / ArraySubqueryExpr.Plan) and
// translates any OuterColumnRef whose Level places it at the scope
// currently being remapped (depth) through posMap. depth starts at 1
// for the subquery's immediate outer scope (the plan node that owns
// the Filter/Project/Sort/Aggregate currently being remapped by
// remapByPosMap) and increases by one for each further level of
// subquery nesting encountered, matching the ctx.OuterRows stack
// depth `Level` indexes against at evaluation time (see
// executor/expr.go's OuterColumnRef case).
//
// remapByPosMap only rewrites the ColumnRef/BinaryOp/etc. skeleton of
// the predicate/target it is given; it does not otherwise descend
// into a correlated subquery's own plan tree, so without this an
// EXISTS or scalar subquery referencing the outer row would silently
// keep stale pre-MultiHashJoin-rewrite indices.
func remapOuterRefsInSubplan(node Node, depth int, posMap func(int) int) {
	if node == nil {
		return
	}
	var visit func(Expr)
	visit = func(e Expr) {
		switch x := e.(type) {
		case *OuterColumnRef:
			if x.Level == depth {
				x.Index = posMap(x.Index)
			}
		case *ExistsExpr:
			remapOuterRefsInSubplan(x.Plan, depth+1, posMap)
		case *SubqueryExpr:
			remapOuterRefsInSubplan(x.Plan, depth+1, posMap)
		case *ArraySubqueryExpr:
			remapOuterRefsInSubplan(x.Plan, depth+1, posMap)
		case *InExpr:
			if x.Plan != nil {
				remapOuterRefsInSubplan(x.Plan, depth+1, posMap)
			}
		}
	}
	walkPlanExprs(node, visit)
}

// findUniqueColumnIndex returns the unique index of `name` in
// `schema` (plus `offset`), or -1 when the name is absent or
// appears more than once. Lifted out of `reresolveJoinByName`'s
// closure (M0063-0001) so the NLI rewrite path can re-bind a
// derived-table outer's Key index by Name.
func findUniqueColumnIndex(schema Schema, name string, offset int) int {
	idx, _ := lookupColumnIndexByName(schema, name, offset)
	return idx
}

// lookupColumnIndexByName is `findUniqueColumnIndex` with the two ways of
// failing told apart: it returns (-1, true) when the name appears more than
// once and (-1, false) when it does not appear at all.
//
// M0127-P5.9-i: the distinction is not decoration. A caller that may consult a
// SECOND schema after a miss — `reresolveJoinByName`'s `predRebind` is the only
// one — must treat ambiguity as "stop", because the reference demonstrably
// belongs to THIS side and the resolver simply cannot say where. Conflating the
// two makes it walk to the other side and rebind a correctly-bound reference
// onto a different relation's column of the same name.
func lookupColumnIndexByName(schema Schema, name string, offset int) (int, bool) {
	hit := -1
	for i, c := range schema {
		if c.Name == name {
			if hit >= 0 {
				return -1, true // ambiguous
			}
			hit = i + offset
		}
	}
	return hit, false
}

// findColumnIndexByNameAndSource (M0071-0009) returns the index
// of the column whose Name and SourceTableIdx both match, plus
// the given offset. Returns -1 when no match or multiple matches.
// Used by predRebind / NLI Key rebind when the binder's
// SourceTableIdx is known and Name alone may be ambiguous
// (self-joins like Q21's lineitem l1/l2/l3).
//
// `sourceTableIdx == 0` is the "unknown / derived" sentinel —
// callers must not invoke this helper with a zero source idx;
// they should fall back to findUniqueColumnIndex instead.
func findColumnIndexByNameAndSource(schema Schema, name string, sourceTableIdx int16, offset int) int {
	idx, _ := lookupColumnIndexByNameAndSource(schema, name, sourceTableIdx, offset)
	return idx
}

// lookupColumnIndexByNameAndSource is `findColumnIndexByNameAndSource` with the
// duplicate case reported instead of folded into the miss — see
// `lookupColumnIndexByName` for why the difference matters.
//
// M0127-P5.9-i also settled what the duplicate case IS. The old comment called
// it "shouldn't happen in well-formed schemas": that is true only within one
// query scope, which is the case M0071-0009 was written for (Q21's `l1/l2/l3`
// are three range-table entries of ONE scope, so three distinct source
// indices). It is false across scopes. TPC-DS Q83 joins three CTE scans whose
// `item_id` each descends from `item.i_item_id` inside a separate WITH arm;
// every arm numbers its own range table, so all three columns carry the same
// source identity and the pair (Name, SourceTableIdx) is genuinely ambiguous.
// Seven TPC-DS queries are in this family (Q11, Q31, Q47, Q57, Q58, Q74, Q83) —
// a shape none of TPC-H's 22 queries produce.
func lookupColumnIndexByNameAndSource(schema Schema, name string, sourceTableIdx int16, offset int) (int, bool) {
	hit := -1
	for i, c := range schema {
		if c.Name == name && c.SourceTableIdx == sourceTableIdx {
			if hit >= 0 {
				return -1, true // ambiguous — the same name from the same source twice
			}
			hit = i + offset
		}
	}
	return hit, false
}

// reresolveJoinByName re‑binds ColumnRef indices in a Join's keys
// and predicate by matching ColumnRef.Name against the actual output
// schemas of n.Left and n.Right. Used after rewriteMultiWayChain to
// fix indices that pointed into the pre‑rewrite (subset‑FROM‑order)
// schema and now need to land in the post‑rewrite (e.g. OID‑sorted
// MHJ output) schema.
//
// Also refreshes j.schema from the current Left/Right outputs so that
// outer Joins (whose Left is this Join) see a current layout when
// they themselves rebind. Without this refresh, the cached schema
// from buildJoinFromDP keeps the pre‑rewrite layout and outer Joins
// rebind to stale positions.
//
// When a name is ambiguous (appears in multiple positions, e.g.
// self‑joins), the original index is preserved for that ref.
// reresolveNLIKeysByName re-resolves a NestedLoopIndexJoin's probe keys
// (Inner.Key / Inner.Keys) by Name+SourceTableIdx against its outer
// Output() schema, and refreshes the NLI's own schema to outer ++ inner.
// Cost-model doc 13 Phase 2: the probe keys were bound at tryBuildNLI
// time to the build-time outer schema, but a later pass reorders that
// schema (reresolveJoinByName rebuilds a child *Join's merged schema),
// leaving the keys pinned to a stale slot that reads the wrong runtime
// column (TPC-H Q9: l_suppkey probe reads l_linenumber → 0 rows).
func reresolveNLIKeysByName(nli *NestedLoopIndexJoin) {
	if nli == nil || nli.Inner == nil || nli.Outer == nil {
		return
	}
	outerSchema := nli.Outer.Output()
	rebind := func(e Expr) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		idx := -1
		if cr.SourceTableIdx != 0 {
			idx = findColumnIndexByNameAndSource(outerSchema, cr.Name, cr.SourceTableIdx, 0)
		}
		if idx < 0 {
			idx = findUniqueColumnIndex(outerSchema, cr.Name, 0)
		}
		if idx >= 0 {
			cr.Index = idx
		}
	}
	if _, innerKey, innerKeys, ok := nliInnerProbe(nli.Inner); ok {
		if innerKey != nil {
			rebind(innerKey)
		}
		for _, k := range innerKeys {
			rebind(k)
		}
	}
	if nli.Type != JoinTypeSemi && nli.Type != JoinTypeAnti {
		innerSchema := nli.Inner.Output()
		merged := make(Schema, len(outerSchema)+len(innerSchema))
		copy(merged, outerSchema)
		copy(merged[len(outerSchema):], innerSchema)
		nli.schema = merged
	} else {
		nli.schema = append(Schema(nil), outerSchema...)
	}
}

// reconcileNLILayout is a FINAL bottom-up pass (doc 13 Phase 2) that runs
// after all planning — including sub-query integration, the point where a
// derived-table outer's schema is reordered relative to the build-time
// schema the NLI keys were bound to. For each *Join it refreshes the
// merged schema + re-resolves keys by name (reresolveJoinByName); for each
// *NestedLoopIndexJoin it re-resolves the probe keys + refreshes the NLI
// schema (reresolveNLIKeysByName). Bottom-up so a child NLI's schema is
// truthful before its parent binds against it. It ran from Plan() gated on
// costDrivenJoinOrder until M0127-P6.3 removed that call site with the flag
// (the two in-place reorders it repaired are deleted); it STAYS as the
// oracle assertSearchedTreeNeedsNoReconcile checks searched plans against —
// 08 §3 retires it only once a searched plan is proven never to need it.
func reconcileNLILayout(node Node) {
	// M0127-P5.5-f-ii-a: never reconcile a searched subtree. This pass exists
	// because the integer DP and the MHJ packer reorder a tree in place and
	// leave stale indices behind; the search leaves none, so every rebind it
	// would perform is at best a no-op re-derivation of the layout, by a weaker
	// mechanism (names) than the one that produced it (coordinates).
	// `assertSearchedTreeNeedsNoReconcile` (searchedtree.go) is what turns
	// "at best a no-op" from an assumption into a per-plan check at the
	// boundary. searchedtree.go also records why this must not reach the
	// boundary Project, whose target list is the map rather than a reference.
	if isSearchedTree(node) {
		return
	}
	reconcileNLILayoutBody(node)
}

// reconcileNLILayoutBody is `reconcileNLILayout` without the searched-subtree
// guard, so the assertion in searchedtree.go can run the real pass over a tree
// the guard would otherwise skip. Its recursive calls go back through the
// guarded entry point, so a searched subtree nested inside a non-searched one is
// still skipped.
func reconcileNLILayoutBody(node Node) {
	switch n := node.(type) {
	case *Join:
		reconcileNLILayout(n.Left)
		if n.Type != JoinTypeSemi && n.Type != JoinTypeAnti {
			reconcileNLILayout(n.Right)
		}
		reresolveJoinByName(n)
	case *NestedLoopIndexJoin:
		reconcileNLILayout(n.Outer)
		reresolveNLIKeysByName(n)
	case *Filter:
		reconcileNLILayout(n.Child)
		if !n.LeafLocal {
			reresolveExprByName(n.Predicate, n.Child.Output())
		}
	case *Project:
		reconcileNLILayout(n.Child)
		if !n.IsolatedScope {
			cs := n.Child.Output()
			for i := range n.Targets {
				reresolveExprByName(n.Targets[i], cs)
			}
		}
	case *Aggregate:
		reconcileNLILayout(n.Child)
		cs := n.Child.Output()
		for i := range n.GroupExprs {
			reresolveExprByName(n.GroupExprs[i], cs)
		}
		for i := range n.Passthrough {
			reresolveExprByName(n.Passthrough[i], cs)
		}
		for i := range n.Aggs {
			reresolveExprByName(n.Aggs[i].Arg, cs)
			reresolveExprByName(n.Aggs[i].Arg2, cs)
			for j := range n.Aggs[i].ExtraArgs {
				reresolveExprByName(n.Aggs[i].ExtraArgs[j], cs)
			}
		}
	case *WindowAgg:
		reconcileNLILayout(n.Child)
	case *Sort:
		reconcileNLILayout(n.Child)
		cs := n.Child.Output()
		for i := range n.Keys {
			reresolveExprByName(n.Keys[i].Expr, cs)
		}
	case *Limit:
		reconcileNLILayout(n.Child)
	}
}

// reresolveExprByName re-resolves every plain ColumnRef in e by
// Name+SourceTableIdx against childSchema (offset 0). visitColumnRefs
// does not descend into sub-query scopes or *OuterColumnRef, so only
// same-scope refs are touched. Ambiguous names (self-join without a
// source disambiguator) resolve to -1 and are left unchanged.
func reresolveExprByName(e Expr, childSchema Schema) {
	if e == nil {
		return
	}
	visitColumnRefs(e, func(x Expr) {
		cr, ok := x.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		idx := -1
		if cr.SourceTableIdx != 0 {
			idx = findColumnIndexByNameAndSource(childSchema, cr.Name, cr.SourceTableIdx, 0)
		}
		if idx < 0 {
			idx = findUniqueColumnIndex(childSchema, cr.Name, 0)
		}
		if idx >= 0 {
			cr.Index = idx
		}
	})
}

func reresolveJoinByName(j *Join) {
	if j == nil {
		return
	}
	leftSchema := j.Left.Output()
	rightSchema := j.Right.Output()
	leftWidth := len(leftSchema)
	// Refresh cached merged schema. Semi/Anti joins (M0061-0001
	// EXISTS / NOT-EXISTS unnest) emit Outer (= Left) only at
	// runtime, so their cached schema must NOT widen to merged
	// even though the predicate evaluates against the padded
	// (Left ++ Right) row. Without this guard, downstream
	// outer-Joins observe a 15-col layout for what runtime
	// produces as 11 cols, and predRebind picks Left positions
	// for refs that should land in Right (Q21's NOT-EXISTS
	// `l3.l_suppkey <> l1.l_suppkey` collapsed onto l2's
	// l_suppkey leaked into the SemiJoin's stale merged schema —
	// silent FN, 0 rows vs canonical ~411).
	if j.Type != JoinTypeSemi && j.Type != JoinTypeAnti {
		merged := make(Schema, leftWidth+len(rightSchema))
		copy(merged, leftSchema)
		copy(merged[leftWidth:], rightSchema)
		j.schema = merged
	}

	// resolveSide tries SourceTableIdx-aware lookup first when
	// the ColumnRef carries a known source identity (M0071-0009);
	// falls back to Name-only when source identity is unknown.
	// Returns (-1, false) on miss and (-1, true) when the name is
	// present but ambiguous — a distinction only `predRebind` acts
	// on (M0127-P5.9-i).
	//
	// An ambiguous (Name, SourceTableIdx) does NOT fall back to the
	// Name-only lookup: dropping the disambiguator can only match
	// the same columns or more of them, so the answer would be
	// ambiguous again.
	resolveSide := func(schema Schema, cr *ColumnRef, offset int) (int, bool) {
		if cr.SourceTableIdx != 0 {
			if newIdx, ambiguous := lookupColumnIndexByNameAndSource(schema, cr.Name, cr.SourceTableIdx, offset); newIdx >= 0 || ambiguous {
				return newIdx, ambiguous
			}
		}
		return lookupColumnIndexByName(schema, cr.Name, offset)
	}

	// rebind resolves a join key against the side it is already known
	// to belong to, so an ambiguous name is simply left alone — there
	// is no other side to be wrongly tempted by.
	rebind := func(e Expr, leftSide bool) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		var newIdx int
		if leftSide {
			newIdx, _ = resolveSide(leftSchema, cr, 0)
		} else {
			newIdx, _ = resolveSide(rightSchema, cr, leftWidth)
		}
		if newIdx >= 0 {
			cr.Index = newIdx
		}
	}
	// predRebind resolves a ColumnRef in the Predicate by NAME. It
	// tries the side suggested by the original Index first (so
	// `a INNER JOIN b ON a.id = b.id` keeps a.id on the left and
	// b.id on the right when names collide), but falls back to the
	// other side if the Name isn't found there. This covers
	// pushOneConjunct's residuals: when a conjunct from a higher
	// scope is ANDed onto a Join's Predicate, its ColumnRef indices
	// may already have been remapped by an earlier pass — so the
	// original-Index side classification can be wrong, and we need
	// to retry the opposite side by Name.
	//
	// M0071-0009: when the ColumnRef carries SourceTableIdx
	// (set by the binder from the rangeBinding's source identity),
	// resolveSide prefers the (Name, SourceTableIdx) match — Q21's
	// 3 lineitem aliases all named `l_suppkey` are no longer
	// "ambiguous"; each disambiguates by its source.
	//
	// M0127-P5.9-i: the fallback is for a MISS, never for an
	// AMBIGUITY. A miss says "this name is not on this side", which
	// is real evidence the side classification was wrong; an
	// ambiguity says "this name is on this side more than once",
	// which is evidence of nothing except that the resolver cannot
	// finish. Crossing over on the second is how a correctly-bound
	// reference to one of three repeated CTE scans (TPC-DS Q83's
	// `item_id`) got rebound onto a different scan's column of the
	// same name — a predicate comparing a column to itself, hence a
	// cross product, and under GOOPG_PGSHAPED_DP a plan-time abort in
	// `assertSearchedTreeNeedsNoReconcile`. Abstaining leaves the
	// index the coordinate arithmetic bound, which is the answer.
	predRebind := func(e Expr) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		type side struct {
			schema Schema
			offset int
		}
		order := [2]side{{leftSchema, 0}, {rightSchema, leftWidth}}
		if cr.Index >= leftWidth {
			order[0], order[1] = order[1], order[0]
		}
		for _, s := range order {
			newIdx, ambiguous := resolveSide(s.schema, cr, s.offset)
			if ambiguous {
				return
			}
			if newIdx >= 0 {
				cr.Index = newIdx
				return
			}
		}
	}
	rebind(j.LeftKey, true)
	rebind(j.RightKey, false)
	visitColumnRefs(j.Predicate, predRebind)
}

// visitColumnRefs invokes fn on every same-scope *ColumnRef in e.
//
// M0125-0002 commit 3: built on walkExprRefs / exprChildSlots instead
// of its own 7-of-32 type switch. Child structure comes from the
// primitive, so a ColumnRef under IS NULL, a cast, a row constructor,
// an IN-list element or a subquery node's PARAM_EXEC Args — all
// silently skipped by the old arms — is now visited, and every rebind
// call site (reresolveExprByName, reresolveJoinByName's predRebind,
// nl_index_join.go's leftover rebind) re-resolves it instead of leaving
// its pre-rewrite Index behind (RC-1a).
//
// Scope policy: scopeIgnore. All three call sites rebind SAME-SCOPE
// indices; an inner plan's ColumnRefs live in the subplan's own
// coordinate space and an *OuterColumnRef names a scope above this
// one, so neither is handed to fn (both were the old walker's
// documented declines, preserved — see visit_refs_arms_test.go). A
// subquery node's Args are same-scope slots (evaluated against the
// current outer row) and ARE visited.
//
// An unenumerated type panics, matching PG's
// expression_tree_walker_impl (nodeFuncs.c:2667); a silent skip is the
// RC-1a defect this conversion exists to remove.
func visitColumnRefs(e Expr, fn func(Expr)) {
	walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(x Expr) bool {
			if cr, ok := x.(*ColumnRef); ok {
				fn(cr)
			}
			return true
		},
		OnUnknown: func(x Expr) {
			panic(fmt.Sprintf("visitColumnRefs: unrecognized expression type %T — teach "+
				"exprChildSlots (exprwalk.go) about it; a silent skip leaves a stale "+
				"column index behind every rebind site", x))
		},
	})
}
