package planner

import (
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
// order and the join keys for each edge, or nil if the tree is not
// a valid chain (≥3 tables, all JoinAlgoHash, all inner, chained).
func collectMultiHashTables(node Node) ([]Node, []MultiHashKey, int) {
	var scans []Node
	var keys []MultiHashKey

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
		return nil, nil, 0
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
// ≥3 hash-joined tables with MultiHashJoin nodes.
func rewriteMultiWayChain(node Node) Node {
	if node == nil {
		return nil
	}
	scans, keys, probeIdx := collectMultiHashTables(node)
	if scans == nil {
		// Not a valid chain — recurse into children.
		// Only recurse into Join (binary ops) and thin wrappers
		// (Filter, Project, Sort). Do NOT recurse into Aggregate:
		// crossing a plan-phase boundary mixes table scopes.
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
		remapPosMapAfterRewrite(n.Right, nil)
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

// mhjPosMapOf returns a position map (old FROM‑order → new MHJ
// DFS‑order) if the subtree contains a MultiHashJoin, or nil.
func mhjPosMapOf(node Node) func(int) int {
	for {
		if node == nil {
			return nil
		}
		if mh, ok := node.(*MultiHashJoin); ok {
			return buildMHJPosMap(mh)
		}
		switch n := node.(type) {
		case *Filter:
			node = n.Child
		case *Project:
			node = n.Child
		case *Sort:
			node = n.Child
		case *Aggregate:
			node = n.Child
		default:
			return nil
		}
	}
}

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
			entries = append(entries, scanEntry{key: scanKey{table: x.Table, alias: ""}, off: off})
			off += len(x.Output())
		case *MultiHashJoin:
			for _, t := range x.Tables {
				if s, ok := t.(*SeqScan); ok {
					entries = append(entries, scanEntry{key: scanKey{table: s.Table, alias: s.Alias}, off: off})
					off += len(s.Output())
				}
			}
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
		return // tables/keys already in output order
	case *Join:
		applyJoinTreePosMap(n.Left, posMap)
		applyJoinTreePosMap(n.Right, posMap)
		// Join.Predicate / LeftKey / RightKey were built with per‑
		// subset indices by buildJoinFromDP — do NOT remap them.
	case *Filter:
		applyJoinTreePosMap(n.Child, posMap)
		remapByPosMap(&n.Predicate, posMap)
	case *Project:
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
