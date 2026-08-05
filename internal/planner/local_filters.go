package planner

import "fmt"

// M0077-0001 (Slice A): relation-local predicate
// partition + attachment.
//
// This file is the planner-side first slice of the
// 4-slice Q5 fix from `docs/design/fix-for-q5/`.
// The slice is intentionally narrow:
//
//  1. Partition WHERE conjuncts into "join-side"
//     (multi-binding) vs "relation-local" (one-binding)
//     buckets BEFORE the bushy DP runs.
//  2. After the bushy DP picks a binary tree, attach
//     each binding's local predicates to its leaf scan
//     as a `Filter(SeqScan)` wrapper.
//  3. Keep the existing `MultiHashJoin` skip-on-
//     filtered-leaf behaviour — promote it to an
//     explicit contract (Slice A).
//
// Slice B refines `shouldAttachBeforeMHJ` with reliable-
// selectivity from the M0077-0002 `baseRelInfo` work;
// Slice C+D continue with cost-model + anchored
// equality synthesis. See
// `docs/design/fix-for-q5/01-target-shape-and-local-filtering.md`.

// relationLocalFilters carries the per-binding local
// predicates extracted by partitionConjunctsForJoinPlanning.
// Indexed by binding position in the FROM clause (NOT
// scan-output offset).
type relationLocalFilters struct {
	byBinding map[int][]Expr
}

// partitionConjunctsForJoinPlanning splits a flat
// conjunct list into two sets:
//
//  1. joinConjuncts — multi-binding predicates plus
//     any conjunct containing OuterColumnRef /
//     SubqueryExpr / ExistsExpr / InExpr-with-Plan
//     (per design 01 §3.1). These flow into the bushy
//     DP / Filter residual path unchanged.
//  2. locals — one-binding predicates keyed by binding
//     index. These get attached to the corresponding
//     leaf scan via attachRelationLocalFilters AFTER
//     the DP picks a join tree.
//
// `cumOffsets` maps the FROM-cumulative output column
// offsets — index `i` maps to bindings[i]'s first
// output-column index; index `len(bindings)` is the
// total schema width. This matches the DP's existing
// `tableForCol` contract at bushy.go:285.
//
// (M0077-0001.)
func partitionConjunctsForJoinPlanning(
	conjuncts []Expr,
	cumOffsets []int,
) (joinConjuncts []Expr, locals relationLocalFilters) {
	locals = relationLocalFilters{byBinding: make(map[int][]Expr)}
	for _, c := range conjuncts {
		// Conjuncts with subquery / outer-ref content can never be
		// safely pushed below a join boundary at this layer; the
		// existing planner stages own that work. Keep them in the
		// join-residual set.
		if !conjunctIsLocalEligible(c) {
			joinConjuncts = append(joinConjuncts, c)
			continue
		}
		bidx := tableForCol(c, cumOffsets)
		if bidx < 0 {
			// Multi-binding (or ColumnRef-out-of-range — rare but
			// possible during error recovery). Treat as join-side.
			joinConjuncts = append(joinConjuncts, c)
			continue
		}
		locals.byBinding[bidx] = append(locals.byBinding[bidx], c)
	}
	return joinConjuncts, locals
}

// conjunctIsLocalEligible reports whether the expression
// is structurally safe to attach as a `Filter(leaf)`
// wrapper inside the bushy DP's leaf set. Per design 01
// §3.1.3: any conjunct containing OuterColumnRef,
// SubqueryExpr, ExistsExpr, or InExpr with Plan != nil
// is INELIGIBLE — those nodes carry execution-time
// dependencies that the leaf attachment cannot honour.
//
// M0125-0002 commit 6 (with localizeExprToLeaf — producer and
// consumer land together): built on walkExprRefs / exprChildSlots
// instead of its own 9-of-32 type switch. The old switch had no
// default and only descended BinaryOp / UnaryOp / FuncCall /
// CaseExpr / ExtractExpr / InExpr, so a conjunct whose subquery or
// outer reference sat under any OTHER container — `(x IS NULL) =
// true`, `CAST(x AS int) > (subq)`, `ROW(x, (subq))`, `x IS
// DISTINCT FROM (subq)`, a COLLATE, an IS TRUE — produced zero
// callbacks below that container and returned a VACUOUS true. That
// is the fail-open direction: the conjunct was moved out of
// joinConjuncts into locals and pushed to a leaf, where
// localizeExprToLeaf (equally incomplete) left its ColumnRef
// indices in FROM-cumulative coordinates. Completing the pair
// therefore REMOVES predicates from the leaf-local set rather than
// adding any.
//
// Scope policy: scopeVeto. An inner plan is precisely the
// execution-time dependency §3.1.3 declines, so the abort IS the
// answer; an unenumerated type aborts too, which turns the old
// silent admission into a decline (fail closed). Declining costs an
// optimisation — the conjunct stays in the join residual and is
// evaluated above the join — never a wrong answer, so unlike
// commits 3/4 this walker must NOT panic on an unknown type.
//
// (M0077-0001.)
func conjunctIsLocalEligible(e Expr) bool {
	eligible := true
	ok := walkExprRefs(e, scopeVeto, exprVisitor{
		Visit: func(n Expr) bool {
			switch n.(type) {
			case *OuterColumnRef:
				// A childless leaf: exprChildSlots reports no slots
				// for it, so scopeVeto can never fire on its behalf
				// and the decline has to be explicit. Same shape as
				// commit 5's exprSide veto.
				eligible = false
				return false
			case *SubqueryExpr, *ExistsExpr, *ArraySubqueryExpr,
				*MultiAssignSubqRow, *MultiAssignSubqElem:
				// Declined whether or not Plan is set. exprChildSlots
				// emits the slotInnerPlan slot ONLY when Plan != nil,
				// so scopeVeto alone would admit an unplanned subquery
				// node — and the old switch declined SubqueryExpr /
				// ExistsExpr unconditionally, which is the behaviour
				// design 01 §3.1.3 states. The three Array/MultiAssign
				// kinds join them: they carry the same execution-time
				// dependency and were admitted by accident before.
				eligible = false
				return false
			}
			return true
		},
	})
	// ok == false means the walk ABORTED: either a slotInnerPlan child
	// under scopeVeto (an InExpr/SubqueryExpr/ExistsExpr that carries a
	// Plan) or a type exprChildSlots does not know. Both are declines —
	// the old switch had no default, so a conjunct built entirely from
	// unenumerated kinds returned a vacuous true and was pushed to a
	// leaf that localizeExprToLeaf could not rebase.
	return eligible && ok
}

// shouldAttachBeforeMHJ is the rollout gate per
// design 01 §4.1. Slice A's gate has TWO clauses:
//
//  1. fromCount ≥ 5 — only the shape that triggers
//     MultiHashJoin packing benefits from the binary-
//     preservation barrier; smaller queries are left
//     alone.
//  2. The FROM list contains at least one
//     `SmallDimension`-flagged table (region / nation
//     in TPC-H). Slice A's intended target — Q5 —
//     leans on filtered region as its anchor; Q7 / Q8 /
//     Q21 are 5+-table queries whose MHJ shape is
//     beneficial and where filtered leaves push the
//     planner away from MHJ packing into a slower
//     binary chain. Without the SmallDim guard, Slice A
//     regresses Q8 / Q21 from PASS to CANCEL.
//
// Slice B (M0077-0002) refines this with reliable-
// selectivity from `baseRelInfo`; Slice A keeps the
// guard cheap (table-flag check, no row-count math).
//
// (M0077-0001.)
// M0125-0043 added the `scans` parameter. The gate's second clause asks
// whether the FROM list contains a small-dimension relation, and that answer
// moved off `catalog.Table.SmallDimension` (a lookup of the literal names
// "region"/"nation") onto the leaf scan, where it is derived from the
// relation's size. `scans[i]` is the leaf built for `bindings[i]`; a binding
// with no scan falls back to the catalog hint, which is what the TPC-H unit
// fixtures set.
func shouldAttachBeforeMHJ(bindings []rangeBinding, scans []Node) bool {
	// Cost-driven order builds a binary Join tree (no MultiHashJoin), so a
	// single-table restriction that isn't routed to its leaf scan here
	// filters the full-table join output at the top instead — e.g. TPC-H
	// Q3 hash-joins all 6M lineitem rows then applies l_shipdate (121s vs
	// 17s once the filter sits on the scan). Partitioning the locals out
	// pre-DP also feeds the DP filtered base-rel cardinality, so it can
	// pick the order for the RESTRICTED sizes rather than the full tables.
	// Fire for every multi-table cost-driven plan. The NLI path is
	// unaffected: the ≥5-table shapes already reach here (Q8's part scan
	// is wrapped AND still becomes a NestedLoopIndexJoin), so a leaf
	// Filter does not block the probe. The production integer DP keeps the
	// original ≥5-table + small-dimension gate — its MultiHashJoin path
	// routes leaf filters separately and its plans are snapshot-pinned.
	if costDrivenJoinOrder {
		return len(bindings) >= 2
	}
	if len(bindings) < 5 {
		return false
	}
	for i, b := range bindings {
		var scan Node
		if i < len(scans) {
			scan = scans[i]
		}
		if smallDimensionSide(scan, b.table) {
			return true
		}
	}
	return false
}

// attachRelationLocalFilters walks the bushy plan tree
// and wraps each leaf scan that owns local predicates
// with a Filter node. The leaf identity is determined
// by pointer equality against the original `scans[i]`
// list passed into the bushy DP — `i` is the binding
// index that the partition step keyed `locals.byBinding`
// against.
//
// Each predicate is rebased from FROM-cumulative
// indices into leaf-local indices via localizeExprToLeaf
// so the executor's evalExprSlot reads from the leaf's
// own slot, not the (no-longer-cumulative) global
// schema.
//
// Per design 01 §3.4: this stage attaches as
// `Filter(SeqScan)` ONLY — no IndexScan promotion,
// no further rewriting. The post-MHJ
// `rewriteScanInputsWithSingleTablePredicates` path
// can still tighten leaves into IndexScans later.
//
// (M0077-0001.)
func attachRelationLocalFilters(
	node Node,
	locals relationLocalFilters,
	scans []Node,
	bindings []rangeBinding,
) Node {
	if len(locals.byBinding) == 0 {
		return node
	}
	// Build identity map: scans[i] → binding index i.
	scanToBinding := make(map[Node]int, len(scans))
	for i, s := range scans {
		scanToBinding[s] = i
	}
	var rewrite func(n Node) Node
	rewrite = func(n Node) Node {
		if n == nil {
			return nil
		}
		// Match leaves by identity against the original
		// scans list. The bushy DP's buildJoinFromDP
		// preserves the same Node pointers as leaves, so
		// pointer-equality is reliable here.
		if bidx, ok := scanToBinding[n]; ok {
			preds := locals.byBinding[bidx]
			if len(preds) == 0 {
				return n
			}
			// Rebase every predicate from FROM-cumulative
			// indices to leaf-local indices.
			binding := bindings[bidx]
			localized := make([]Expr, 0, len(preds))
			for _, p := range preds {
				localized = append(localized, localizeExprToLeaf(p, binding))
			}
			return &Filter{
				Child:     n,
				Predicate: combineAnd(localized),
				LeafLocal: true,
			}
		}
		switch x := n.(type) {
		case *Join:
			x.Left = rewrite(x.Left)
			x.Right = rewrite(x.Right)
			return x
		case *Filter:
			x.Child = rewrite(x.Child)
			return x
		case *Project:
			x.Child = rewrite(x.Child)
			return x
		}
		return n
	}
	return rewrite(node)
}

// localizeExprToLeaf rewrites every ColumnRef.Index in
// the expression from FROM-cumulative coordinates into
// leaf-local coordinates by subtracting binding.offset.
// SourceTableIdx is preserved unchanged (the relation
// identity doesn't change when we rebase).
//
// M0125-0002 commit 6: built on cloneExprRefs / exprChildSlots
// instead of its own 7-of-32 type switch, whose trailing
// pass-through ("Constants … no ColumnRef; pass through") was a
// claim about the seven kinds it knew and a silent lie about the
// other twenty-five: an IsNullExpr, CastExpr, RowExpr, IsBoolExpr,
// CollateExpr or IsDistinctFromExpr wrapping a ColumnRef was
// returned UNCHANGED, i.e. attached to a leaf Filter still carrying
// FROM-cumulative indices. With binding.offset > 0 that reads the
// wrong column at execution time. conjunctIsLocalEligible was the
// only thing keeping it rare, and it was fail-open in the same
// places — the two are one commit for that reason.
//
// Scope policy: scopeVeto, which is unreachable by construction:
// every conjunct arriving here has passed conjunctIsLocalEligible,
// whose own scopeVeto already declined inner plans and unknown
// types over the SAME primitive. An abort therefore means the pair
// has diverged, and that is a planner bug, not a shape this
// function may decline: the caller has already removed the conjunct
// from joinConjuncts, so returning it un-rebased (or dropping it)
// would be a wrong answer. Hence the panic.
//
// (M0077-0001.)
func localizeExprToLeaf(e Expr, binding rangeBinding) Expr {
	if e == nil {
		return nil
	}
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(n Expr) Expr {
			// n is the CLONE — cloneExprRefs shallow-copies every
			// node, leaves included — so mutating it in place leaves
			// the caller's tree untouched. That is the defensive copy
			// the old *ColumnRef arm made by hand, now uniform across
			// all 32 kinds instead of the 7 it enumerated.
			if cr, isCol := n.(*ColumnRef); isCol {
				cr.Index -= binding.offset
			}
			return n
		},
	})
	if !ok {
		panic(fmt.Sprintf("localizeExprToLeaf: cannot rebase %T — "+
			"conjunctIsLocalEligible must decline every conjunct this "+
			"driver aborts on (an inner plan under scopeVeto, or a type "+
			"exprChildSlots does not know). The producer and the consumer "+
			"are one commit for exactly this reason; a silent pass-through "+
			"here leaves FROM-cumulative indices on a leaf-local Filter", e))
	}
	return out
}
