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
// is deliberately HELD BACK until the 03 §10 boundary map is proven in
// production. They are today's boundary translation, the pinned-spine
// re-resolution (predp.go) consumes them, and 08 §4 names deleting them before
// the replacement is validated as the S7 change most likely to regress. The
// standing deferral pointer in IMPLEMENTATION-TODO carries the resume point.
//
// `reconcileNLILayout` stays for the same reason, one level weaker: 08 §3 says
// it keeps running until a searched plan is shown never to need it, and the
// assertion that proves that has not been taken yet.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// scanKey uniquely identifies a scan by its catalog table pointer and
// FROM-clause alias. For self-joins (e.g. `nation n1, nation n2`) the alias
// distinguishes the two instances; for ordinary tables the alias is empty and
// the table pointer alone is sufficient. `buildBindingsPosMap` keys its
// pre/post-rewrite leaf correspondence on it.
type scanKey struct {
	table *catalog.Table
	alias string
}

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

// remapExprRefsToMHJ walks the plan tree and remaps ColumnRef
// indices.  It first looks for a MultiHashJoin and uses its
// table list to build a FROM‑order → output‑order position map.
// If no MHJ is found, it falls back to building a posMap from
// the SeqScan leaves of a binary join tree.
func remapColumnRefsAfterRewrite(node Node) Node {
	remapPosMapAfterRewrite(node, nil)
	return node
}

func remapPosMapAfterRewrite(node Node, posMap func(int) int) {
	if node == nil {
		return
	}
	// walkSubqueryPlans walks an expression tree and recursively
	// calls remapPosMapAfterRewrite on any SubqueryExpr.Plan or
	// InExpr.Plan found within. This handles subquery inner plans
	// that need their own independent remap pass after the outer
	// plan tree has been rewritten (e.g. MHJ or bushy DP).
	var walkSubqueryPlans func(Expr)
	walkSubqueryPlans = func(e Expr) {
		if e == nil {
			return
		}
		switch x := e.(type) {
		case *SubqueryExpr:
			remapPosMapAfterRewrite(x.Plan, nil)
		case *MultiAssignSubqRow:
			remapPosMapAfterRewrite(x.Plan, nil)
		case *InExpr:
			if x.Plan != nil {
				remapPosMapAfterRewrite(x.Plan, nil)
			}
		case *BinaryOp:
			walkSubqueryPlans(x.Left)
			walkSubqueryPlans(x.Right)
		case *UnaryOp:
			walkSubqueryPlans(x.Operand)
		case *FuncCall:
			for _, a := range x.Args {
				walkSubqueryPlans(a)
			}
		case *CaseExpr:
			if x.Operand != nil {
				walkSubqueryPlans(x.Operand)
			}
			for _, w := range x.Whens {
				walkSubqueryPlans(w.When)
				walkSubqueryPlans(w.Then)
			}
			if x.Else != nil {
				walkSubqueryPlans(x.Else)
			}
		case *ExtractExpr:
			walkSubqueryPlans(x.Source)
		}
	}
	subRemap := func(exprs []Expr) {
		for _, e := range exprs {
			walkSubqueryPlans(e)
		}
	}

	switch n := node.(type) {
	case *Join:
		remapPosMapAfterRewrite(n.Left, nil)
		// M0062-0005: Semi / Anti joins carry an isolated subquery
		// scope on their Right (the cloned EXISTS inner plan). Do
		// not descend with the outer scope's posMap — the inner
		// plan was already independently optimised by the
		// recursive `unnestSubqueriesInPlan` call inside
		// `unnestExistsExpr`, and its ColumnRefs use inner-scope
		// indices that must not be remapped against outer
		// bindings.
		if n.Type != JoinTypeSemi && n.Type != JoinTypeAnti {
			remapPosMapAfterRewrite(n.Right, nil)
		}
		subRemap([]Expr{n.Predicate, n.LeftKey, n.RightKey})
		return
	case *Filter:
		// M0077-0001: Filter wrappers attached above leaf scans
		// by Slice A carry leaf-local Predicate ColumnRefs (NOT
		// FROM-cumulative). Skip the cumulative-space posMap.
		if n.LeafLocal {
			return
		}
		remapPosMapAfterRewrite(n.Child, nil)
		// The OID-keyed `mhjPosMapOf` that used to run here returned nil
		// unconditionally (its own comment: OID order is not FROM order, and
		// it collapsed self-join duplicates) and M0127-P6.2 deleted it with
		// the rest of the MHJ family. `remapWithBindings`' bindings-keyed
		// posMap is and was the pass that actually remaps this arm.
		subRemap([]Expr{n.Predicate})
		return
	case *Project:
		// M0063-0001: skip isolated-scope Projects (view rename wrapper).
		if n.IsolatedScope {
			return
		}
		remapPosMapAfterRewrite(n.Child, nil)
		subRemap(n.Targets)
		return
	case *Sort:
		remapPosMapAfterRewrite(n.Child, nil)
		for i := range n.Keys {
			subRemap([]Expr{n.Keys[i].Expr})
		}
		return
	case *Aggregate:
		remapPosMapAfterRewrite(n.Child, nil)
		subRemap(n.GroupExprs)
		for i := range n.Aggs {
			if n.Aggs[i].Arg != nil {
				subRemap([]Expr{n.Aggs[i].Arg})
			}
			if n.Aggs[i].Arg2 != nil {
				subRemap([]Expr{n.Aggs[i].Arg2})
			}
		}
		return
	}
}

// `binaryTreePosMapOf` (dead — nothing had called it for several milestones)
// and `remapExprRefsToMHJ` (a one-line alias for `remapColumnRefsAfterRewrite`,
// kept only because callers predated the rename) were deleted by M0127-P6.2
// along with the `buildMHJPosMap` they fed.

// remapWithBindings applies a bindings‑based position remap to the
// join‑tree portion of node (everything below any Aggregate).  It
// maps FROM‑clause column offsets (as stored in bindings[i].offset)
// to the actual scan offsets in the current plan output.  Self‑join
// tables (e.g. `nation n1, nation n2`) are disambiguated via the
// (table pointer, alias) scanKey.
func remapWithBindings(node Node, bindings []rangeBinding) {
	if node == nil || len(bindings) == 0 {
		return
	}
	posMap := buildBindingsPosMap(node, bindings)
	if posMap == nil {
		return
	}
	applyJoinTreePosMap(node, posMap)
}

// remapTopProjection applies a bindings‑based posMap to ColumnRefs
// in the top Project's Targets and any Sort keys above the join
// tree, stopping as soon as a Filter / Aggregate / Join / MHJ is
// reached (those were already remapped by the earlier
// remapWithBindings pass — walking into them would double‑remap).
//
// Used for inline‑view subqueries (TPC‑H Q7/Q8/Q9), whose recursive
// planSelect resolves Project targets against FROM‑clause bindings
// after the join tree was rewritten — so the targets carry stale
// FROM‑order indices that the join‑tree remap already corrected
// elsewhere.
func remapTopProjection(out Node, bindings []rangeBinding) {
	if out == nil || len(bindings) == 0 {
		return
	}
	// Find the join‑tree subtree (the Filter / Join / MHJ node)
	// to derive the posMap from. Walk down past Project / Sort /
	// Limit / LockRows wrappers until we hit it.
	root := out
	for {
		// M0127-P5.9-c (08 §3): this descent is the one place the
		// searched-subtree opacity could be walked THROUGH rather than
		// stopped at, and it was. The boundary is a `*Project`
		// (createplanroot.go) and an elided boundary over a sorted root
		// is a `*Sort`, so both arms below step over the search root and
		// hand `buildBindingsPosMap` a node INSIDE it. `collect`'s own
		// guard (bushy.go:2563) then never fires — it is asked about the
		// searched join, not about the searched root — and the map that
		// comes back is the search's binding→plan-position permutation.
		//
		// Applied to the wrappers, that map is a second permutation on
		// top of the one the boundary already performed: the enclosing
		// Project's targets are written in binding coordinates and the
		// boundary republishes binding order, so the correct action here
		// is NOTHING. Measured on `select * from customer, orders where
		// o_custkey = c_custkey and o_orderkey = 1` (P5.9 run 1's
		// reproducer): every column's value came back one relation-block
		// away from its name, and the boundary Project's OWN target list
		// — which is the coordinate map, not a reference into it — was
		// rewritten along with the targets above it.
		//
		// Stopping here rather than teaching `collect` is the correct
		// half: `collect` is already right, and what was wrong is asking
		// it a question about the inside of an opaque subtree.
		if isSearchedTree(root) {
			return
		}
		switch n := root.(type) {
		case *Project:
			root = n.Child
			continue
		case *Sort:
			root = n.Child
			continue
		case *Limit:
			root = n.Child
			continue
		case *LockRows:
			root = n.Child
			continue
		}
		break
	}
	posMap := buildBindingsPosMap(root, bindings)
	if posMap == nil {
		return
	}
	// Now walk the wrappers and remap only their direct
	// expressions.
	n := out
	for n != nil {
		switch x := n.(type) {
		case *Project:
			for i := range x.Targets {
				remapByPosMap(&x.Targets[i], posMap)
			}
			n = x.Child
		case *Sort:
			for i := range x.Keys {
				remapByPosMap(&x.Keys[i].Expr, posMap)
			}
			n = x.Child
		case *Limit:
			n = x.Child
		case *LockRows:
			n = x.Child
		default:
			return
		}
	}
}

// remapAggExprsWithBindings remaps the GroupExprs and Agg.Arg
// expressions of the Aggregate node that is at or directly below node
// (unwrapping at most one Filter wrapper for the HAVING clause).
// The posMap is built from the Aggregate's child (the join tree), so
// it maps FROM‑clause offsets to scan offsets without touching the
// HAVING‑filter predicate, which already uses aggregate‑output
// indices and must not be remapped.
func remapAggExprsWithBindings(node Node, bindings []rangeBinding) {
	if node == nil || len(bindings) == 0 {
		return
	}
	// Unwrap at most one HAVING Filter to find the Aggregate.
	var aggNode *Aggregate
	switch n := node.(type) {
	case *Aggregate:
		aggNode = n
	case *Filter:
		if ag, ok := n.Child.(*Aggregate); ok {
			aggNode = ag
		}
	}
	if aggNode == nil {
		return
	}
	posMap := buildBindingsPosMap(aggNode.Child, bindings)
	if posMap == nil {
		return
	}
	for i := range aggNode.GroupExprs {
		remapByPosMap(&aggNode.GroupExprs[i], posMap)
	}
	for i := range aggNode.Aggs {
		if aggNode.Aggs[i].Arg != nil {
			remapByPosMap(&aggNode.Aggs[i].Arg, posMap)
		}
		if aggNode.Aggs[i].Arg2 != nil {
			remapByPosMap(&aggNode.Aggs[i].Arg2, posMap)
		}
	}
}

// `mhjPosMapOf` was a position map keyed by table OID, meant to remap
// FROM-order ColumnRef indices into the MHJ's OID-sorted output order. It was
// already a permanent `return nil` before M0127-P6.2 deleted it: OID order is
// FROM order only when the FROM list happens to be in table-creation order
// (false for most TPC-H queries), and it collapsed duplicate OIDs, so self
// joins like Q7's `nation n1, nation n2` mapped both scans onto one entry.
// `buildBindingsPosMap` (via `remapWithBindings`) is the posMap that handles
// both cases, because it reads the real FROM order off `rangeBinding.offset`
// and disambiguates self-joins with `scanKey{table, alias}`.

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

// `buildMHJPosMap` — old (FROM-order binary tree) → new (MHJ DFS-order)
// column positions, keyed by table OID — was deleted with the node by
// M0127-P6.2. `buildBindingsPosMap` below is the surviving position map.

// buildBindingsPosMap collects all SeqScan leaves from node (in DFS
// order) and builds a position map from FROM‑clause offsets (as
// recorded in bindings) to actual plan‑output offsets.  The map uses
// (table pointer, alias) pairs so self‑joins like `nation n1, nation
// n2` are disambiguated even when both have the same catalog OID.
//
// Returns nil when no scans can be found (e.g. the node is an opaque
// derived‑table output whose inner scan nodes are already resolved).
func buildBindingsPosMap(node Node, bindings []rangeBinding) func(int) int {
	type scanEntry struct {
		key scanKey
		off int
	}
	var entries []scanEntry
	var off int
	// declined is set by collect's default arm when it meets a node kind
	// it cannot classify; see the comment there. Once set, the whole
	// remap is abandoned rather than applied with a wrong offset.
	var declined bool
	var collect func(Node)
	collect = func(n Node) {
		if n == nil {
			return
		}
		// M0127-P5.5-f-ii-a: a subtree the PG-shaped search built already
		// publishes its columns at the positions the bindings put them —
		// createplanroot.go's boundary is what guarantees it. Treat it as an
		// opaque leaf, the same treatment the *Project / *CTEScan / SRF arms
		// below get: advance past its width so scans to its RIGHT keep correct
		// offsets, record NO scan entry, and let every binding inside it fall
		// through this function's returned closure unchanged — the identity,
		// which is the truth.
		//
		// When the boundary emitted a Project this arm is redundant with the
		// *Project arm below, which already stops there (M0125-0012). It is
		// here for the ELIDED case — a search whose order already was binding
		// order returns a bare *Join, which `collect` descends into. That
		// descent is numerically harmless (identity layout ⇒ identity map),
		// but it is what puts the searched joins in `applyJoinTreePosMap`'s
		// path, and that pass does more than arithmetic. See searchedtree.go.
		if isSearchedTree(n) {
			off += searchedTreeWidth(n)
			return
		}
		switch x := n.(type) {
		case *SeqScan:
			entries = append(entries, scanEntry{key: scanKey{table: x.Table, alias: x.Alias}, off: off})
			off += len(x.Output())
		case *IndexScan:
			// M0062-0002: preserve Alias so self-joins (Q8 `nation n1, nation n2`)
			// can disambiguate when one side flips to IndexScan.
			entries = append(entries, scanEntry{key: scanKey{table: x.Table, alias: x.Alias}, off: off})
			off += len(x.Output())
		// A `*MultiHashJoin` arm sat here until M0127-P6.2. Its lesson
		// outlives it and still governs the arms below: M0125-0013 found it
		// matching bare scans inline, so a table wrapped in a *Filter by a
		// pushed-down single-source conjunct recorded no scanEntry AND never
		// advanced `off` — every table to its right got an offset short by
		// the skipped width, while the skipped table's own columns kept their
		// FROM-cumulative index. TPC-DS Q47 returned s_county for d_year with
		// the row COUNT still correct, because only the top projection was
		// misremapped. That is why this walker recurses through `collect`
		// everywhere and declines the whole remap on anything it cannot
		// classify (the `default:` arm's RC-2 hardening).
		case *Join:
			collect(x.Left)
			collect(x.Right)
		case *NestedLoopIndexJoin:
			// M0062-0006: NLI sits between Filter and the join subtree
			// for Q9-shape plans. Without this case the collect walker
			// stops at NLI and `buildBindingsPosMap` returns an empty
			// scanMap, so `p_name`'s ColumnRef.Index is never
			// re-resolved against the rewritten output layout.
			collect(x.Outer)
			collect(x.Inner)
		case *Filter:
			collect(x.Child)
		case *Project:
			// Any Project in the join-tree subtree passed to collect()
			// is a subquery-derived table — its inner scans are in a
			// separate planning scope and must NOT contribute entries to
			// the outer scanMap (doing so would count their raw scan
			// widths instead of the projected output width, causing the
			// outer-scan offsets to be wrong).
			//
			// For IsolatedScope=true (M0063-0001 view-rename wrapper) this
			// was already the contract. Extend it to all Projects:
			// advance `off` by the projected output width and stop.
			off += len(x.Output())
		case *Sort:
			collect(x.Child)
		case *Aggregate:
			// M0127-P5.9-f (TPC-H Q17): opaque leaf, NOT a descent.
			// `applyJoinTreePosMap` has always stopped at *Aggregate
			// ("aggregate expressions are a different scope"), so the
			// entries this arm used to record were never applied inside
			// the aggregate's own subtree — they only leaked into
			// `scanMap` and mis-addressed the SAME table elsewhere in the
			// tree. Build and apply must stop at the same nodes; this is
			// the third instance of that rule (*Project M0125-0012,
			// *SetOp/*WindowAgg RC-2).
			//
			// The descent was also numerically wrong on its own terms: an
			// Aggregate's output is group keys + agg results, so
			// descending advanced `off` by the CHILD's width instead of
			// the aggregate's, leaving every node to its RIGHT short by
			// the difference — the identical defect *WindowAgg was moved
			// out of the descend set for.
			//
			// Q17 is where it became visible. `unnestSubquery` (unnest.go)
			// decorrelates `l_quantity < (select 0.2*avg(l_quantity) from
			// lineitem where l_partkey = p_partkey)` into a hash join whose
			// INNER side is a HashAggregate over a CLONE of lineitem — a
			// separate planning scope. With `GOOPG_PGSHAPED_DP` on, the
			// outer side is a searched subtree and so records no entries
			// (the arm above), which left that clone as the FIRST and only
			// `lineitem` entry, at offset 25. Every outer `lineitem`
			// binding was then remapped to `25 + col`, and the residual
			// `l_quantity/4` became `l_quantity/29` against a 27-wide
			// composed slot: "column ref l_quantity/29 out of VirtualSlot
			// range 27". Flag OFF hid it only by accident — the untagged
			// outer join recorded `lineitem` at offset 0 first, and
			// "first occurrence wins" (below) discarded the clone.
			//
			// With this arm opaque, Q17 collects no entries at all and the
			// remap declines — which is the truth: the search boundary
			// already publishes binding order, so there is nothing to
			// correct. See 09 §5.21.
			off += len(x.Output())
		case *Values:
			// Values node with non-empty schema (e.g. FROM (VALUES (r1), (r2)) AS t).
			// Advance off by the output width so sibling scans stay aligned.
			off += len(x.Output())
		case *CTEScan:
			// CTE Scan (WITH query) contributes its output columns to the
			// join-tree schema.  Advance off so sibling scans get the
			// correct scanMap offset; without this, aggregate arguments and
			// GROUP BY expressions referencing columns to the right of a
			// CTE are remapped to the wrong indices.  (M0097-0058)
			off += len(x.Output())
		case *MaterializedCTEScan:
			// DML CTE — same offset-advance requirement as CTEScan above.
			off += len(x.Output())
		case *FromUnnest, *GenerateSeries, *GenerateSubscripts,
			*UserSrfScan, *ScalarFuncScan, *PgPartitionTree, *PgOptionsToTable,
			*PgInputErrorInfo, *PgGetPublicationTables,
			*PgAvailableWalSummaries, *PgGetSequenceData, *VerifyHeapam,
			*PgGetCatalogForeignKeys:
			// FROM-clause set-returning / table functions are leaf
			// nodes that contribute output columns but carry no
			// scanKey to remap. Advance `off` by their output width
			// (mirroring the *Values case) so any scan to their RIGHT
			// gets the correct scanMap offset. Omitting these made
			// `off` too low for downstream scans, so remapTopProjection
			// shifted right-side projection columns down by the SRF's
			// width — e.g. pg_dump's getTableAttrs
			// `unnest('{oid}'::oid[]) src JOIN pg_attribute a` returned
			// a scrambled row (DU-002 slice 46, M0110-0001). Their own
			// columns need no remap: the posMap returns oldIdx unchanged
			// for bindings absent from scanMap.
			off += len(x.Output())

		// --- RC-2 (TPC-DS Q8): opaque leaves that were missing entirely.
		// Each of these contributes output columns to the join-tree
		// schema but carries no scanKey. Without an arm, `off` is not
		// advanced and EVERY scan to their right gets an offset that is
		// too low, so ColumnRef indices are remapped into another
		// table's columns. For a set operation inside a FROM subquery
		// that produced `index out of range [57] with length 1` in
		// MaterializedSlot.Get, via the hash-join build-side drain that
		// gatherOp.Open runs in the leader (see
		// docs/design/tpcds-round2-fixes/README.md §4).
		//
		// WindowAgg belongs here, NOT in the descend set: it APPENDS
		// window-function columns to its child's output, so descending
		// would leave right-hand scans short by exactly that many
		// columns — the identical defect SetOp has today.
		case *SetOp, *RecursiveUnion, *WorkTableScan, *WindowAgg,
			*ProjectSet, *OrdinalityWrap, *RowsFrom, *IndexOnlyScan:
			off += len(x.Output())

		// --- Pass-through nodes: schema is exactly the child's, so
		// descend without advancing.
		case *Distinct:
			collect(x.Child)
		case *DistinctOn:
			collect(x.Child)
		case *Limit:
			collect(x.Child)
		case *LockRows:
			collect(x.Child)
		case *Memoize:
			collect(x.Child)

		default:
			// RC-2: an unhandled node used to fall through silently,
			// leaving `off` un-advanced — a wrong answer or an
			// out-of-range panic, unconditionally. Declining the whole
			// remap instead is the safe direction: an unremapped tree is
			// only wrong when a reorder actually happened, whereas a
			// mis-advanced offset is always wrong. All three callers
			// (remapWithBindings, remapTopProjection,
			// remapAggExprsWithBindings) already nil-check the result.
			declined = true
		}
	}
	collect(node)
	if declined || len(entries) == 0 {
		return nil
	}

	// Build scanMap: only the FIRST occurrence of each (table,alias)
	// is stored so that later duplicate aliases don't clobber it.
	scanMap := make(map[scanKey]int, len(entries))
	for _, e := range entries {
		if _, exists := scanMap[e.key]; !exists {
			scanMap[e.key] = e.off
		}
	}

	return func(oldIdx int) int {
		for i := range bindings {
			b := &bindings[i]
			if b.table == nil {
				continue
			}
			w := len(b.table.Columns)
			if oldIdx >= b.offset && oldIdx < b.offset+w {
				k := scanKey{table: b.table, alias: b.alias}
				if scanOff, ok := scanMap[k]; ok {
					return scanOff + (oldIdx - b.offset)
				}
				return oldIdx
			}
		}
		return oldIdx
	}
}

// applyJoinTreePosMap walks the join‑tree portion of a plan (below
// any Aggregate) and applies posMap to all ColumnRefs in Filter
// predicates, Join predicates, Sort keys, and Project targets.
// It stops at Aggregate nodes — those are handled separately by
// remapAggExprsWithBindings so that post‑aggregate ColumnRefs (which
// reference aggregate output columns, not scan columns) are never
// inadvertently remapped.
func applyJoinTreePosMap(node Node, posMap func(int) int) {
	if node == nil {
		return
	}
	// M0127-P5.5-f-ii-a: stop at a searched subtree, for the same reason the
	// *Project arm below stops at a Project — build and apply must stop at the
	// same nodes (`collect` now stops here too). The searched tree's quals were
	// translated onto their own merged row by the `createPlan` arm that built
	// it, so there is no correction to make; and this arm does not only apply
	// posMap, it calls `reresolveJoinByName`, which would rebind those quals by
	// NAME over a layout that was just derived by coordinate. searchedtree.go.
	if isSearchedTree(node) {
		return
	}
	switch n := node.(type) {
	case *Join:
		applyJoinTreePosMap(n.Left, posMap)
		// M0062-0005: Semi/Anti joins' Right side is the cloned
		// EXISTS inner plan — an isolated subquery scope whose
		// ColumnRefs use inner-scope indices and must NOT be
		// remapped by the outer FROM-bindings posMap. (The same
		// rule applies in `remapPosMapAfterRewrite`.)
		if n.Type != JoinTypeSemi && n.Type != JoinTypeAnti {
			applyJoinTreePosMap(n.Right, posMap)
		}
		// Re‑resolve Join keys/predicate by NAME against the
		// post‑rewrite child output schemas. The bushy DP produces
		// subset‑FROM‑order indices, and a later pass may reorder the
		// inner subtree in place, invalidating them. (Until M0127-P6.2
		// the reordering pass was `rewriteMultiWayChain` OID-sorting the
		// MHJ; the by-name re-resolution is what made this arm robust to
		// it, and stays robust to the passes that remain.)
		// Looking up by ColumnRef.Name in the
		// freshly‑exposed schemas is robust to any in‑place
		// reordering — column names are unique per table
		// (TPC‑H prefixes p_, s_, l_, …). Self‑joins use SeqScan
		// alias‑aware schemas; ambiguous matches keep the original
		// index untouched.
		reresolveJoinByName(n)
	case *NestedLoopIndexJoin:
		// M0065-0001: walk into Outer (so deeper Joins get their
		// keys reresolved). Don't touch NLI's own keys here —
		// posMap remap and Name re-resolve both empirically break
		// Q9's chained-NLI shape (where the existing keys already
		// align with the runtime row layout). Q21's mismatching
		// Anti-NLI keys are a separate problem tracked under
		// M0065-Q21-walker; the safe thing is to leave NLI keys
		// alone in this walker.
		applyJoinTreePosMap(n.Outer, posMap)
	case *Filter:
		// M0077-0001: Slice A leaf-local Filter wrappers carry
		// leaf-scoped ColumnRefs; skip both the recursion (Child
		// is a SeqScan; nothing to remap there) and the predicate
		// remap (would corrupt local indices).
		if n.LeafLocal {
			return
		}
		applyJoinTreePosMap(n.Child, posMap)
		remapByPosMap(&n.Predicate, posMap)
	case *Project:
		// M0125-0012 (TPC-DS Q8): EVERY Project in the join tree is a
		// separate planning scope, not just the M0063-0001
		// SubqueryAlias-style (`IsolatedScope`) view-rename wrapper.
		// `posMap` is only defined over the coordinate space that
		// `buildBindingsPosMap`'s `collect` walked, and `collect`'s
		// own *Project arm stops at ANY Project ("Extend it to all
		// Projects: advance `off` by the projected output width and
		// stop"). Descending here therefore fed posMap indices it
		// never had a domain for: a FROM-subquery's own target
		// `ca_zip/0`, correct against its 1-column SetOp child, fell
		// inside the OUTER binding that happens to start at 0
		// (`store_sales`) and was rewritten to that table's
		// MHJ-reordered offset — 57 at SF=1, 6 in the minimal shape.
		// Execution then read index 57 out of the SetOp's 1-wide
		// MaterializedSlot ("column ref ca_zip/57 out of
		// MaterializedSlot range 1").
		//
		// Note this is the *build* half's mirror image: `9740fce9`
		// gave `collect` its opaque-leaf arms so `off` advances past a
		// SetOp, but left this applier free to walk into the scope
		// above it. Build and apply must stop at the same nodes or
		// the map is applied outside its domain (CLAUDE.md "sibling
		// paths must change together").
		//
		// Nothing is lost by stopping: the subquery's inner plan was
		// already normalised into its own coordinate space by
		// `remapSubqueryColumnRefs` (planner.go, M0097-0058) when the
		// derived table was planned, and Projects ABOVE the join tree
		// are remapped by the separate `remapTopProjection` pass.
		return
	case *Sort:
		applyJoinTreePosMap(n.Child, posMap)
		for i := range n.Keys {
			remapByPosMap(&n.Keys[i].Expr, posMap)
		}
	case *Aggregate:
		return // stop — aggregate expressions are a different scope
	}
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
	if nli.Inner.Key != nil {
		rebind(nli.Inner.Key)
	}
	for _, k := range nli.Inner.Keys {
		rebind(k)
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
