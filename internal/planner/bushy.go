package planner

import (
	"fmt"
	"math/bits"

	"github.com/goopg/goopg/internal/catalog"
)

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
}

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
	conjuncts := splitAnd(pred)
	g := buildJoinGraph(tables, scans, scanWidth, conjuncts, ctx.bindings)
	if g == nil || len(g.edges) == 0 {
		return node, pred
	}
	bushyPlan, residual, err := enumerateBushyPlans(g, conjuncts, cat)
	if err != nil || bushyPlan == nil {
		return node, pred
	}
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
func buildJoinGraph(tables []*catalog.Table, scans []Node, scanWidth []int, conjuncts []Expr, bindings []rangeBinding) *joinGraph {
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

	addEdge := func(bin *BinaryOp) {
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
		})
	}
	for _, c := range conjuncts {
		if bin, ok := c.(*BinaryOp); ok && bin.Op == "=" {
			addEdge(bin)
			continue
		}
		// M0058-0004: descend into OR-of-ANDs predicates so a join
		// predicate like Q19's `(p_partkey=l_partkey AND ...) OR
		// (p_partkey=l_partkey AND ...) OR (...)` contributes a join
		// edge. The full OR remains as a residual predicate; this
		// only feeds the join-order DP.
		for _, eq := range plannerCommonEquijoinsAcrossOr(c) {
			addEdge(eq)
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
	if !ok || bin.Op != "OR" {
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
			if !ok || b.Op != "=" {
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
	if !ok || bin.Op != "OR" {
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

// visitColumnRefsForTable is a local helper that visits ColumnRef
// nodes in an expression tree. Mirrors pushdown.go's visitColumnRefs
// but uses planner Expr instead of parser Expr.
func visitColumnRefsForTable(e Expr, onIdx func(int)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		onIdx(x.Index)
	case *OuterColumnRef, *SubqueryExpr, *InExpr, *ExistsExpr:
		// outer refs and subqueries → out of scope
	case *BinaryOp:
		visitColumnRefsForTable(x.Left, onIdx)
		visitColumnRefsForTable(x.Right, onIdx)
	case *UnaryOp:
		visitColumnRefsForTable(x.Operand, onIdx)
	case *FuncCall:
		for _, a := range x.Args {
			visitColumnRefsForTable(a, onIdx)
		}
	case *CaseExpr:
		visitColumnRefsForTable(x.Operand, onIdx)
		for _, w := range x.Whens {
			visitColumnRefsForTable(w.When, onIdx)
			visitColumnRefsForTable(w.Then, onIdx)
		}
		visitColumnRefsForTable(x.Else, onIdx)
	case *ExtractExpr:
		visitColumnRefsForTable(x.Source, onIdx)
	}
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
	cost int64
}

func enumerateBushyPlans(g *joinGraph, conjuncts []Expr, cat catalog.Catalog) (Node, []Expr, error) {
	if g.nodes == 0 {
		return nil, nil, nil
	}
	if g.nodes == 1 {
		// Mark edges as used (none to mark for 1 table).
		residual := make([]Expr, 0, len(conjuncts))
		for _, c := range conjuncts {
			bin, ok := c.(*BinaryOp)
			if !ok || bin.Op != "=" {
				residual = append(residual, c)
			}
		}
		return g.scans[0], residual, nil
	}

	rowCounts := make([]int64, g.nodes)
	for i, tbl := range g.tables {
		if tbl != nil && tbl.Stats != nil && tbl.Stats.RowCount > 0 {
			rowCounts[i] = tbl.Stats.RowCount
		} else {
			rowCounts[i] = 1
		}
	}

	edgeUsed := make([]bool, len(g.edges))
	dp := make(map[uint16]dpEntry)

	for i := 0; i < g.nodes; i++ {
		mask := uint16(1 << i)
		dp[mask] = dpEntry{plan: g.scans[i], cost: rowCounts[i]}
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
				cost := estimateJoinCost(entryA.cost, entryB.cost, edge, g, cat)
				if best == nil || cost < best.cost {
					join := buildJoinFromDP(entryA.plan, entryB.plan, a, b, edge, g)
					best = &dpEntry{plan: join, cost: cost}
					bestEdgeIdx = edgeIdx
				}
			})
			if best != nil {
				dp[mask] = *best
				if bestEdgeIdx >= 0 {
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
		if !ok || bin.Op != "=" {
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

func estimateJoinCost(leftRows, rightRows int64, edge *joinEdge, g *joinGraph, cat catalog.Catalog) int64 {
	if leftRows <= 0 {
		leftRows = 1
	}
	if rightRows <= 0 {
		rightRows = 1
	}
	ndv := int64(1)
	if edge.leftTable < len(g.tables) && g.tables[edge.leftTable] != nil {
		tbl := g.tables[edge.leftTable]
		if tbl.Stats != nil {
			for _, cs := range tbl.Stats.Columns {
				if cs.NDistinct > 0 {
					ndv = max(ndv, int64(cs.NDistinct))
				}
			}
		}
	}
	if edge.rightTable < len(g.tables) && g.tables[edge.rightTable] != nil {
		tbl := g.tables[edge.rightTable]
		if tbl.Stats != nil {
			for _, cs := range tbl.Stats.Columns {
				if cs.NDistinct > 0 {
					ndv = max(ndv, int64(cs.NDistinct))
				}
			}
		}
	}
	product := leftRows * rightRows
	cost := product / ndv
	if cost < 1 {
		cost = 1
	}
	return cost
}

func buildJoinFromDP(leftPlan, rightPlan Node, a, b uint16, edge *joinEdge, g *joinGraph) *Join {
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
	leftKey := remapKeyToSubset(lk, a, g)
	rightKey := remapKeyToSubset(rk, b, g)

	leftSchema := leftPlan.Output()
	rightSchema := rightPlan.Output()
	mergedSchema := make(Schema, len(leftSchema)+len(rightSchema))
	copy(mergedSchema, leftSchema)
	copy(mergedSchema[len(leftSchema):], rightSchema)

	lRows := EstimateRows(leftPlan)
	rRows := EstimateRows(rightPlan)
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

	return &Join{
		pos:       0,
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      leftPlan,
		Right:     rightPlan,
		Predicate: &BinaryOp{pos: 0, Op: "=", Left: leftKey, Right: rightKey},
		LeftKey:   leftKey,
		RightKey:  rightKey,
		BuildLeft: buildLeft,
		schema:    mergedSchema,
	}
}

// remapKeyToSubset adjusts ColumnRef indices from the global merged
// schema to the per-subset local schema.
func remapKeyToSubset(key Expr, subset uint16, g *joinGraph) Expr {
	if col, ok := key.(*ColumnRef); ok {
		cl := *col
		offset := int32(0)
		for i := 0; i < g.nodes; i++ {
			w := int32(g.scanWidth[i])
			if subset&(1<<i) != 0 {
				if cl.Index >= int(offset) && cl.Index < int(offset+w) {
					newOff := int32(0)
					for j := 0; j < i; j++ {
						if subset&(1<<j) != 0 {
							newOff += int32(g.scanWidth[j])
						}
					}
					cl.Index = int(newOff + (int32(cl.Index) - offset))
					return &cl
				}
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
	if !ok || bin.Op != "=" {
		return false
	}
	return bin.Left == leftKey && bin.Right == rightKey
}

// extraInScans reports whether every ColumnRef in c references a
// column name that appears in the output schema of at least one
// scan in scans. Used to validate that an MHJ.Filters extra
// belongs to the MHJ's subset before capturing it.
func extraInScans(c Expr, scans []Node) bool {
	allMatched := true
	visitColumnRefsForTable(c, func(idx int) {})
	visitColumnRefsByName(c, func(name string) {
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
	return allMatched
}

// visitColumnRefsByName invokes fn on each ColumnRef Name in e.
func visitColumnRefsByName(e Expr, fn func(string)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		if x.Name != "" {
			fn(x.Name)
		}
	case *BinaryOp:
		visitColumnRefsByName(x.Left, fn)
		visitColumnRefsByName(x.Right, fn)
	case *UnaryOp:
		visitColumnRefsByName(x.Operand, fn)
	case *FuncCall:
		for _, a := range x.Args {
			visitColumnRefsByName(a, fn)
		}
	case *ExtractExpr:
		visitColumnRefsByName(x.Source, fn)
	case *CaseExpr:
		if x.Operand != nil {
			visitColumnRefsByName(x.Operand, fn)
		}
		for _, w := range x.Whens {
			visitColumnRefsByName(w.When, fn)
			visitColumnRefsByName(w.Then, fn)
		}
		if x.Else != nil {
			visitColumnRefsByName(x.Else, fn)
		}
	case *InExpr:
		visitColumnRefsByName(x.Operand, fn)
	}
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
			}
		}
		subRemap(n.GroupExprs)
		for i := range n.Aggs {
			if n.Aggs[i].Arg != nil {
				subRemap([]Expr{n.Aggs[i].Arg})
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

// remapColumnRefs walks an expression tree and updates any
// ColumnRef.Index using a position map built from the
// MultiHashJoin's table OIDs.  This correctly handles duplicate
// column names across different table instances (e.g. two
// nation tables each with n_name).
func remapByPosMap(e *Expr, posMap func(int) int) {
	if e == nil || *e == nil {
		return
	}
	switch x := (*e).(type) {
	case *ColumnRef:
		newIdx := posMap(x.Index)
		if newIdx != x.Index {
			cl := *x
			cl.Index = newIdx
			*e = &cl
		}
	case *BinaryOp:
		remapByPosMap(&x.Left, posMap)
		remapByPosMap(&x.Right, posMap)
	case *UnaryOp:
		remapByPosMap(&x.Operand, posMap)
	case *FuncCall:
		for i := range x.Args {
			remapByPosMap(&x.Args[i], posMap)
		}
	case *ExtractExpr:
		remapByPosMap(&x.Source, posMap)
	case *InExpr:
		// Remap the probe operand; do NOT remap the inner Plan (already remapped).
		remapByPosMap(&x.Operand, posMap)
	case *CaseExpr:
		if x.Operand != nil {
			remapByPosMap(&x.Operand, posMap)
		}
		for i := range x.Whens {
			remapByPosMap(&x.Whens[i].When, posMap)
			remapByPosMap(&x.Whens[i].Then, posMap)
		}
		if x.Else != nil {
			remapByPosMap(&x.Else, posMap)
		}
	}
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
	var collect func(Node)
	collect = func(n Node) {
		if n == nil {
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
			for _, t := range x.Tables {
				switch s := t.(type) {
				case *SeqScan:
					entries = append(entries, scanEntry{key: scanKey{table: s.Table, alias: s.Alias}, off: off})
					off += len(s.Output())
				case *IndexScan:
					// M0062-0002: same alias preservation as the
					// top-level IndexScan arm.
					entries = append(entries, scanEntry{key: scanKey{table: s.Table, alias: s.Alias}, off: off})
					off += len(s.Output())
				}
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
			// M0063-0001: SubqueryAlias-style Projects (view
			// rename wrapper) bound an isolated subquery scope.
			// Advance `off` by the projected schema width but do
			// NOT recurse into the Child — its scans are inner-
			// scope and must not enter the outer FROM-bindings
			// scanMap.
			if x.IsolatedScope {
				off += len(x.Output())
				return
			}
			collect(x.Child)
		case *Sort:
			collect(x.Child)
		case *Aggregate:
			collect(x.Child)
		}
	}
	collect(node)
	if len(entries) == 0 {
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
		applyJoinTreePosMap(n.Child, posMap)
		remapByPosMap(&n.Predicate, posMap)
	case *Project:
		// M0063-0001: SubqueryAlias-style Projects (view rename
		// wrapper) are isolated subquery scopes. Don't recurse.
		if n.IsolatedScope {
			return
		}
		applyJoinTreePosMap(n.Child, posMap)
		for i := range n.Targets {
			remapByPosMap(&n.Targets[i], posMap)
		}
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
	hit := -1
	for i, c := range schema {
		if c.Name == name {
			if hit >= 0 {
				return -1 // duplicate
			}
			hit = i + offset
		}
	}
	return hit
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
func reresolveJoinByName(j *Join) {
	if j == nil {
		return
	}
	leftSchema := j.Left.Output()
	rightSchema := j.Right.Output()
	leftWidth := len(leftSchema)
	// Refresh cached merged schema.
	merged := make(Schema, leftWidth+len(rightSchema))
	copy(merged, leftSchema)
	copy(merged[leftWidth:], rightSchema)
	j.schema = merged

	findUnique := findUniqueColumnIndex

	rebind := func(e Expr, leftSide bool) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		var newIdx int
		if leftSide {
			newIdx = findUnique(leftSchema, cr.Name, 0)
		} else {
			newIdx = findUnique(rightSchema, cr.Name, leftWidth)
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
	predRebind := func(e Expr) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		tryLeftFirst := cr.Index < leftWidth
		if tryLeftFirst {
			if newIdx := findUnique(leftSchema, cr.Name, 0); newIdx >= 0 {
				cr.Index = newIdx
				return
			}
			if newIdx := findUnique(rightSchema, cr.Name, leftWidth); newIdx >= 0 {
				cr.Index = newIdx
				return
			}
		} else {
			if newIdx := findUnique(rightSchema, cr.Name, leftWidth); newIdx >= 0 {
				cr.Index = newIdx
				return
			}
			if newIdx := findUnique(leftSchema, cr.Name, 0); newIdx >= 0 {
				cr.Index = newIdx
				return
			}
		}
	}
	rebind(j.LeftKey, true)
	rebind(j.RightKey, false)
	visitColumnRefs(j.Predicate, predRebind)
}

// visitColumnRefs invokes fn on every ColumnRef (and OuterColumnRef
// via type fallthrough — left out: outer refs reach a different
// scope) found in the expression tree, including arms of CaseExpr
// and arguments of FuncCall, BinaryOp, UnaryOp, ExtractExpr.
func visitColumnRefs(e Expr, fn func(Expr)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		fn(x)
	case *BinaryOp:
		visitColumnRefs(x.Left, fn)
		visitColumnRefs(x.Right, fn)
	case *UnaryOp:
		visitColumnRefs(x.Operand, fn)
	case *FuncCall:
		for _, a := range x.Args {
			visitColumnRefs(a, fn)
		}
	case *ExtractExpr:
		visitColumnRefs(x.Source, fn)
	case *CaseExpr:
		if x.Operand != nil {
			visitColumnRefs(x.Operand, fn)
		}
		for _, w := range x.Whens {
			visitColumnRefs(w.When, fn)
			visitColumnRefs(w.Then, fn)
		}
		if x.Else != nil {
			visitColumnRefs(x.Else, fn)
		}
	case *InExpr:
		visitColumnRefs(x.Operand, fn)
	}
}
