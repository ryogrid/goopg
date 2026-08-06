package planner

import (
	"fmt"
	"math"
	"math/bits"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// GOOPG_COST_DRIVEN_JOINORDER is RETIRED (M0127-P5.9, 2026-08-06). It was the
// C4 pivot's measurement knob — real PG-unit path cost as the old DP's
// argmin, with MultiHashJoin packing dropped — and M0126 closed it as a
// documented no-go (the Q9 MHJ plan could not be cost-forced; every penalty
// that moved Q9 broke Q5). 08 §2 files its retirement as part of the S5 event
// that replaces it: `GOOPG_PGSHAPED_DP` is now the default enumerator, so a
// knob whose only purpose was to re-rank the enumerator it replaced has no
// remaining production meaning and no measurement left to serve.
//
// What is retired is the ENV HOOK, not the mechanism: `costDrivenJoinOrder`
// and `SetCostDrivenJoinOrder` stay until S7 deletes the old subset-bitmask DP
// they belong to (08 §4), because the kill-switch arm still runs that DP and
// its tests still need to reach both of its argmins.
func init() {
	// M0126-0005 measurement-only: force packing off independently of
	// join-order, so the A/B measures the cascade cost without
	// conflating two variables (F12 trap).
	if mhjPackingOffFromEnv(os.Getenv("GOOPG_MHJ_PACKING_OFF")) {
		mhjPackingEnabled = false
	}
}

// mhjPackingOffFromEnv is the measurement switch's polarity, factored out of
// init for the provenance table (flaglabels.go); see memoizeFromEnv. Note the
// inverted name: this variable turns packing OFF, so its unset label reads
// `unset(off)` for "the off-switch is not engaged".
func mhjPackingOffFromEnv(v string) bool { return v == "1" }

// scanKey uniquely identifies a scan by its catalog table pointer and
// FROM‑clause alias.  For self‑joins (e.g. `nation n1, nation n2`)
// the alias distinguishes the two instances; for ordinary tables the
// alias is empty and the table pointer alone is sufficient.
type scanKey struct {
	table *catalog.Table
	alias string
}

// joinEdge is one equijoin predicate between two FROM tables.
type joinEdge struct {
	leftTable  int // index into the FROM list
	rightTable int
	predicate  Expr // the BinaryOp("=") expression
	leftKey    Expr // left-hand side key expression
	rightKey   Expr // right-hand side key expression

	// M0076-0004: marks edges produced from synthesised
	// (transitively-inferred) conjuncts. estimateJoinCost
	// applies `inferredEdgePenalty` (> 1.0) to these edges
	// to bias the bushy DP away from plans that exploit
	// transitivity as if it were independent selectivity.
	// Set by buildJoinGraph when an edge originates from
	// the synthesised tail of the conjunct list (post-
	// inferTransitiveEqualities). Default false.
	isInferred bool
}

// inferredEdgePenalty is the multiplier applied to
// estimateJoinCost when the joinEdge is `isInferred=true`.
// > 1.0 means inferred edges are MORE EXPENSIVE than
// explicit edges, so the bushy DP prefers plans driven
// by original predicates. Initial value 2.0 chosen as
// a conservative starting point; M0076-0001 (Commit D)
// verifies whether this prevents the Q9 regression mode
// when the inferTransitiveEqualities hook is enabled.
// (M0076-0004.)
const inferredEdgePenalty = 2.0

// joinGraph is an undirected graph where nodes are FROM tables and
// edges are equijoin predicates from the WHERE clause.
type joinGraph struct {
	nodes     int
	tables    []*catalog.Table
	edges     []joinEdge
	scans     []Node // SeqScan nodes, index = FROM position
	scanWidth []int  // per-table output schema width
	bindings  []rangeBinding
	mask      uint16 // all-nodes mask for this component
}

// tryBushyDP checks if the bushy join DP is applicable and runs it.
// On success, returns (newPlan, residualPredicate) where residualPredicate
// is the Filter predicate with consumed equalities removed (may be nil).
// On failure, returns (originalNode, originalPred) unchanged.
func tryBushyDP(node Node, pred Expr, ctx *resolveContext, cat catalog.Catalog) (Node, Expr) {
	// M0127-P5.9-b: under `GOOPG_PGSHAPED_DP` the PG-shaped search gets the
	// statement first, and this DP is what it falls back to — the coexistence
	// rule of 08 §2, which keeps `tryBushyDP` callable so the flag is a real
	// rollback and not a one-way door. The seam declines on its own first line
	// with the flag off, so nothing below changes.
	if searched, residual, used := tryPGShapedJoinSearch(node, pred, ctx, cat); used {
		return searched, residual
	}
	if ctx == nil || len(ctx.bindings) < 3 {
		return node, pred
	}
	// Collect tables; enumerateBushyPlans handles missing stats
	// with RowCount=1 defaults internally, so the DP works even
	// before ANALYZE has populated statistics.
	tables := make([]*catalog.Table, len(ctx.bindings))
	for i, b := range ctx.bindings {
		if b.table == nil {
			return node, pred
		}
		tables[i] = b.table
	}
	if len(tables) > 12 {
		return node, pred
	}
	// Extract scan nodes from the CROSS chain.
	scans, scanWidth := extractScans(node)
	if len(scans) != len(tables) {
		return node, pred
	}
	// M0097-0058: buildBindingsPosMap resolves column-index remapping
	// via (table-pointer, alias) scan keys, which only exist for
	// SeqScan and IndexScan leaves. Subquery FROM items (Aggregate,
	// Values, etc.) produce a synthetic catalog.Table in their binding
	// that never matches any scan node, so after bushy DP restructures
	// the join tree the ColumnRefs for subquery columns stay at their
	// pre-DP cross-join indices and cause an index-out-of-bounds panic
	// inside the inner join's row evaluation. Skip bushy DP for any
	// FROM list that contains non-table leaf nodes.
	for _, scan := range scans {
		switch scan.(type) {
		case *SeqScan, *IndexScan, *MultiHashJoin:
			// OK — buildBindingsPosMap can remap these.
		default:
			return node, pred
		}
	}
	conjuncts := splitAnd(pred)
	// M0076-0001 attempt 2026-05-10: re-enabled the
	// inferTransitiveEqualities hook with M0076-0004's
	// inferredEdgePenalty=2.0 cost-model preparation.
	// Empirical result: Q5 still cancelled at 1100s. The
	// synthesised `c.c_nationkey = n.n_nationkey` edge
	// DID appear in Q5's plan (plan-diff showed
	// structural change from MultiHashJoin(6 tables) to
	// nested Hash Joins), but the new plan was WORSE —
	// it estimated 303M rows for the intermediate
	// lineitem⋈orders join, eclipsing the baseline plan's
	// (already-uncompletable) cardinality.
	//
	// Per the M0076 plan risk register R2 + verification
	// protocol: revert behavioural change, keep the
	// M0076-0004 structural infrastructure (joinEdge
	// isInferred field, inferredEdgePenalty constant,
	// buildJoinGraph inferredCount param). Q5 unlock
	// requires deeper cost-model work (build-side memory
	// cost) deferred to M0077.
	//
	// inferredCount=0 keeps the historical pre-M0076
	// behaviour. The hook + cost-model are dormant
	// infrastructure for M0077.

	// M0077-0001 (Slice A): partition single-binding
	// predicates out of the join-DP conjunct list
	// before buildJoinGraph; they get attached to leaf
	// scans below as Filter wrappers AFTER the bushy DP
	// picks a binary tree. This preserves Q5's binary
	// shape because the existing collectMultiHashTables
	// path declines on Filter(SeqScan) leaves.
	//
	// shouldAttachBeforeMHJ gates the rollout: only
	// FROM-clauses with ≥ 5 tables (the shape that
	// triggers MultiHashJoin packing) get pre-MHJ
	// attachment. Smaller queries keep their pre-M0077
	// behaviour. See docs/design/fix-for-q5/01.
	var locals relationLocalFilters
	dpConjuncts := conjuncts
	if shouldAttachBeforeMHJ(ctx.bindings, scans) {
		// Build cumOffsets matching the bindings'
		// FROM-cumulative output coordinates.
		cumOffsets := make([]int, len(ctx.bindings)+1)
		for i, b := range ctx.bindings {
			cumOffsets[i] = b.offset
		}
		// Total schema width = last binding's offset + its width.
		last := ctx.bindings[len(ctx.bindings)-1]
		cumOffsets[len(ctx.bindings)] = last.offset + len(last.table.Columns)
		dpConjuncts, locals = partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)
	}

	// M0077-0002 (Slice B): build per-binding `baseRelInfo`
	// before the DP runs so singleton subsets can seed their
	// row counts from post-filter cardinality. The slice is
	// always built (even when Slice A's gate declined to
	// partition); empty `locals` simply yields
	// `filteredRows == baseRows` and the prior bushy DP
	// behaviour is preserved.
	relInfos := make([]baseRelInfo, len(ctx.bindings))
	for i, b := range ctx.bindings {
		var local Expr
		if preds := locals.byBinding[i]; len(preds) > 0 {
			local = combineAnd(preds)
		}
		var leafScan Node
		if i < len(scans) {
			leafScan = scans[i]
		}
		relInfos[i] = estimateBaseRelInfo(b, leafScan, local)
		relInfos[i].bindingIdx = i
	}

	// M0077-0004 (Slice D): synthesise selective anchored
	// equality edges from `dpConjuncts` + `relInfos`. Edges
	// fire only from anchor (SmallDimension /
	// strongly-filtered / small-anchor-rows) relations to
	// non-anchor relations in the same class — the design
	// 02 §5 rule that avoids the M0075-0001 / M0076-0001 Q9
	// hang while still adding Q5's missing
	// `c_nationkey = n_nationkey` edge when applicable.
	// Tagged inferred via `inferredCount` so the edge
	// inherits M0076-0004's penalty multiplier.
	anchoredEdges := inferAnchoredEqualities(dpConjuncts, relInfos)
	if len(anchoredEdges) > 0 {
		dpConjuncts = append(dpConjuncts, anchoredEdges...)
	}
	inferredCount := len(anchoredEdges)

	g := buildJoinGraph(tables, scans, scanWidth, dpConjuncts, inferredCount, ctx.bindings)
	if g == nil || len(g.edges) == 0 {
		return node, pred
	}
	bushyPlan, residual, err := enumerateBushyPlans(g, dpConjuncts, relInfos, cat)
	if err != nil || bushyPlan == nil {
		return node, pred
	}
	// M0077-0001: attach partitioned local predicates to
	// the matching leaf scans on the bushy plan.
	if len(locals.byBinding) > 0 {
		bushyPlan = attachRelationLocalFilters(bushyPlan, locals, scans, ctx.bindings)
	}
	// Cost-model C0.2 (design ch. 03 §3): route the DP's chosen join subtree
	// through create_plan. Today this is an identity transform (the subtree is
	// carried whole in a PathPrebuilt); from C4 the *cost-selected* path flows
	// through this same seam instead of the integer DP's hand-built tree.
	bushyPlan = createPlanFromDPChoice(bushyPlan)
	if len(residual) == 0 {
		// All conjuncts consumed — Filter is unnecessary.
		return bushyPlan, nil
	}
	pred = combineAnd(residual)
	return bushyPlan, pred
}

// extractScans walks a left-deep CROSS join tree and returns the
// SeqScan nodes in order, plus their output widths.
func extractScans(node Node) ([]Node, []int) {
	var scans []Node
	var widths []int
	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if j, ok := n.(*Join); ok && j.Type == JoinTypeCross {
			walk(j.Left)
			walk(j.Right)
			return
		}
		scans = append(scans, n)
		widths = append(widths, len(n.Output()))
	}
	walk(node)
	return scans, widths
}

// buildJoinGraph extracts equijoin edges from WHERE conjuncts.
//
// M0076-0004: `inferredCount` tags the last N conjuncts
// in the slice as synthesised (transitively-inferred via
// inferTransitiveEqualities). Edges produced from those
// conjuncts get `joinEdge.isInferred = true` so
// estimateJoinCost can apply the penalty multiplier.
// inferredCount = 0 means no synthesised conjuncts (the
// historical pre-M0076 behaviour).
func buildJoinGraph(tables []*catalog.Table, scans []Node, scanWidth []int, conjuncts []Expr, inferredCount int, bindings []rangeBinding) *joinGraph {
	g := &joinGraph{
		nodes:     len(tables),
		tables:    tables,
		scans:     scans,
		scanWidth: scanWidth,
		bindings:  bindings,
	}
	if g.nodes > 16 || g.nodes == 0 {
		return nil
	}
	g.mask = (1 << g.nodes) - 1

	// Build cumulative offsets for column index → table mapping.
	cumOffsets := make([]int, g.nodes+1)
	for i, w := range scanWidth {
		cumOffsets[i+1] = cumOffsets[i] + w
	}

	addEdge := func(bin *BinaryOp, isInferred bool) {
		li := tableForCol(bin.Left, cumOffsets)
		ri := tableForCol(bin.Right, cumOffsets)
		if li < 0 || ri < 0 || li == ri {
			return
		}
		g.edges = append(g.edges, joinEdge{
			leftTable:  li,
			rightTable: ri,
			predicate:  bin,
			leftKey:    bin.Left,
			rightKey:   bin.Right,
			isInferred: isInferred,
		})
	}
	// M0076-0004: explicitStart is the index AFTER which all
	// conjuncts are synthesised. inferredCount=0 → all conjuncts
	// are explicit (pre-M0076 behaviour preserved).
	explicitEnd := len(conjuncts) - inferredCount
	if explicitEnd < 0 {
		explicitEnd = 0
	}
	for i, c := range conjuncts {
		isInferred := i >= explicitEnd
		if bin, ok := c.(*BinaryOp); ok && bin.Op == parser.OpEq {
			addEdge(bin, isInferred)
			continue
		}
		// M0058-0004: descend into OR-of-ANDs predicates so a join
		// predicate like Q19's `(p_partkey=l_partkey AND ...) OR
		// (p_partkey=l_partkey AND ...) OR (...)` contributes a join
		// edge. The full OR remains as a residual predicate; this
		// only feeds the join-order DP.
		for _, eq := range plannerCommonEquijoinsAcrossOr(c) {
			addEdge(eq, isInferred)
		}
	}
	return g
}

// plannerCommonEquijoinsAcrossOr is the planner-Expr counterpart of
// commonEquijoinsAcrossOr in joinorder.go. Returns equality
// expressions of the form `t1.col = t2.col` that appear in every
// branch of an OR predicate.
func plannerCommonEquijoinsAcrossOr(e Expr) []*BinaryOp {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return nil
	}
	branches := flattenPlannerOr(bin)
	if len(branches) < 2 {
		return nil
	}
	branchEqs := make([]map[string]*BinaryOp, len(branches))
	for i, br := range branches {
		branchEqs[i] = map[string]*BinaryOp{}
		for _, c := range splitAnd(br) {
			b, ok := c.(*BinaryOp)
			if !ok || b.Op != parser.OpEq {
				continue
			}
			lc, lok := b.Left.(*ColumnRef)
			rc, rok := b.Right.(*ColumnRef)
			if !lok || !rok {
				continue
			}
			branchEqs[i][colRefIndexPairKey(lc, rc)] = b
		}
	}
	var common []*BinaryOp
	for k, v := range branchEqs[0] {
		ok := true
		for j := 1; j < len(branchEqs); j++ {
			if _, present := branchEqs[j][k]; !present {
				ok = false
				break
			}
		}
		if ok {
			common = append(common, v)
		}
	}
	return common
}

func flattenPlannerOr(e Expr) []Expr {
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpOr {
		return []Expr{e}
	}
	out := flattenPlannerOr(bin.Left)
	out = append(out, flattenPlannerOr(bin.Right)...)
	return out
}

// colRefIndexPairKey produces an order-independent key for two
// planner ColumnRef nodes via their resolved column indices.
func colRefIndexPairKey(a, b *ColumnRef) string {
	li, ri := a.Index, b.Index
	if li > ri {
		li, ri = ri, li
	}
	return fmt.Sprintf("%d==%d", li, ri)
}

// tableForCol returns the FROM-table index that all ColumnRef nodes
// in e belong to, or -1 if columns span multiple tables.
func tableForCol(e Expr, cumOffsets []int) int {
	result := -1
	visitColumnRefsForTable(e, func(colIdx int) {
		for t := 0; t < len(cumOffsets)-1; t++ {
			if colIdx >= cumOffsets[t] && colIdx < cumOffsets[t+1] {
				if result == -1 {
					result = t
				} else if result != t {
					result = -2 // spans multiple tables
				}
				return
			}
		}
		result = -2 // column not in any table
	})
	if result < 0 {
		return -1
	}
	return result
}

// visitColumnRefsForTable invokes onIdx with the Index of every
// same-scope *ColumnRef in e. Its one live consumer is tableForCol.
//
// M0125-0002 commit 4: built on walkExprRefs / exprChildSlots instead
// of its own 12-of-32 type switch. Child structure comes from the
// primitive, so a ColumnRef under IS NULL, a cast, a row constructor
// or IS DISTINCT FROM — all silently skipped by the old arms — now
// contributes its index, and tableForCol attributes the conjunct
// correctly instead of answering -1 from a partial reference set.
//
// Scope policy: scopeIgnore. tableForCol's cumOffsets attribution is
// only meaningful for indices in THIS scope's coordinate space: an
// inner plan's ColumnRefs index the subplan's own schema and an
// *OuterColumnRef names a scope above, so neither reaches onIdx (the
// old walker's documented declines, preserved — see
// visit_refs_for_table_arms_test.go). A subquery node's PARAM_EXEC
// Args are same-scope slots and ARE visited now, as is the Operand of
// a Plan-carrying InExpr — the old arm returned before visiting
// anything when Plan != nil, so `col IN (subquery)` read as "no
// table" even though col is an ordinary same-scope reference.
//
// An unenumerated type panics, matching PG's
// expression_tree_walker_impl (nodeFuncs.c:2667); a silent skip means
// tableForCol partitions on an incomplete reference set (RC-1a).
func visitColumnRefsForTable(e Expr, onIdx func(int)) {
	walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(x Expr) bool {
			if cr, ok := x.(*ColumnRef); ok {
				onIdx(cr.Index)
			}
			return true
		},
		OnUnknown: func(x Expr) {
			panic(fmt.Sprintf("visitColumnRefsForTable: unrecognized expression type %T — "+
				"teach exprChildSlots (exprwalk.go) about it; a silent skip makes "+
				"tableForCol partition on an incomplete reference set", x))
		},
	})
}

// isConnectedMask checks whether the subset mask is connected.
func isConnectedMask(mask uint16, g *joinGraph) bool {
	if mask == 0 {
		return false
	}
	start := bits.TrailingZeros16(mask)
	visited := uint16(1 << start)
	queue := []int{start}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, e := range g.edges {
			if e.leftTable != u && e.rightTable != u {
				continue
			}
			v := e.leftTable
			if v == u {
				v = e.rightTable
			}
			mv := uint16(1 << v)
			if mask&mv == 0 || visited&mv != 0 {
				continue
			}
			visited |= mv
			queue = append(queue, v)
		}
	}
	return visited == mask
}

func hasCrossEdge(a, b uint16, g *joinGraph) bool {
	for _, e := range g.edges {
		ma := uint16(1 << e.leftTable)
		mb := uint16(1 << e.rightTable)
		if (a&ma != 0 && b&mb != 0) || (a&mb != 0 && b&ma != 0) {
			return true
		}
	}
	return false
}

func findEdgeBetween(a, b uint16, g *joinGraph) *joinEdge {
	e, _ := findEdgeBetweenIdx(a, b, g)
	return e
}

// findEdgeBetweenIdx is findEdgeBetween that also returns the edge's
// index in g.edges, so callers can mark just that specific edge as
// used (rather than all edges within a mask).
func findEdgeBetweenIdx(a, b uint16, g *joinGraph) (*joinEdge, int) {
	for i := range g.edges {
		e := &g.edges[i]
		ma := uint16(1 << e.leftTable)
		mb := uint16(1 << e.rightTable)
		if (a&ma != 0 && b&mb != 0) || (a&mb != 0 && b&ma != 0) {
			return e, i
		}
	}
	return nil, -1
}

type dpEntry struct {
	plan Node
	// M0077-0002 (Slice B): post-filter row count for this
	// subset. Singletons take `baseRelInfo.filteredRows`;
	// composed subsets take the chosen join's output
	// cardinality. `buildJoinFromDP` reads this for the
	// build-side decision (replacing the prior
	// `EstimateRows(plan)` re-evaluation).
	rows int64
	cost int64

	// layout maps each base-table index (0..nodes-1) to the column
	// offset at which that table's columns begin within THIS entry's
	// plan.Output(). A singleton {i} is {i:0}; a composed subset is
	// leftLayout ∪ {t: rightLayout[t] + leftWidth}. buildJoinFromDP
	// uses it to remap a join key to its REAL position in a child
	// plan whose schema is `leftSchema ++ rightSchema` — which is NOT
	// ascending table order for bushy compositions (enumerateSplits
	// assigns arbitrary subsets to left/right). The old ascending
	// assumption mis-resolved keys for non-ascending subsets (TPC-H
	// Q8 → 0 rows under cost-driven order; see cost-model
	// IMPLEMENTATION-TODO). For ascending subsets layout[t] equals the
	// old prefix-sum, so behaviour is unchanged for every plan the
	// integer DP produces today.
	layout map[int]int

	// pgCost is the subtree's cost in real PG units (cost.h), built
	// bottom-up: base = costSeqscan; join = hashJoinCost over the two
	// children's pgCosts (or nestloopCost when the join will convert
	// to an NL-index, C4-pg-ii). When costDrivenJoinOrder is on the DP
	// selects the join order by pgCost.Total instead of the integer
	// `cost` heuristic (cost-model ch. 12 §2). Always computed so the
	// switch is a comparison-key change, not a structural one.
	pgCost Cost
}

// costDrivenJoinOrder switches enumerateBushyPlans from the integer
// `output + build*4 + probe` argmin (bushy.go:764) to real PG-unit
// path cost (dpEntry.pgCost) for join-order selection — the C4 pivot
// (cost-model ch. 12 §2). Default OFF: the integer DP is unchanged and
// plan-preserving until a measurement gate promotes the switch.
var costDrivenJoinOrder = false

// SetCostDrivenJoinOrder toggles cost-driven join-order selection.
// Returns the previous value so a caller/test can restore it.
func SetCostDrivenJoinOrder(v bool) bool {
	prev := costDrivenJoinOrder
	costDrivenJoinOrder = v
	return prev
}

// mhjPackingEnabled controls whether planSelect packs binary hash-join
// chains into a MultiHashJoin (rewriteMultiWayChain). PG has no MHJ;
// the cost-driven planner drops it (ch. 12 §3) so the DP's PG-shaped
// binary tree is final and the order-then-rewrite mismatch that
// regressed Q9 cannot recur. Default ON to preserve the current
// (non-cost-driven) planner; SetCostDrivenJoinOrder(true) should be
// paired with SetMHJPackingEnabled(false).
var mhjPackingEnabled = false

// SetMHJPackingEnabled toggles MultiHashJoin packing. Returns the
// previous value.
func SetMHJPackingEnabled(v bool) bool {
	prev := mhjPackingEnabled
	mhjPackingEnabled = v
	return prev
}

// nliCostDelegation, when true (default under cost-driven order), makes
// the DP cost each candidate join as the method rewriteJoinsToNLI will
// ACTUALLY build it (ch. 12 §4): consult tryBuildNLI on a clone and, if
// convertible, cost the join with nestloopCost + indexProbeCost instead
// of hashJoinCost. Construction stays solely in rewriteJoinsToNLI — the
// DP only borrows tryBuildNLI as the shared predicate so its ranking and
// the executed method cannot desync.
var nliCostDelegation = true

// SetNLICostDelegation toggles delegated NLI costing. Returns previous.
func SetNLICostDelegation(v bool) bool {
	prev := nliCostDelegation
	nliCostDelegation = v
	return prev
}

// isProbableInnerScan reports whether a node is a base-relation scan
// (optionally behind a Filter) — the shape tryBuildNLI index-probes as
// the NLI inner. Used to decide which side drives the loop when costing
// a delegated NLI (ch. 12 §4).
func isProbableInnerScan(n Node) bool {
	switch x := n.(type) {
	case *SeqScan, *IndexScan:
		return true
	case *Filter:
		return isProbableInnerScan(x.Child)
	}
	return false
}

// entryNCols is `relNCols` for the bushy DP's own entry type: the column count
// of the subtree this entry already built, which is what the hash geometry must
// be solved for (M0127-P5.7-a). Zero for an entry with no plan, which
// hashJoinCost reads as "assume no spill".
func entryNCols(e dpEntry) int {
	if e.plan == nil {
		return 0
	}
	return len(e.plan.Output())
}

// costJoinCandidate returns the PG-unit cost of a candidate join as the
// method it will actually execute. Default: hashJoinCost. When NLI-cost
// delegation is on and tryBuildNLI reports the join convertible (on a
// shallow clone, so no mutation escapes), it is costed as a nested loop
// whose inner is one index probe per outer row — the cost that makes the
// DP avoid orders that NL-probe a large relation.
func costJoinCandidate(cp costParams, join *Join, entryA, entryB dpEntry, outRows int64, cat catalog.Catalog) Cost {
	hashCost := hashJoinCost(cp, hashJoinInputs{
		outer: entryA.pgCost, inner: entryB.pgCost,
		outerRows: float64(entryA.rows), innerRows: float64(entryB.rows),
		outputRows:     float64(outRows),
		numHashClauses: 1,
		// This DP has no RelOptInfo; its entries carry the executor subtree
		// they built, whose schema is the same one the join will concatenate
		// (M0127-P5.7-a).
		outerCols: entryNCols(entryA), innerCols: entryNCols(entryB),
	})

	// M0127-P5.6-d: the quadratic large-build penalty that used to be added
	// here is GONE. M0126-0013 charged `overshoot² × cpu_tuple_cost ×
	// innerRows` above a fixed 2 M-row threshold as a stand-in deterrent
	// against join orders that chain enormous intermediate builds (Q9's
	// cost-driven pathology). It stood in for the batch I/O of a build that
	// does not fit memory, which M0127-P5.7-a now charges honestly inside
	// hashJoinCost — and charges better, because the stand-in's threshold was
	// a fixed ROW COUNT while whether a build fits depends on its width: at
	// one column, 6 M rows fit the default budget and were penalised anyway;
	// at forty columns, 1 M rows spill and were not penalised at all. The
	// spill term asks `hashsize.Choose` — the executor's own geometry
	// function — so the deterrent now fires exactly when the executor will
	// really write batch files. leftdeep-joins 04 §4; 06 §5.
	if !nliCostDelegation || cat == nil || join == nil {
		return hashCost
	}
	clone := *join
	if _, ok := tryBuildNLI(&clone, cat); !ok {
		return hashCost
	}
	// tryBuildNLI prefers the Right child as the index-probed inner; the
	// other side drives the loop. Cost = outer scan + one probe per outer
	// row (indexProbeCost) + per-output CPU.
	outerCost, outerRows, innerCost := entryA.pgCost, entryA.rows, entryB.pgCost
	if !isProbableInnerScan(entryB.plan) {
		outerCost, outerRows, innerCost = entryB.pgCost, entryB.rows, entryA.pgCost
	}
	// innerRows is `inner_path->rows` — for the index probe this arm costs
	// (one `indexProbeCost` per outer row) that is the PER-PROBE count, which
	// this DP models as a single matched row, not the probed relation's total.
	// M0127-P5.9-j moved the `cpu_tuple_cost` charge onto `outer × inner`
	// tuples processed; passing 1 here keeps this arm's per-outer-row charge
	// exactly what `outRows` used to buy it whenever the join emits about one
	// row per outer row, and unlike `outRows` it does not vary with a
	// downstream cardinality this arm cannot see.
	return nestloopCost(cp, outerCost, innerCost, float64(outerRows), 1, indexProbeCost(cp))
}

// bushySeedRowCounts computes the per-relation row count the bushy DP seeds its
// singleton subsets with. Every join-order decision the DP makes descends from
// these numbers, so this is the highest-leverage cardinality input in the
// planner and the reason M0125-0003 stages it separately.
//
// The tiers, in order, each falling through only when the one above has nothing:
//
//  1. M0077-0002 (Slice B): the post-filter row count from `relInfos`. Best
//     available — it is the only tier that accounts for local predicates.
//  2. The historical `tableRows` lookup: ANALYZE'd, pre-filter.
//  3. M0125-0003 stage 2: the estimate_rel_size fallback, derived from the LIVE
//     block count and the declared column widths. This is the first tier that
//     needs no statistics at all, which is the whole point — tiers 1 and 2 are
//     both 0 on a cold-started server (TableStats.RowCount does not survive a
//     restart, ledger pq-P6), so before this the DP saw a flat 1 for every
//     relation and ranked join orders on no cardinality signal whatsoever.
//  4. The 1-row floor, which is also what tier 3 returns to when the flag is
//     off. Flag-off is therefore byte-identical to the pre-M0125-0003 DP.
//
// Tier 3 is deliberately PRE-filter: the cold server that reaches it has no
// column statistics either, so scaling by a selectivity would be inventing
// precision the estimate does not have. estimateBaseRelInfo makes the same
// choice for an unreliable selectivity (cardinality.go).
//
// Note the ordering consequence once stage 3 lands: it feeds the fallback into
// `estimateBaseRelInfo.baseRows`, which makes tier 1 positive on a cold server
// and SHADOWS tier 3 at this site. That is intended — stage 3's estimate is
// strictly better here, having passed through the local filter — but it means
// stage 2's arm of the four-arm measurement must be read before stage 3 lands,
// not after.
func bushySeedRowCounts(g *joinGraph, relInfos []baseRelInfo, cat catalog.Catalog) []int64 {
	rowCounts := make([]int64, g.nodes)
	for i, tbl := range g.tables {
		if i >= len(rowCounts) {
			break // defensive: a malformed graph must not panic the planner
		}
		switch {
		case i < len(relInfos) && relInfos[i].filteredRows > 0:
			rowCounts[i] = relInfos[i].filteredRows
		case tbl != nil && tbl.Stats != nil && tbl.Stats.RowCount > 0:
			rowCounts[i] = tbl.Stats.RowCount
		default:
			// Returns 0 with the flag off or below stage 2, and 0 whenever the
			// catalog cannot report a live block count — both land on the floor.
			if rows := relSizeFallbackRows(2, cat, tbl); rows > 0 {
				rowCounts[i] = rows
			} else {
				rowCounts[i] = 1
			}
		}
	}
	return rowCounts
}

func enumerateBushyPlans(g *joinGraph, conjuncts []Expr, relInfos []baseRelInfo, cat catalog.Catalog) (Node, []Expr, error) {
	if g.nodes == 0 {
		return nil, nil, nil
	}
	if g.nodes == 1 {
		// Mark edges as used (none to mark for 1 table).
		residual := make([]Expr, 0, len(conjuncts))
		for _, c := range conjuncts {
			bin, ok := c.(*BinaryOp)
			if !ok || bin.Op != parser.OpEq {
				residual = append(residual, c)
			}
		}
		return g.scans[0], residual, nil
	}

	rowCounts := bushySeedRowCounts(g, relInfos, cat)

	edgeUsed := make([]bool, len(g.edges))
	dp := make(map[uint16]dpEntry)
	cp := defaultCostParams()

	for i := 0; i < g.nodes; i++ {
		mask := uint16(1 << i)
		width := nodeTupleWidth(g.scans[i])
		baseCost := costSeqscan(cp, estScanPages(float64(rowCounts[i]), width), float64(rowCounts[i]), 0)
		dp[mask] = dpEntry{plan: g.scans[i], rows: rowCounts[i], cost: rowCounts[i], layout: map[int]int{i: 0}, pgCost: baseCost}
	}

	for size := 2; size <= g.nodes; size++ {
		var masks []uint16
		enumerateSubsets(g.mask, size, func(m uint16) {
			masks = append(masks, m)
		})
		for _, mask := range masks {
			if !isConnectedMask(mask, g) {
				continue
			}
			var best *dpEntry
			var bestEdgeIdx int
			var bestA, bestB uint16
			bestEdgeIdx = -1
			enumerateSplits(mask, func(a, b uint16) {
				if !isConnectedMask(a, g) || !isConnectedMask(b, g) {
					return
				}
				if !hasCrossEdge(a, b, g) {
					return
				}
				edge, edgeIdx := findEdgeBetweenIdx(a, b, g)
				if edge == nil {
					return
				}
				entryA, okA := dp[a]
				entryB, okB := dp[b]
				if !okA || !okB {
					return
				}
				// M0077-0003 (Slice C): cost is the 3-part
				// `output + build*4 + probe` value; outputRows
				// becomes the composed subset's `dpEntry.rows`
				// so deeper DP levels see this subset's
				// cardinality estimate rather than its cost
				// (the two diverge once the build term enters).
				outRows, cost := estimateJoinCost(entryA.rows, entryB.rows, edge, a, b, g, cat)
				// Cost-driven mode builds the candidate join up front so its
				// pgCost can reflect the method rewriteJoinsToNLI will build
				// (ch. 12 §2, §4). Integer mode defers the build to the
				// winner only (unchanged, plan-preserving).
				var join *Join
				var mergedLayout map[int]int
				var pgCost Cost
				if costDrivenJoinOrder {
					join = buildJoinFromDP(entryA.plan, entryB.plan, entryA.rows, entryB.rows, a, b, entryA.layout, entryB.layout, edge, g)
					mergedLayout = mergeSubsetLayouts(entryA.layout, entryB.layout, len(entryA.plan.Output()))
					pgCost = costJoinCandidate(cp, join, entryA, entryB, outRows, cat)
				}
				// Selection key: real PG cost when cost-driven, else the
				// integer heuristic.
				better := best == nil
				if !better {
					if costDrivenJoinOrder {
						better = pgCost.Total < best.pgCost.Total
					} else {
						better = cost < best.cost
					}
				}
				if better {
					if join == nil {
						join = buildJoinFromDP(entryA.plan, entryB.plan, entryA.rows, entryB.rows, a, b, entryA.layout, entryB.layout, edge, g)
						mergedLayout = mergeSubsetLayouts(entryA.layout, entryB.layout, len(entryA.plan.Output()))
					}
					best = &dpEntry{plan: join, rows: outRows, cost: cost, layout: mergedLayout, pgCost: pgCost}
					bestEdgeIdx = edgeIdx
					bestA, bestB = a, b
				}
			})
			if best != nil {
				dp[mask] = *best
				if costDrivenJoinOrder {
					// Cost-driven buildJoinFromDP attached EVERY cross-edge
					// between bestA and bestB onto the join in local coords
					// (attachExtraEdgesLocal), so mark them all consumed —
					// none should survive as a residual or be re-attached in
					// global coords by attachUnusedCrossEdges (which the
					// composite NLI cannot localise; the Q9=0 cause).
					for i := range g.edges {
						e := &g.edges[i]
						la := bestA&(1<<e.leftTable) != 0
						ra := bestA&(1<<e.rightTable) != 0
						lb := bestB&(1<<e.leftTable) != 0
						rb := bestB&(1<<e.rightTable) != 0
						if (la && rb) || (ra && lb) {
							edgeUsed[i] = true
						}
					}
				} else if bestEdgeIdx >= 0 {
					// Mark only the SPECIFIC edge picked at this
					// DP step. Internal edges of the two subsets
					// were marked when their dp[] entries were
					// computed. Marking all edges in mask (the old
					// behaviour) over‑consumes when two tables are
					// connected by multiple equality conjuncts —
					// e.g. TPC‑H Q9's partsupp↔lineitem (ps_suppkey
					// =l_suppkey AND ps_partkey=l_partkey): the
					// join uses one edge, the other should remain
					// a residual conjunct that pushdown can AND
					// onto the join's predicate.
					edgeUsed[bestEdgeIdx] = true
				}
			}
		}
	}

	fullEntry, ok := dp[g.mask]
	if !ok {
		return nil, conjuncts, nil
	}

	// TPC-H Q9's partsupp<->lineitem pair is connected by TWO
	// equalities (ps_suppkey=l_suppkey AND ps_partkey=l_partkey);
	// the DP loop above only ever wires ONE such edge into a
	// join's canonical LeftKey/RightKey, leaving the other
	// unattached anywhere in the winning tree. Attach any
	// still-unused edge onto the lowest *Join that first
	// co-locates both of its tables, using RAW (global, unremapped)
	// key expressions — see attachUnusedCrossEdges's doc comment
	// for why this coordinate choice matters.
	attachUnusedCrossEdges(fullEntry.plan, g, edgeUsed)

	// Build residual conjuncts. A conjunct is consumed only if the
	// SPECIFIC edge that carries its predicate was used by the DP
	// — checking by table-pair alone over‑consumes when two
	// tables are connected by multiple equalities (e.g. TPC‑H Q9's
	// partsupp↔lineitem ps_suppkey=l_suppkey AND ps_partkey=
	// l_partkey: only one edge wins; the other must surface as
	// residual so pushdown can AND it onto the join's predicate).
	residual := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		bin, ok := c.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			residual = append(residual, c)
			continue
		}
		li := tableForCol(bin.Left, buildCumOffsets(g))
		ri := tableForCol(bin.Right, buildCumOffsets(g))
		if li < 0 || ri < 0 || li == ri {
			residual = append(residual, c)
			continue
		}
		used := false
		for i, e := range g.edges {
			if !edgeUsed[i] {
				continue
			}
			if e.predicate == bin {
				used = true
				break
			}
		}
		if !used {
			residual = append(residual, c)
		}
	}
	return fullEntry.plan, residual, nil
}

// attachUnusedCrossEdges walks the winning bushy-DP tree bottom-up
// and ANDs any equality edge that DP left unused (edgeUsed[i] ==
// false) onto the lowest *Join node that first co-locates both of
// the edge's two tables — i.e. the join whose Left/Right children
// each contain exactly one side of the edge. See
// enumerateBushyPlans's call site for the motivating TPC-H Q9 shape.
//
// The attached predicate is the edge's RAW, unremapped
// `*BinaryOp` (`joinEdge.predicate`, still carrying the original
// global FROM-order ColumnRef indices) rather than a copy remapped
// into the Join's own local Left/Right subset coordinates (the
// convention `LeftKey`/`RightKey` use). This is deliberate: a join
// built here may later be folded into a `*MultiHashJoin` by
// rewriteMultiWayChain, whose `collectMultiHashTables` already
// expects any AND'd-on "extra" conjunct beyond the canonical
// equality to be in that same global coordinate space (see its
// `extraInScans`-guarded capture, added to handle exactly this
// query shape). `collectCrossSideEquiKeys` (nl_index_join.go) is
// taught a name-based fallback so the NestedLoopIndexJoin rewrite
// can also consume a global-coordinate extra conjunct correctly.
// Returns the subtree's table mask (bit i set ⇔ g.scans[i] is a
// leaf of plan) so callers can recurse.
func attachUnusedCrossEdges(plan Node, g *joinGraph, edgeUsed []bool) uint16 {
	if plan == nil {
		return 0
	}
	for i, s := range g.scans {
		if s == plan {
			return uint16(1) << i
		}
	}
	j, ok := plan.(*Join)
	if !ok {
		return 0
	}
	leftMask := attachUnusedCrossEdges(j.Left, g, edgeUsed)
	rightMask := attachUnusedCrossEdges(j.Right, g, edgeUsed)
	for i := range g.edges {
		if edgeUsed[i] {
			continue
		}
		e := &g.edges[i]
		lBit := uint16(1) << e.leftTable
		rBit := uint16(1) << e.rightTable
		spans := (leftMask&lBit != 0 && rightMask&rBit != 0) ||
			(leftMask&rBit != 0 && rightMask&lBit != 0)
		if !spans {
			continue
		}
		if j.Predicate == nil {
			j.Predicate = e.predicate
		} else {
			j.Predicate = &BinaryOp{pos: e.predicate.Pos(), Op: parser.OpAnd, Left: j.Predicate, Right: e.predicate}
		}
		edgeUsed[i] = true
	}
	return leftMask | rightMask
}

func buildCumOffsets(g *joinGraph) []int {
	off := make([]int, g.nodes+1)
	for i, w := range g.scanWidth {
		off[i+1] = off[i] + w
	}
	return off
}

func enumerateSubsets(universe uint16, size int, fn func(uint16)) {
	subsetBits(universe, 0, 0, size, fn)
}

func subsetBits(universe uint16, start, current, remaining int, fn func(uint16)) {
	if remaining == 0 {
		fn(uint16(current))
		return
	}
	n := bits.OnesCount16(universe)
	for i := start; i <= n-remaining; i++ {
		bit := uint16(1 << i)
		if universe&bit != 0 {
			subsetBits(universe, i+1, current|(1<<i), remaining-1, fn)
		}
	}
}

func enumerateSplits(mask uint16, fn func(a, b uint16)) {
	sub := (mask - 1) & mask
	for sub != 0 {
		comp := mask ^ sub
		if sub != 0 && comp != 0 {
			fn(sub, comp)
		}
		sub = (sub - 1) & mask
	}
}

// M0077-0003 (Slice C): three-part hash-join cost weights per
// design 02 §3. The build term must be heavy enough that the
// DP visibly prefers plans whose hash-build inputs are small
// (filtered region/nation, customer) over plans that build a
// hash table from a large unfiltered fact (lineitem/orders).
// Probe and output remain at unit weight so the formula stays
// approximately proportional to total work for selective
// filtered plans.
const (
	outputRowWeight = 1
	hashBuildWeight = 4
	hashProbeWeight = 1
)

// estimateJoinCost returns (outputRows, cost) for a candidate
// hash join over the given filtered row counts. cost is the
// 3-part formula:
//
//	cost = outputRows*outputRowWeight
//	     + buildRows*hashBuildWeight
//	     + probeRows*hashProbeWeight
//
// buildRows is the smaller side (the side that hashes); probeRows
// is the larger side (the side that streams). M0076-0004's
// inferredEdgePenalty multiplier is applied AFTER the 3-part
// sum so an inferred edge of any shape gets the same penalty
// factor relative to its explicit equivalent.
//
// The single-output `(L*R)/maxNDV` quantity is still computed —
// it becomes outputRows, also stored in `dpEntry.rows` so
// downstream subsets see this subset's cardinality estimate
// rather than re-reading raw table sizes.
//
// (M0077-0003 / Slice C per design 02 §3.)
// accurateKeyDistinct returns the distinct-value count of a single join-key
// column, preferring the estimate NDistinctFrac × RowCount (PG's negative
// stadistinct fraction) over NDistinct. The two agree since
// M0127-P5.6-e-iii — ANALYZE now scales the sample's distinct count up to the
// relation (Haas-Stokes) and writes both renderings of that one estimate — so
// this preference no longer picks a winner; before it, NDistinct was the raw
// sample count and saturated at ~30000. It resolves the SPECIFIC key column
// (not the table max) because an
// equijoin's selectivity is governed by the joined columns only. Mirrors
// cardinality.go's columnNDistinctForChild resolution (ColumnRef.Index indexes
// the base table's positional Stats.Columns). Returns 0 when unresolvable.
func accurateKeyDistinct(key Expr, tbl *catalog.Table) int64 {
	if tbl == nil || tbl.Stats == nil {
		return 0
	}
	// M0127-P5.6-f-ii: resolve through the NAME, for the coordinate-space
	// reason spelled out on `edgeColName`. Indexing Stats.Columns by
	// `ColumnRef.Index` — which is what this did — read out of range for
	// every join key in Q5 (returning 0, so `sideKeyDistinct` quietly served
	// the table-wide maximum and every equi-join in the search was priced by a
	// column it does not mention), and IN range for `nation`, where it served
	// `n_comment`'s distinct count for `n_nationkey`.
	name, ok := edgeColName(key, tbl)
	if !ok {
		return 0
	}
	idx, ok := tableColumnIndex(tbl, name)
	if !ok || idx >= len(tbl.Stats.Columns) {
		return 0
	}
	return columnStatsDistinct(tbl.Stats.Columns[idx], tbl.Stats.RowCount)
}

// columnStatsDistinct renders a column's distinct estimate as an ABSOLUTE
// count, through upstream's stadistinct sign convention (`StaDistinct()`:
// positive is a count, negative is a fraction of the relation's rows —
// get_variable_numdistinct, selfuncs.c).
//
// The arithmetic this replaces — "prefer NDistinctFrac × RowCount whenever
// NDistinctFrac > 0" — predates M0127-P5.6-e-iii, which made NDistinct and
// NDistinctFrac two renderings of ONE estimate and put the 10 %-of-rows choice
// between them in `StaDistinct()`. Multiplying the fraction unconditionally is
// the branch `StaDistinct()` exists to arbitrate, so it has to go through it.
func columnStatsDistinct(cs catalog.ColumnStats, rowCount int64) int64 {
	sd := cs.StaDistinct()
	switch {
	case sd > 0:
		return int64(sd)
	case sd < 0 && rowCount > 0:
		return int64(-sd * float64(rowCount))
	}
	return 0
}

// maxColSaturatedDistinct is the historical fallback: the largest per-column
// NDistinct across the table. Used when the join key does not resolve to
// a base-table ColumnRef, so keyless/degenerate edges estimate as before.
// The name predates M0127-P5.6-e-iii, when NDistinct held the raw SAMPLE count
// and therefore saturated at the statistics target; it is now the Haas-Stokes
// table-wide estimate. Kept as-is because the fallback's CALLERS are what the
// name warns about — a table max is not the join key's ndistinct.
func maxColSaturatedDistinct(tbl *catalog.Table) int64 {
	if tbl == nil || tbl.Stats == nil {
		return 0
	}
	best := int64(0)
	for _, cs := range tbl.Stats.Columns {
		if cs.NDistinct > best {
			best = cs.NDistinct
		}
	}
	return best
}

// sideKeyDistinct resolves the accurate join-key distinct for one side, falling
// back to the table's saturated max when the key is unresolvable.
func sideKeyDistinct(key Expr, tbl *catalog.Table) int64 {
	if d := accurateKeyDistinct(key, tbl); d > 0 {
		return d
	}
	return maxColSaturatedDistinct(tbl)
}

// satRowsMulDiv returns a*b/d clamped to [1, MaxInt64], computed in
// float64 so the a*b product cannot overflow int64. Composed-subset
// cardinalities reach ~1e14 at SF1 scale; an int64 a*b then wraps
// negative before the divide. float64 keeps ~15 significant digits —
// ample for a cardinality estimate — and saturates instead of wrapping.
func satRowsMulDiv(a, b, d int64) int64 {
	if d < 1 {
		d = 1
	}
	v := float64(a) * float64(b) / float64(d)
	if v >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if v < 1 {
		return 1
	}
	return int64(v)
}

// satMul returns a*b clamped to [1, MaxInt64], computed in float64 so the
// product cannot wrap int64 negative — the §4 multi-clause divisor multiplies
// several ~1e5 per-column NDVs, whose product reaches ~1e15.
func satMul(a, b int64) int64 {
	if a < 1 {
		a = 1
	}
	if b < 1 {
		b = 1
	}
	v := float64(a) * float64(b)
	if v >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(v)
}

// satCost sums the 3-part integer cost in float64 and clamps to
// [1, MaxInt64], for the same overflow reason as satRowsMulDiv (the
// output/build/probe terms can each be ~1e14 at scale).
func satCost(out, build, probe int64) int64 {
	v := float64(out)*float64(outputRowWeight) +
		float64(build)*float64(hashBuildWeight) +
		float64(probe)*float64(hashProbeWeight)
	if v >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if v < 1 {
		return 1
	}
	return int64(v)
}

// crossEdgesBetween returns every graph edge that straddles the two subset
// masks a and b (in either FROM-order orientation). findEdgeBetweenIdx returns
// only the FIRST such edge; the FK/unique/multi-clause estimators (ch. 14
// §2–§4) need ALL of them — Q9's partsupp↔lineitem join spans two edges
// (ps_partkey=l_partkey AND ps_suppkey=l_suppkey) and a single edge sees only
// one column of the composite key.
func crossEdgesBetween(a, b uint16, g *joinGraph) []*joinEdge {
	var out []*joinEdge
	for i := range g.edges {
		e := &g.edges[i]
		lm := uint16(1) << uint(e.leftTable)
		rm := uint16(1) << uint(e.rightTable)
		if (a&lm != 0 && b&rm != 0) || (a&rm != 0 && b&lm != 0) {
			out = append(out, e)
		}
	}
	return out
}

// crossEdgesOrSelf is `crossEdgesBetween` with the caller's already-selected
// edge as the floor. The masks are 0 in the unit tests that call
// `estimateJoinCost` directly (and would be for any caller pricing an edge
// outside a DP step), where the enumeration finds nothing and the single edge
// is all the evidence there is.
func crossEdgesOrSelf(edge *joinEdge, a, b uint16, g *joinGraph) []*joinEdge {
	if edges := crossEdgesBetween(a, b, g); len(edges) > 0 {
		return edges
	}
	if edge == nil {
		return nil
	}
	return []*joinEdge{edge}
}

// edgeColName resolves the base-table column a join-key expression refers to.
//
// M0127-P5.6-f-ii INVERTED the preference order this function was written with.
// Its previous comment read "preferring the reliable table-relative Index over
// the diagnostic Name" — and the Index is not table-relative. A `joinEdge`'s
// key expressions are written in the query's GLOBAL FROM-list coordinate space,
// so on TPC-H Q5 `c_nationkey` arrives as `Index: 16` against an 8-column
// `customer` and `s_nationkey` as `Index: 8` against a 7-column `supplier`.
//
// The old order hid that in the two ways a coordinate-space error always hides
// (the P5.6-e-ii `RightKey` row records the same pair): out of range, it fell
// through to `Name` and looked correct; IN range, it silently answered for
// ANOTHER column — Q5's `n_nationkey` is `Index: 3`, and `nation`'s fourth
// column is `n_comment`. The name is the only end that is meaningful in this
// space, so it goes first, checked against the table so a stale or qualified
// name cannot invent a column. The Index is kept as the fallback for the
// synthesised edges (transitive inference, unit-test graphs) whose ColumnRefs
// carry an Index and no Name.
func edgeColName(key Expr, tbl *catalog.Table) (string, bool) {
	cr, ok := key.(*ColumnRef)
	if !ok || tbl == nil {
		return "", false
	}
	if cr.Name != "" {
		if _, ok := tableColumnIndex(tbl, cr.Name); ok {
			return cr.Name, true
		}
	}
	if cr.Index >= 0 && cr.Index < len(tbl.Columns) {
		return tbl.Columns[cr.Index].Name, true
	}
	if cr.Name != "" {
		return cr.Name, true
	}
	return "", false
}

// tableColumnIndex is the positional index of a column name in the base table,
// which is also its index into Stats.Columns (ANALYZE fills that slice
// positionally against tbl.Columns — operators_analyze.go's
// `stats.Columns[i] = computeColumnStats(reservoir, i, …)` loop).
func tableColumnIndex(tbl *catalog.Table, name string) (int, bool) {
	if tbl == nil {
		return -1, false
	}
	for i := range tbl.Columns {
		if tbl.Columns[i].Name == name {
			return i, true
		}
	}
	return -1, false
}

// columnsSubset reports whether every name in want is present in have.
func columnsSubset(want []string, have map[string]bool) bool {
	if len(want) == 0 {
		return false
	}
	for _, c := range want {
		if !have[c] {
			return false
		}
	}
	return true
}

// The ch. 14 §2/§3 superkey/FK proof used to live here as
// `uniqueNoFanoutRawCount`. It was deleted by M0127-P5.6-f-ii in favour of
// `graphJoinKeyDivisor` (joinkeyproof.go), which is the SAME algorithm as the
// `*Join`-space prover P5.6-f landed, arm for arm — and which fixes the two
// defects the ledger recorded against the version that stood here: its FK arm
// divided by the CHILD's raw count where upstream divides by the PARENT's
// (costsize.c:5847), and it took the MAX over qualifying sides instead of
// consuming each edge once and multiplying over the disjoint keys it can prove.

func estimateJoinCost(leftRows, rightRows int64, edge *joinEdge, a, b uint16, g *joinGraph, cat catalog.Catalog) (outputRows, cost int64) {
	if leftRows <= 0 {
		leftRows = 1
	}
	if rightRows <= 0 {
		rightRows = 1
	}
	// M0127-P5.6-f-ii: ONE divisor for both branches.
	//
	// Until this task the integer DP (the PRODUCTION branch) had a second
	// implementation here: `ndv` was the maximum NDistinct across EVERY column
	// of the edge's two tables — a column the join need not even mention — and
	// only the `costDrivenJoinOrder` arm resolved the join KEY. That is why
	// P5.6-f made Q9's joinrel estimate exact without moving its plan by a
	// single node: `estimateJoin` prices the FINISHED plan, and the number the
	// SEARCH selected on was still coming from here (09 §5.5).
	//
	// WHY THE TWO ARMS COULD NOT BE MERGED HALFWAY, which is what was tried
	// first and measured: adding only the superkey/FK proof, and leaving the
	// no-proof case on the table-wide maximum, made Q5 *worse* than either
	// consistent policy. Q5's `lineitem ⋈ supplier` became truthful (39 981 →
	// 5 997 241, via supplier's proven `s_suppkey`) while its rival
	// `customer ⋈ supplier` on `c_nationkey = s_nationkey` stayed at the
	// table-wide maximum — 150 000, `c_custkey`'s, when the join key has 25
	// distinct values — and read 10 000 against a real 60 000 000. The DP duly
	// picked the 6 000×-underestimated cartesian product and Q5 went from 65.9 s
	// to over the 150 s audit timeout. A search compares estimates: making one
	// of them truthful while its competitor stays wrong is not half a fix, it
	// is a new bug. Evidence: `analysis/leftdeep-joins/2026-08-05-p56fii-halfway.txt`.
	//
	// ch. 14 §2–§4, now for both arms: enumerate ALL edges spanning the two
	// subsets, not just the first one `findEdgeBetweenIdx` returned. If the
	// equated columns contain a UNIQUE index / valid FK as a superkey, the join
	// does not fan out → divide by the key side's RAW tuple count (§2/§3, and
	// for an FK by the PARENT's — `graphJoinKeyDivisor`). Otherwise divide by
	// the PRODUCT of every spanning edge's per-column NDV (§4 multi-clause),
	// which is what `clauselist_selectivity` does with the clauses the proof did
	// not consume. `sideKeyDistinct` still falls back to the table-wide maximum
	// per edge when a key expression names no base column, so a keyless or
	// degenerate edge is priced exactly as it always was.
	//
	// The ch. 12 §5 note this replaces — "deliberately NOT applied to the
	// integer DP, whose build*4 weights are calibrated to the saturated regime
	// and regress under accurate cardinality (measured)" — was written when
	// ANALYZE stored the SAMPLE's distinct count, so "accurate" meant a number
	// saturated at ~30 000 for every large relation. M0127-P5.6-e-iii replaced
	// that with the Haas–Stokes estimate scaled to the relation, and the regime
	// the weights were fitted to no longer exists on either arm.
	ndv := int64(1)
	edges := crossEdgesOrSelf(edge, a, b, g)
	if raw, ok := graphJoinKeyDivisor(edges, g, cat); ok {
		ndv = max(ndv, raw)
	} else {
		prod := int64(1)
		for _, e := range edges {
			en := int64(1)
			if e.leftTable < len(g.tables) {
				en = max(en, sideKeyDistinct(e.leftKey, g.tables[e.leftTable]))
			}
			if e.rightTable < len(g.tables) {
				en = max(en, sideKeyDistinct(e.rightKey, g.tables[e.rightTable]))
			}
			prod = satMul(prod, en)
		}
		ndv = max(ndv, prod)
	}
	// Overflow guard: for deep composed subsets at scale, leftRows and
	// rightRows can each be ~1e12–1e14, so the int64 product wraps
	// NEGATIVE before the `/ ndv` divide and the `< 1` clamp then pins it
	// to 1 — a garbage cardinality that poisons the integer cost, the
	// build-side decision, AND the cost-driven pgCost (which reads this as
	// outRows). Compute in float64 (ample range for an estimate) and
	// saturate at MaxInt64. This is the scale boundary behind Q9's 6M-vs-
	// 300k behaviour.
	outputRows = satRowsMulDiv(leftRows, rightRows, ndv)
	// Build side = smaller, probe side = larger. This is the
	// same heuristic `buildJoinFromDP` uses to pick BuildLeft;
	// keeping the cost-side and physical-side decisions in
	// sync prevents the "right shape, wrong build" failure mode
	// design 02 §4 calls out.
	buildRows, probeRows := leftRows, rightRows
	if rightRows < leftRows {
		buildRows, probeRows = rightRows, leftRows
	}
	cost = satCost(outputRows, buildRows, probeRows)
	// M0076-0004: penalise edges produced from synthesised
	// (transitively-inferred) conjuncts. Slice D adds these
	// only via anchored synthesis; the penalty stays as a final
	// tiebreaker so even an anchored edge isn't preferred over
	// an explicit equivalent of identical 3-part cost.
	if edge.isInferred {
		cost = int64(float64(cost) * inferredEdgePenalty)
		if cost < 1 {
			cost = 1
		}
	}
	return outputRows, cost
}

func buildJoinFromDP(leftPlan, rightPlan Node, leftRows, rightRows int64, a, b uint16, leftLayout, rightLayout map[int]int, edge *joinEdge, g *joinGraph) *Join {
	// Determine which edge key belongs to which subset BEFORE
	// remapping.  The edge stores {leftTable, rightTable} in
	// FROM-clause order, but the DP may have assigned those
	// tables to different subsets.  Remapping the wrong key into
	// the wrong subset produces out-of-range ColumnRef indices.
	lk := edge.leftKey
	rk := edge.rightKey
	if a&(1<<edge.leftTable) == 0 {
		// leftTable is in subset b → leftKey belongs to b
		lk, rk = edge.rightKey, edge.leftKey
	}
	// Remap each key to its REAL position within the child plan using
	// that child's actual table→offset layout, not an ascending-order
	// assumption: a bushy child schema is `leftSchema ++ rightSchema`
	// over arbitrary subsets, so a table's columns need not sit at
	// their ascending prefix-sum. (Fixes the Q8=0-rows cost-driven
	// remap bug; see dpEntry.layout.)
	leftKey := remapKeyToLayout(lk, leftLayout, g)
	rightKey := remapKeyToLayout(rk, rightLayout, g)

	leftSchema := leftPlan.Output()
	rightSchema := rightPlan.Output()
	mergedSchema := make(Schema, len(leftSchema)+len(rightSchema))
	copy(mergedSchema, leftSchema)
	copy(mergedSchema[len(leftSchema):], rightSchema)

	// M0077-0002 (Slice B): build-side decision uses the
	// post-filter rowcounts threaded through `dpEntry.rows`
	// rather than re-reading `EstimateRows(plan)` per call.
	// Falls back to `EstimateRows` only when the caller passed
	// a non-positive sentinel (defensive against test fixtures).
	lRows := leftRows
	if lRows <= 0 {
		lRows = EstimateRows(leftPlan)
	}
	rRows := rightRows
	if rRows <= 0 {
		rRows = EstimateRows(rightPlan)
	}
	buildLeft := lRows > 0 && rRows > 0 && lRows < rRows
	// M0054-0010: when one side is a known small-dimension table
	// (region, nation) but stats are absent on the other side,
	// pin the small-dim side to the build side. Without stats the
	// row-count comparison above defaults to false (== build on
	// right), so a left-side small-dim table would otherwise
	// hash-build on the much larger right side. Detect both
	// directions here.
	leftSmall := IsSmallDimensionSide(leftPlan)
	rightSmall := IsSmallDimensionSide(rightPlan)
	if leftSmall && !rightSmall {
		buildLeft = true
	} else if rightSmall && !leftSmall {
		buildLeft = false
	}

	// For the RightKey, shift indices to the right side by left schema width.
	if cr, ok := rightKey.(*ColumnRef); ok {
		cl := *cr
		cl.Index += len(leftSchema)
		rightKey = &cl
	}

	j := &Join{
		pos:       0,
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      leftPlan,
		Right:     rightPlan,
		Predicate: &BinaryOp{pos: 0, Op: parser.OpEq, Left: leftKey, Right: rightKey},
		LeftKey:   leftKey,
		RightKey:  rightKey,
		BuildLeft: buildLeft,
		schema:    mergedSchema,
	}

	// Cost-model C4: when two subsets are connected by MORE than one
	// equality (TPC-H Q9's partsupp↔lineitem: ps_suppkey=l_suppkey AND
	// ps_partkey=l_partkey), the DP wires ONE as the canonical
	// LeftKey/RightKey; the rest must also be enforced on this join.
	// The non-cost-driven planner defers them to attachUnusedCrossEdges
	// (raw GLOBAL coords, consumed by MultiHashJoin). With MHJ dropped
	// (cost-driven), no consumer localises those global coords and the
	// composite NLI probes a wrong column → drops rows (Q9=0). So here,
	// where the layouts are known, AND each extra edge onto the
	// Predicate in the SAME LOCAL coordinates as the canonical key.
	if costDrivenJoinOrder {
		attachExtraEdgesLocal(j, a, b, leftLayout, rightLayout, len(leftSchema), edge, g)
	}

	return j
}

// attachExtraEdgesLocal ANDs every cross-edge between subsets a and b
// (other than the canonical `skip` edge, already the join's key) onto
// j.Predicate, remapping each side's key to the join's local schema —
// a-side into leftLayout, b-side into rightLayout shifted by leftWidth,
// exactly as the canonical key is remapped. This keeps a multi-equality
// join (Q9) fully local-coordinate so the downstream NLI/hash consumer
// resolves both probe columns correctly.
func attachExtraEdgesLocal(j *Join, a, b uint16, leftLayout, rightLayout map[int]int, leftWidth int, skip *joinEdge, g *joinGraph) {
	for i := range g.edges {
		e := &g.edges[i]
		if e == skip {
			continue
		}
		la := a&(1<<e.leftTable) != 0
		ra := a&(1<<e.rightTable) != 0
		lb := b&(1<<e.leftTable) != 0
		rb := b&(1<<e.rightTable) != 0
		// The edge must cross a↔b (one endpoint in each subset).
		if !((la && rb) || (ra && lb)) {
			continue
		}
		// Orient so ak belongs to subset a, bk to subset b.
		ak, bk := e.leftKey, e.rightKey
		if !la {
			ak, bk = e.rightKey, e.leftKey
		}
		aKey := remapKeyToLayout(ak, leftLayout, g)
		bKey := remapKeyToLayout(bk, rightLayout, g)
		if cr, ok := bKey.(*ColumnRef); ok {
			cl := *cr
			cl.Index += leftWidth
			bKey = &cl
		}
		extra := &BinaryOp{pos: 0, Op: parser.OpEq, Left: aKey, Right: bKey}
		j.Predicate = &BinaryOp{pos: 0, Op: parser.OpAnd, Left: j.Predicate, Right: extra}
	}
}

// mergeSubsetLayouts composes the table→offset layouts of a join's two
// children into the parent's layout. The runtime schema is
// `leftSchema ++ rightSchema`, so left-side tables keep their offsets
// and every right-side table shifts by leftWidth (= len(leftPlan.Output())).
func mergeSubsetLayouts(leftLayout, rightLayout map[int]int, leftWidth int) map[int]int {
	merged := make(map[int]int, len(leftLayout)+len(rightLayout))
	for t, off := range leftLayout {
		merged[t] = off
	}
	for t, off := range rightLayout {
		merged[t] = off + leftWidth
	}
	return merged
}

// remapKeyToLayout adjusts a ColumnRef index from the global
// (FROM-order concatenation) coordinate space to a child plan's LOCAL
// schema, using that plan's actual table→offset layout. The global
// index identifies a base table t and a within-table offset via the
// g.scanWidth prefix sums; the local index is layout[t] + that
// within-table offset. Unlike the old ascending-order assumption this
// is correct for bushy child schemas (`leftSchema ++ rightSchema` over
// arbitrary subsets). For an ascending subset layout[t] equals the old
// prefix-sum, so the result is identical to the prior behaviour.
func remapKeyToLayout(key Expr, layout map[int]int, g *joinGraph) Expr {
	if col, ok := key.(*ColumnRef); ok {
		cl := *col
		offset := 0
		for i := 0; i < g.nodes; i++ {
			w := g.scanWidth[i]
			if cl.Index >= offset && cl.Index < offset+w {
				if localBase, ok := layout[i]; ok {
					cl.Index = localBase + (cl.Index - offset)
				}
				return &cl
			}
			offset += w
		}
		return &cl
	}
	return cloneExprLeaf(key)
}

func markEdgesInMask(mask uint16, g *joinGraph, used []bool) {
	for i, e := range g.edges {
		ma := uint16(1 << e.leftTable)
		mb := uint16(1 << e.rightTable)
		if mask&ma != 0 && mask&mb != 0 {
			used[i] = true
		}
	}
}

// collectMultiHashTables walks a left-deep chain of hash joins and
// collects the SeqScan leaf nodes. Returns the scan nodes in join
// order, the join keys for each edge, the probe table index, and
// any extra residual conjuncts that were ANDed onto Inner-Hash
// joins by `pushOneConjunct` (which the original chain detection
// silently dropped because it only inspected `j.LeftKey` /
// `j.RightKey`). Returns (nil, nil, 0, nil) when the tree is not a
// valid chain (≥3 tables, all JoinAlgoHash, all inner, chained).
func collectMultiHashTables(node Node) ([]Node, []MultiHashKey, int, []Expr) {
	var scans []Node
	var keys []MultiHashKey
	var extras []Expr

	var walk func(n Node) bool
	walk = func(n Node) bool {
		if s, ok := n.(*SeqScan); ok {
			scans = append(scans, s)
			return true
		}
		// Stop at scope boundaries: aggregate, sort, project,
		// filter — these represent query phases, not join trees.
		if _, ok := n.(*Aggregate); ok {
			return false
		}
		if _, ok := n.(*Sort); ok {
			return false
		}
		if _, ok := n.(*Project); ok {
			return false
		}
		if _, ok := n.(*Filter); ok {
			return false
		}
		j, ok := n.(*Join)
		if !ok || j.Algo != JoinAlgoHash || j.Type != JoinTypeInner {
			return false
		}
		leftStart := len(scans)
		if !walk(j.Left) {
			return false
		}
		leftEnd := len(scans) // right subtree starts here
		if !walk(j.Right) {
			return false
		}
		rightEnd := len(scans)
		// Capture extras AND'd onto j.Predicate beyond the canonical
		// `LeftKey = RightKey` equality, but ONLY when every
		// ColumnRef in the extra references a column NAME present
		// in some scan inside the MHJ's subset. pushOneConjunct's
		// width-based side classification can mis-push a conjunct
		// onto a Join whose subtree doesn't actually contain the
		// referenced tables (e.g. TPC-H Q9 pushes
		// `ps_partkey = l_partkey` onto a 4-table Inner Join that
		// doesn't include partsupp because the global FROM index of
		// ps_partkey happens to fall inside that Join's
		// subset-FROM-order width range). Capturing such conjuncts
		// into MHJ.Filters would attempt to evaluate them on the
		// MHJ output row where the ps_partkey column doesn't exist,
		// producing wrong results. The conjunct is left in the
		// outer Filter where the bindings posMap will remap it to
		// actual scan offsets and the post-join evaluation will
		// see all tables.
		for _, c := range splitAnd(j.Predicate) {
			if isCanonicalKeyEquality(c, j.LeftKey, j.RightKey) {
				continue
			}
			if extraInScans(c, scans) {
				extras = append(extras, c)
			}
		}

		// Determine which scan and column the left/right join keys
		// reference.  We resolve by column name rather than by
		// ColumnRef.Index, because remapKeyToSubset (called from
		// buildJoinFromDP) produces FROM-order indices while the
		// tree walk collects scans in DFS order — the two orders
		// may differ for bushy DP trees.  Column names are
		// unique (prefixed by table), so the lookup is unambiguous.
		li, lc := findScanByColName(scans, leftStart, leftEnd, j.LeftKey)
		ri, rc := findScanByColName(scans, leftEnd, rightEnd, j.RightKey)
		if li >= 0 && ri >= 0 {
			keys = append(keys, MultiHashKey{
				LeftTable:  li,
				LeftCol:    lc,
				RightTable: ri,
				RightCol:   rc,
			})
		}
		return true
	}
	if !walk(node) || len(scans) < 3 {
		return nil, nil, 0, nil
	}

	// M0126-0001 Stage −1: a tree of N scans must have exactly N−1
	// join keys connecting them. Fewer keys means at least one table
	// is unreached and would be silently NULL-padded — a
	// silent-wrong-answer path (bundle Stage −1, risk R1). Fail
	// closed: decline to pack.
	if len(keys) != len(scans)-1 {
		return nil, nil, 0, nil
	}

	// Verify the keys form a simple chain: each table may appear
	// at most twice (once as "source", once as "destination").
	// Star graphs (e.g. lineitem at the centre of Q9) cannot be
	// expressed as a single chain; the MultiHashJoin probe loop
	// only follows one path through the keys.
	chainOK := true
	tableDeg := make([]int, len(scans))
	for _, k := range keys {
		tableDeg[k.LeftTable]++
		tableDeg[k.RightTable]++
	}
	for _, d := range tableDeg {
		if d > 2 {
			chainOK = false
			break
		}
	}
	if !chainOK {
		return nil, nil, 0, nil
	}

	// Determine probe table: the one with the largest row count.
	probeIdx := 0
	probeRows := int64(0)
	for i, s := range scans {
		if r := EstimateRows(s); r > probeRows {
			probeRows = r
			probeIdx = i
		}
	}
	return scans, keys, probeIdx, extras
}

// isCanonicalKeyEquality reports whether c is the canonical
// `LeftKey = RightKey` BinaryOp produced by buildJoinFromDP /
// pushOneConjunct's CROSS→Inner promotion. Used to filter that
// canonical equality out when capturing extras from a Join's
// AND'd Predicate.
func isCanonicalKeyEquality(c Expr, leftKey, rightKey Expr) bool {
	bin, ok := c.(*BinaryOp)
	if !ok || bin.Op != parser.OpEq {
		return false
	}
	return bin.Left == leftKey && bin.Right == rightKey
}

// extraInScans reports whether every ColumnRef in c references a
// column name that appears in the output schema of at least one
// scan in scans. Used to validate that an MHJ.Filters extra
// belongs to the MHJ's subset before capturing it.
//
// M0125-0002 commit 7 — the fail-open this whole series was scoped
// around. `allMatched` starts true and is only ever falsified from
// INSIDE the callback, so before the conversion a conjunct built
// entirely from kinds the 7-arm walker did not enumerate produced
// ZERO callbacks and was admitted into MultiHashJoin.Filters on a
// vacuous true (design doc §"Why this is not just fixing stale
// indices"). The second result of visitColumnRefsByName closes it:
// "the name test did not cover c" is NOT MATCHED, never matched.
// D3 predetermined this inversion before a line was written.
func extraInScans(c Expr, scans []Node) bool {
	allMatched := true
	total := visitColumnRefsByName(c, func(name string) {
		found := false
		for _, s := range scans {
			ss, ok := s.(*SeqScan)
			if !ok {
				continue
			}
			for _, col := range ss.Output() {
				if col.Name == name {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			allMatched = false
		}
	})
	return total && allMatched
}

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

// findScanByColName resolves a join-key ColumnRef to a
// (scan-index, column-within-scan) pair by matching the column
// name against scans[start..end).  Column names are unique per
// table (TPC-H prefixes: p_partkey, s_suppkey, …) so the lookup
// is unambiguous.  Returns (-1, 0) when the key type is not a
// ColumnRef or the name is not found.
func findScanByColName(scans []Node, start, end int, key Expr) (scanIdx int, colIdx int) {
	cr, ok := key.(*ColumnRef)
	if !ok {
		return -1, 0
	}
	for i := start; i < end; i++ {
		s, ok := scans[i].(*SeqScan)
		if !ok {
			continue
		}
		for j, col := range s.Output() {
			if col.Name == cr.Name {
				return i, j
			}
		}
	}
	return -1, 0
}

// rewriteMultiWayChain walks the plan tree and replaces chains of
// ≥3 hash-joined tables with MultiHashJoin nodes. The catalog
// argument is forwarded to `rewriteMHJInputsWithSingleTablePredicates`
// so single-table constant-RHS filters can promote their input scan
// from `SeqScan` to `IndexScan` (M0054-0006a-pre).
func rewriteMultiWayChain(node Node, cat catalog.Catalog) Node {
	if node == nil {
		return nil
	}
	// M0127-P5.9-b (08 §3): never pack a searched tree. PG has no MHJ, the
	// search's binary cascade IS the plan it costed, and the packer re-sorts
	// the leaf layout — the order-then-rewrite mismatch that regressed Q9
	// (ch. 12 §3). `mhjPackingEnabled` is off by default, so this guard is for
	// the env that turns it back on.
	if isSearchedTree(node) {
		return node
	}
	scans, keys, probeIdx, extras := collectMultiHashTables(node)
	if scans == nil {
		// Not a valid chain — recurse into children.
		// Only recurse into Join (binary ops) and thin wrappers
		// (Filter, Project, Sort). Do NOT recurse into Aggregate:
		// crossing a plan-phase boundary mixes table scopes.
		switch n := node.(type) {
		case *Join:
			n.Left = rewriteMultiWayChain(n.Left, cat)
			n.Right = rewriteMultiWayChain(n.Right, cat)
		case *Filter:
			n.Child = rewriteMultiWayChain(n.Child, cat)
		case *Project:
			n.Child = rewriteMultiWayChain(n.Child, cat)
		case *Sort:
			n.Child = rewriteMultiWayChain(n.Child, cat)
		}
		return node
	}

	// Build MultiHashJoin node.
	mh := &MultiHashJoin{
		pos:        node.Pos(),
		Tables:     scans,
		Keys:       keys,
		ProbeTable: probeIdx,
		Filters:    extras,
	}
	// Sort the tables (scans) by catalog OID so the output
	// schema is in FROM-clause (table-creation) order, matching
	// the binary join tree that was replaced.  Without this
	// sort, the schema is in DFS tree-walk order which differs
	// for bushy DP trees and would require downstream ColumnRef
	// remapping.
	//
	// The sort also remaps the MultiHashKey table indices and
	// the probe table index.
	type idxEntry struct {
		idx  int
		oid  uint32
		scan *SeqScan
	}
	items := make([]idxEntry, len(scans))
	for i, s := range scans {
		items[i] = idxEntry{idx: i, scan: s.(*SeqScan), oid: s.(*SeqScan).Table.OID}
	}
	byOID := func(i, j int) bool { return items[i].oid < items[j].oid }
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if byOID(j, i) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	// Build old-to-new scan index mapping.
	oldToNew := make([]int, len(scans))
	sortedScans := make([]Node, len(scans))
	for newIdx, item := range items {
		oldToNew[item.idx] = newIdx
		sortedScans[newIdx] = scans[item.idx]
	}
	// Update keys and probe table.
	for i := range keys {
		keys[i].LeftTable = oldToNew[keys[i].LeftTable]
		keys[i].RightTable = oldToNew[keys[i].RightTable]
	}
	mh.Tables = sortedScans
	mh.ProbeTable = oldToNew[probeIdx]

	// Build output schema from all tables (now in FROM order).
	fullSchema := make(Schema, 0)
	for _, s := range sortedScans {
		fullSchema = append(fullSchema, s.Output()...)
	}
	mh.schema = fullSchema

	// M0054-0006a-pre: rewrite SeqScan inputs to IndexScan whenever a
	// single-table constant-RHS filter from `mh.Filters` admits a
	// usable B-tree index. Closes the M0054-0003d Q8 gap
	// (`p_type = 'ECONOMY ANODIZED STEEL'` → `Index Scan using
	// idx_part_type on part`). Mutates mh in place; absorbed
	// conjuncts are removed from mh.Filters.
	rewriteMHJInputsWithSingleTablePredicates(mh, cat)

	return mh
}

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
	case *MultiHashJoin:
		return
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
		// Only use MHJ posMap (OID‑based); binaryTreePosMapOf is
		// disabled here because it assumes OID order == FROM order
		// which is not always true — remapWithBindings handles that
		// case using the actual FROM‑clause bindings.
		pm := mhjPosMapOf(n.Child)
		if pm != nil {
			remapByPosMap(&n.Predicate, pm)
		}
		subRemap([]Expr{n.Predicate})
		return
	case *Project:
		// M0063-0001: skip isolated-scope Projects (view rename wrapper).
		if n.IsolatedScope {
			return
		}
		remapPosMapAfterRewrite(n.Child, nil)
		pm := mhjPosMapOf(n.Child)
		if pm != nil {
			for i := range n.Targets {
				remapByPosMap(&n.Targets[i], pm)
			}
		}
		subRemap(n.Targets)
		return
	case *Sort:
		remapPosMapAfterRewrite(n.Child, nil)
		pm := mhjPosMapOf(n.Child)
		if pm != nil {
			for i := range n.Keys {
				remapByPosMap(&n.Keys[i].Expr, pm)
			}
		}
		for i := range n.Keys {
			subRemap([]Expr{n.Keys[i].Expr})
		}
		return
	case *Aggregate:
		remapPosMapAfterRewrite(n.Child, nil)
		pm := mhjPosMapOf(n.Child)
		if pm != nil {
			for i := range n.GroupExprs {
				remapByPosMap(&n.GroupExprs[i], pm)
			}
			for i := range n.Aggs {
				if n.Aggs[i].Arg != nil {
					remapByPosMap(&n.Aggs[i].Arg, pm)
				}
				if n.Aggs[i].Arg2 != nil {
					remapByPosMap(&n.Aggs[i].Arg2, pm)
				}
			}
		}
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

// binaryTreePosMapOf collects SeqScan leaves from a binary join
// tree (traversing through thin wrappers), sorts them by OID
// (FROM order), and returns a position map from old (FROM-order)
// to new (DFS‑order) positions.
func binaryTreePosMapOf(node Node) func(int) int {
	var scans []Node
	var collect func(Node)
	collect = func(n Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *SeqScan:
			scans = append(scans, x)
		case *Join:
			collect(x.Left)
			collect(x.Right)
		case *Filter:
			collect(x.Child)
		case *Project:
			collect(x.Child)
		case *Sort:
			collect(x.Child)
		case *Aggregate:
			collect(x.Child)
		}
	}
	collect(node)
	if len(scans) < 3 {
		return nil // 2‑table trees are left‑deep (FROM order)
	}
	// Build MHJ-like posMap from these scans.
	mh := &MultiHashJoin{Tables: make([]Node, len(scans))}
	for i, s := range scans {
		mh.Tables[i] = s
	}
	return buildMHJPosMap(mh)
}

// remapExprRefsToMHJ is the old entry point; use
// remapColumnRefsAfterRewrite instead.
func remapExprRefsToMHJ(node Node) Node {
	return remapColumnRefsAfterRewrite(node)
}

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

// mhjPosMapOf was a position map keyed by table OID, intended to
// remap FROM‑order ColumnRef indices into the MHJ's OID‑sorted
// output. The implementation was fundamentally broken: it assumed
// FROM‑order == OID‑order (false whenever the FROM list isn't in
// table‑creation order, which is most TPC‑H queries), and it
// collapsed duplicate OIDs (self‑joins like TPC‑H Q7's
// `nation n1, nation n2` where both scans share the nation OID).
// The bindings‑based posMap (`buildBindingsPosMap`, used by
// `remapWithBindings`) correctly handles both cases — it has access
// to the actual FROM order via `rangeBinding.offset` and uses
// `scanKey{table, alias}` to disambiguate self‑joins.
//
// Returning nil here makes the first remap pass a no‑op for all
// node arms; the second (bindings) pass handles everything that
// matters.
func mhjPosMapOf(node Node) func(int) int { return nil }

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

// buildMHJPosMap returns a position map from old (FROM‑order
// binary tree) column positions to new (MHJ DFS‑order) column
// positions for the given MultiHashJoin.  The map uses table
// OIDs to correctly disambiguate duplicate column names.
func buildMHJPosMap(mh *MultiHashJoin) func(int) int {
	type tblInfo struct {
		oid uint32
		off int
		w   int
	}
	infos := make([]tblInfo, len(mh.Tables))
	off := 0
	for i, t := range mh.Tables {
		if s, ok := t.(*SeqScan); ok {
			infos[i] = tblInfo{oid: s.Table.OID, off: off, w: len(s.Output())}
			off += len(s.Output())
		}
	}
	// Sort by OID to get FROM‑order.
	sorted := make([]tblInfo, len(infos))
	copy(sorted, infos)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].oid > sorted[j].oid {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	fromWidth := 0
	for i := range sorted {
		fromWidth += sorted[i].w
	}
	return func(oldIdx int) int {
		if oldIdx < 0 || oldIdx >= fromWidth {
			return oldIdx
		}
		off2 := 0
		for si := range sorted {
			w := sorted[si].w
			if oldIdx >= off2 && oldIdx < off2+w {
				colIdx := oldIdx - off2
				targetOID := sorted[si].oid
				for ei := range infos {
					if infos[ei].oid == targetOID {
						return infos[ei].off + colIdx
					}
				}
				break
			}
			off2 += w
		}
		return oldIdx
	}
}

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
		case *MultiHashJoin:
			// M0125-0013: recurse through `collect` instead of
			// matching bare scans inline. An MHJ table is NOT
			// always a bare scan: pushSingleSourceFiltersIntoMHJTables
			// wraps Tables[i] in a *Filter when it pushes a
			// single-source conjunct down (and, for >=5-table FROM
			// clauses, so does attachRelationLocalFilters). The old
			// two-case switch silently skipped such a table — it
			// recorded no scanEntry AND never advanced `off`, so
			// every table to its RIGHT got an offset short by the
			// skipped table's width, while the skipped table's own
			// columns kept their FROM-cumulative index. Both halves
			// then landed in a different relation's columns: TPC-DS
			// Q47 returned s_county for d_year and a d_date_sk for
			// s_store_sk, with the row COUNT still correct because
			// only the top projection was misremapped.
			//
			// This is the same silent-fallthrough defect the
			// `default:` arm below was hardened against in RC-2; the
			// MHJ loop simply never received that fix. `collect`
			// treats *SeqScan / *IndexScan exactly as the old code
			// did (M0062-0002 alias preservation included), sees
			// through the *Filter wrapper via its pass-through arm,
			// and declines the whole remap on anything it cannot
			// classify — the documented safe direction.
			for _, t := range x.Tables {
				collect(t)
			}
		case *Join:
			collect(x.Left)
			collect(x.Right)
		case *NestedLoopIndexJoin:
			// M0062-0006: NLI sits between Filter and the underlying
			// MHJ for Q9-shape plans. Without this case the collect
			// walker stops at NLI and `buildBindingsPosMap` returns
			// an empty scanMap, so `p_name`'s ColumnRef.Index is
			// never re-resolved against the OID-sorted MHJ output.
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
			*PgAvailableWalSummaries, *PgGetSequenceData, *VerifyHeapam:
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
	case *MultiHashJoin:
		// MHJ keys are stored in per-table column-index pairs and
		// are independent of the output schema. Filters, however,
		// are evaluated on the joined output row — their
		// ColumnRefs come from `pushOneConjunct` ANDing residual
		// conjuncts onto Inner-Hash joins, then collectMultiHash-
		// Tables capturing those extras when those joins were
		// absorbed into the MHJ. Those refs are in pre-rewrite
		// (global FROM-order) coords; remap them to MHJ-output
		// coords here.
		for i := range n.Filters {
			remapByPosMap(&n.Filters[i], posMap)
		}
		return
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
		// post‑rewrite child output schemas. The bushy DP produced
		// subset‑FROM‑order indices, but rewriteMultiWayChain may
		// have OID‑sorted the inner subtree (the MHJ), invalidating
		// those indices. Looking up by ColumnRef.Name in the
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
// truthful before its parent binds against it. Gated on costDrivenJoinOrder
// (Plan), so only the experimental cost path — where NLI is being re-enabled
// — pays for it; production is untouched.
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
	case *MultiHashJoin:
		for i := range n.Tables {
			reconcileNLILayout(n.Tables[i])
		}
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
