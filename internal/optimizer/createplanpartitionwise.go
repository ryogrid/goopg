package optimizer

import "sort"

// The PathPartitionwiseAppend lowering arm (M0148-0001c3a9, c3a10): the
// Append — or, for a pwMerge path, the Merge Append — over the child plans of
// a partitionwise join or a partitioned table's child rels. It lives in a
// createplan file because it is part of createPlanNode's walk: the merge keys
// are translated against the layout the children return
// (TestTranslateToLayoutIsLoweringLocal).

// createPartitionwiseAppendPlan is the Append over the child join plans, as
// the UNION ALL chain goopg's appendrels use. Every child join emits the
// parent join's layout, so the first child's layout is the Append's.
func createPartitionwiseAppendPlan(p *Path) (Node, outputLayout) {
	// Every child join is planned first: each elects its own inner and
	// outer and may narrow its output to what the parent needs, so the
	// children can publish different column subsets in different orders.
	// The Append's layout is the coordinates EVERY child provides (a
	// column some child dropped is not needed above the join — narrowing
	// only drops unneeded columns) plus the tableoid columns a bare child
	// scan fills, in binding order, as PG's child join target lists follow
	// the parent joinrel's. Each child is then projected to exactly that.
	nodes := make([]Node, len(p.Children))
	lays := make([]outputLayout, len(p.Children))
	count := map[int]int{}
	for i, c := range p.Children {
		nodes[i], lays[i] = createPlanNode(c)
		seen := map[int]bool{}
		for _, coord := range lays[i] {
			seen[coord] = true
		}
		if i < len(p.pwFills) {
			for coord := range p.pwFills[i] {
				seen[coord] = true
			}
		}
		for coord := range seen {
			count[coord]++
		}
	}
	var lay outputLayout
	for coord, n := range count {
		if n == len(p.Children) {
			lay = append(lay, coord)
		}
	}
	sort.Ints(lay)
	schemaFor := p.pwFillSchema()
	var mergeKeys []SortKey
	if p.pwMerge {
		// Slice 5: the merge compares the children's rows on the pathkeys,
		// read from the Append's layout every child is projected to.
		index := lay.bindingIndex()
		for _, pk := range p.Pathkeys {
			e := translateToLayout("merge append key", pk.Expr, lay, index)
			mergeKeys = append(mergeKeys, SortKey{Expr: e, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst})
		}
	}
	var root Node
	for i, n := range nodes {
		var fill map[int]Expr
		if i < len(p.pwFills) {
			fill = p.pwFills[i]
		}
		n = reorderToLayout(n, lays[i], lay, fill, schemaFor)
		if root == nil {
			root = n
			continue
		}
		root = &SetOp{Left: root, Right: n, All: true, MergeKeys: mergeKeys}
	}
	if so, ok := root.(*SetOp); ok {
		so.PartitionwiseParents = p.pwParents
	}
	return root, lay
}
