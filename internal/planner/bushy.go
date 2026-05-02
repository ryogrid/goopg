package planner

import (
	"math/bits"

	"github.com/goopg/goopg/internal/catalog"
)

// joinEdge is one equijoin predicate between two FROM tables.
type joinEdge struct {
	leftTable  int   // index into the FROM list
	rightTable int
	predicate  Expr  // the BinaryOp("=") expression
	leftKey    Expr  // left-hand side key expression
	rightKey   Expr  // right-hand side key expression
}

// joinGraph is an undirected graph where nodes are FROM tables and
// edges are equijoin predicates from the WHERE clause.
type joinGraph struct {
	nodes     int
	tables    []*catalog.Table
	edges     []joinEdge
	scans     []Node      // SeqScan nodes, index = FROM position
	scanWidth []int       // per-table output schema width
	bindings  []rangeBinding
	mask      uint16      // all-nodes mask for this component
}

// tryBushyDP checks if the bushy join DP is applicable and runs it.
// On success, returns (newPlan, residualPredicate) where residualPredicate
// is the Filter predicate with consumed equalities removed (may be nil).
// On failure, returns (originalNode, originalPred) unchanged.
func tryBushyDP(node Node, pred Expr, ctx *resolveContext, cat catalog.Catalog) (Node, Expr) {
	if ctx == nil || len(ctx.bindings) < 3 {
		return node, pred
	}
	// Check all tables have stats.
	tables := make([]*catalog.Table, len(ctx.bindings))
	for i, b := range ctx.bindings {
		if b.table == nil || b.table.Stats == nil || b.table.Stats.RowCount <= 0 {
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

	for _, c := range conjuncts {
		bin, ok := c.(*BinaryOp)
		if !ok || bin.Op != "=" {
			continue
		}
		li := tableForCol(bin.Left, cumOffsets)
		ri := tableForCol(bin.Right, cumOffsets)
		if li < 0 || ri < 0 || li == ri {
			continue
		}
		g.edges = append(g.edges, joinEdge{
			leftTable:  li,
			rightTable: ri,
			predicate:  bin,
			leftKey:    bin.Left,
			rightKey:   bin.Right,
		})
	}
	return g
}

// tableForCol returns the FROM-table index that all ColumnRef nodes
// in e belong to, or -1 if columns span multiple tables.
func tableForCol(e Expr, cumOffsets []int) int {
	result := -1
	walkColumnRefsForTable(e, func(colIdx int) {
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

// walkColumnRefsForTable is a local helper that visits ColumnRef
// nodes in an expression tree. Mirrors pushdown.go's walkColumnRefs
// but uses planner Expr instead of parser Expr.
func walkColumnRefsForTable(e Expr, onIdx func(int)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		onIdx(x.Index)
	case *OuterColumnRef, *SubqueryExpr, *InExpr, *ExistsExpr:
		// outer refs and subqueries → out of scope
	case *BinaryOp:
		walkColumnRefsForTable(x.Left, onIdx)
		walkColumnRefsForTable(x.Right, onIdx)
	case *UnaryOp:
		walkColumnRefsForTable(x.Operand, onIdx)
	case *FuncCall:
		for _, a := range x.Args {
			walkColumnRefsForTable(a, onIdx)
		}
	case *CaseExpr:
		walkColumnRefsForTable(x.Operand, onIdx)
		for _, w := range x.Whens {
			walkColumnRefsForTable(w.When, onIdx)
			walkColumnRefsForTable(w.Then, onIdx)
		}
		walkColumnRefsForTable(x.Else, onIdx)
	case *ExtractExpr:
		walkColumnRefsForTable(x.Source, onIdx)
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
	for i := range g.edges {
		e := &g.edges[i]
		ma := uint16(1 << e.leftTable)
		mb := uint16(1 << e.rightTable)
		if (a&ma != 0 && b&mb != 0) || (a&mb != 0 && b&ma != 0) {
			return e
		}
	}
	return nil
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
			enumerateSplits(mask, func(a, b uint16) {
				if !isConnectedMask(a, g) || !isConnectedMask(b, g) {
					return
				}
				if !hasCrossEdge(a, b, g) {
					return
				}
				edge := findEdgeBetween(a, b, g)
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
				}
			})
			if best != nil {
				dp[mask] = *best
				markEdgesInMask(mask, g, edgeUsed)
			}
		}
	}

	fullEntry, ok := dp[g.mask]
	if !ok {
		return nil, conjuncts, nil
	}

	// Build residual conjuncts.
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
			if e.leftTable == li && e.rightTable == ri && edgeUsed[i] {
				used = true
				break
			}
			if e.leftTable == ri && e.rightTable == li && edgeUsed[i] {
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
	// Remap keys from global indices to subset-local indices.
	leftKey := remapKeyToSubset(edge.leftKey, a, g)
	rightKey := remapKeyToSubset(edge.rightKey, b, g)
	// If the edge's left table is in subset b and right in a, swap.
	if a&(1<<edge.leftTable) == 0 {
		leftKey, rightKey = rightKey, leftKey
	}

	leftSchema := leftPlan.Output()
	rightSchema := rightPlan.Output()
	mergedSchema := make(Schema, len(leftSchema)+len(rightSchema))
	copy(mergedSchema, leftSchema)
	copy(mergedSchema[len(leftSchema):], rightSchema)

	lRows := EstimateRows(leftPlan)
	rRows := EstimateRows(rightPlan)
	buildLeft := lRows > 0 && rRows > 0 && lRows < rRows

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
			if subset&(1<<i) == 0 {
				continue
			}
			w := int32(g.scanWidth[i])
			if cl.Index >= int(offset) && cl.Index < int(offset+w) {
				// Found the table in this subset.
				// Compute new index within subset.
				newOff := int32(0)
				for j := 0; j < i; j++ {
					if subset&(1<<j) != 0 {
						newOff += int32(g.scanWidth[j])
					}
				}
				cl.Index = int(newOff + (int32(cl.Index) - offset))
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
// order and the join keys for each edge, or nil if the tree is not
// a valid chain (≥3 tables, all JoinAlgoHash, all inner, chained).
func collectMultiHashTables(node Node) ([]Node, []MultiHashKey, int) {
	var scans []Node
	var keys []MultiHashKey

	// Map from SeqScan node pointer to table index.
	scanIdx := make(map[Node]int)

	var walk func(n Node) bool
	walk = func(n Node) bool {
		if s, ok := n.(*SeqScan); ok {
			idx := len(scans)
			scans = append(scans, s)
			scanIdx[n] = idx
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
		if !walk(j.Left) || !walk(j.Right) {
			return false
		}
		// Record the equijoin edge between left and right.
		li := scanIdx[j.Left]
		ri := scanIdx[j.Right]
		if li < len(scans) && ri < len(scans) {
			key := MultiHashKey{
				LeftTable: li, LeftCol: 0,
				RightTable: ri, RightCol: 0,
			}
			// Find correct column indices from the expression.
			if lk, ok := j.LeftKey.(*ColumnRef); ok {
				key.LeftCol = lk.Index
			}
			if rk, ok := j.RightKey.(*ColumnRef); ok {
				key.RightCol = rk.Index
			}
			keys = append(keys, key)
		}
		return true
	}
	if !walk(node) || len(scans) < 3 {
		return nil, nil, 0
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
	return scans, keys, probeIdx
}

// rewriteMultiWayChain walks the plan tree and replaces chains of
// ≥3 hash-joined tables with MultiHashJoin nodes.
func rewriteMultiWayChain(node Node) Node {
	if node == nil {
		return nil
	}
	scans, keys, probeIdx := collectMultiHashTables(node)
	if scans == nil {
		// Not a valid chain — recurse into children.
		switch n := node.(type) {
		case *Join:
			n.Left = rewriteMultiWayChain(n.Left)
			n.Right = rewriteMultiWayChain(n.Right)
		case *Filter:
			n.Child = rewriteMultiWayChain(n.Child)
		case *Project:
			n.Child = rewriteMultiWayChain(n.Child)
		case *Sort:
			n.Child = rewriteMultiWayChain(n.Child)
		case *Aggregate:
			n.Child = rewriteMultiWayChain(n.Child)
		}
		return node
	}

	// Build MultiHashJoin node.
	mh := &MultiHashJoin{
		pos:        node.Pos(),
		Tables:     scans,
		Keys:       keys,
		ProbeTable: probeIdx,
		Filters:    nil,
	}
	// Build output schema from all tables.
	fullSchema := make(Schema, 0)
	for _, s := range scans {
		fullSchema = append(fullSchema, s.Output()...)
	}
	mh.schema = fullSchema
	return mh
}
