package optimizer

import (
	"math/bits"
	"sort"

	"github.com/goopg/goopg/internal/catalog"
)

// Partitionwise join (M0148-0001c3a9 slice 3).
//
// try_partitionwise_join (joinrels.c) joins two partitioned rels partition
// by partition when their schemes match, their bounds are equal and the join
// equates every partition key: partition i of one side can only join rows of
// partition i of the other, so the join is the Append of the n child joins.
// Each child join is an ordinary joinrel of the two child rels, given every
// join path the parent pair would get (populate_joinrel_with_paths), and
// generate_partitionwise_join_paths (allpaths.c) adds an Append over the
// cheapest child joins to the parent joinrel, where it competes in add_path
// with the whole-rel joins.
//
// goopg's appendrel is a pre-built UNION ALL leaf. A member's output layout
// is the appendrel's own (the parent's columns, then tableoid), and the
// search's clauses address a leaf by its pre-search offset, so a child rel is
// the parent rel with its leaf swapped for member i: the same relids, offset
// and clauses, the member's own rows and scan cost. Every child join then
// goes through the same addPaths the parent pair used.
//
// Scope: an inner join (no SpecialJoinInfo) of two base appendrels of
// one-level partitioned tables (partScheme), whose bounds are equal. A child
// rel carries only its sequential scan (no index, bitmap or partial paths
// yet), and outer / semi / anti joins, N-way partitionwise joins (a child
// join joined again) and partition_bounds_merge are later slices.

// tryPartitionwiseJoin adds the partitionwise Append to joinrel when rel1 ⋈
// rel2 qualifies.
func (s *searchCtx) tryPartitionwiseJoin(joinrel, rel1, rel2 *RelOptInfo, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) error {
	if s == nil || !s.cp.enablePartitionwiseJoin || sjinfo != nil || s.builder == nil {
		return nil
	}
	ps1, ps2 := relPartScheme(rel1), relPartScheme(rel2)
	if ps1 == nil || ps2 == nil || !partitionBoundsEqual(ps1, ps2) {
		return nil
	}
	i1, ok1 := s.baseRelIndex(rel1)
	i2, ok2 := s.baseRelIndex(rel2)
	if !ok1 || !ok2 {
		return nil
	}
	if !partkeyEquiJoined(ps1, ps2, s.relInfos[i1].sourceIdx, s.relInfos[i2].sourceIdx, clauses) {
		return nil
	}
	so1, f1 := appendRelLeaf(rel1)
	so2, f2 := appendRelLeaf(rel2)
	m1 := unionAllMembers(so1)
	m2 := unionAllMembers(so2)
	if len(m1) != len(ps1.parts) || len(m2) != len(ps2.parts) {
		return nil
	}
	children := make([]*Path, 0, len(m1))
	fills := make([]map[int]Expr, 0, len(m1))
	for i := range m1 {
		c1, fill1 := s.appendChildRel(rel1, m1[i], f1, so1.Output(), s.relInfos[i1], ps1, i)
		c2, fill2 := s.appendChildRel(rel2, m2[i], f2, so2.Output(), s.relInfos[i2], ps2, i)
		if c1 == nil || c2 == nil {
			return nil
		}
		fill := map[int]Expr{}
		for k, v := range fill1 {
			fill[k] = v
		}
		for k, v := range fill2 {
			fill[k] = v
		}
		fills = append(fills, fill)
		rows, width := s.builder.sizeJoinRel(c1, c2, clauses, nil)
		if !(rows >= 1) {
			rows = 1
		}
		child := newRelOptInfo(joinrel.Relids, rows, width)
		child.NCols = joinrel.NCols
		child.AvgVarBytes = joinrel.AvgVarBytes
		child.ConsiderStartup = joinrel.ConsiderStartup
		child.NeededCols, child.NeededColsKnown = joinrel.NeededCols, joinrel.NeededColsKnown
		child.OutputCols, child.OutputColsKnown = joinrel.OutputCols, joinrel.OutputColsKnown
		child.memberRels = []*RelOptInfo{c1, c2}
		child.publishedMembers = []*RelOptInfo{c1, c2}
		child.colVarFrom = [2]*RelOptInfo{c1, c2}
		child.colVarPending = true
		child.joinNarrow = joinrel.joinNarrow
		if err := s.builder.addPaths(child, c1, c2, clauses, nil); err != nil {
			return err
		}
		if err := s.builder.addPaths(child, c2, c1, clauses, nil); err != nil {
			return err
		}
		setCheapest(child)
		best := cheapestUnparameterized(child)
		if best == nil {
			return nil
		}
		children = append(children, best)
	}
	pa := newPartitionwiseAppendPath(joinrel, children, s.cp)
	pa.pwParents = []*SetOp{so1, so2}
	pa.pwFills = fills
	addPath(joinrel, pa, "partitionwise-join")
	return nil
}

// baseRelIndex is the search position of a base rel (its one relid bit).
func (s *searchCtx) baseRelIndex(rel *RelOptInfo) (int, bool) {
	if rel == nil || rel.baseLeaf == nil || bits.OnesCount32(uint32(rel.Relids)) != 1 {
		return 0, false
	}
	i := bits.TrailingZeros32(uint32(rel.Relids))
	if i >= len(s.relInfos) {
		return 0, false
	}
	return i, true
}

// appendChildRel is the child rel of partition i: parent's relids, offset and
// width, the member leaf, and that leaf's rows and sequential-scan cost
// (set_append_rel_size / set_rel_pathlist on the child).
func (s *searchCtx) appendChildRel(parent *RelOptInfo, member Node, restrict *Filter, out Schema, pinfo baseRelInfo, ps *partScheme, i int) (*RelOptInfo, map[int]Expr) {
	if _, isScan := leafBaseScan(memberScanRoot(member)).(*SeqScan); !isScan {
		return nil, nil
	}
	if restrict != nil && restrict.Predicate != nil {
		// The parent's restriction, given to the child as its own
		// (adjust_appendrel_attrs over baserestrictinfo): the member chain is
		// copied so the parent's shared member nodes stay untouched, then the
		// translatable conjuncts move onto the member scan as the finished
		// plan's pushdown does (appendrel_pushdown.go). A conjunct that does
		// not translate stays above the member.
		member = cloneMemberChain(member)
		m, ok := appendRelMemberOf(member)
		if !ok {
			return nil, nil
		}
		f := &Filter{pos: restrict.pos, Child: member, Predicate: restrict.Predicate, LeafLocal: restrict.LeafLocal}
		pushFilterIntoMembers(f, out, []appendRelMember{m})
		if bc, isTrue := f.Predicate.(*BooleanConst); !isTrue || !bc.Value {
			member = f
		}
	}
	tbl := ps.parts[i]
	// A member whose only projection is the tableoid wrapper is planned on
	// its bare `[Filter →] scan`, as PG plans a child rel on the child
	// table itself: every base-rel scan producer (index, parameterised
	// index) then applies unchanged, and the tableoid column — the one
	// output the scan does not carry — is filled with the partition's OID
	// above the child join (createPartitionwiseAppendPlan).
	var fill map[int]Expr
	if top, ok := member.(*Project); ok && isTableoidWrapper(top) {
		scanOut := top.Child.Output()
		fill = map[int]Expr{parent.baseOffset + len(scanOut): top.Targets[len(scanOut)]}
		member = top.Child
	}
	rows := float64(EstimateRows(member))
	if !(rows >= 1) {
		rows = 1
	}
	rel := newRelOptInfo(parent.Relids, rows, parent.Width)
	rel.NCols = parent.NCols
	rel.AvgVarBytes = parent.AvgVarBytes
	rel.ConsiderStartup = parent.ConsiderStartup
	rel.NeededCols, rel.NeededColsKnown = parent.NeededCols, parent.NeededColsKnown
	rel.OutputCols, rel.OutputColsKnown = parent.OutputCols, parent.OutputColsKnown
	rel.baseLeaf = member
	rel.baseOffset = parent.baseOffset
	cinfo := pinfo
	cinfo.table = tbl
	cinfo.appendrel = false
	cinfo.baseRows = tableRows(tbl)
	cinfo.localFilter = memberLocalFilter(member)
	cinfo.hasLocalFilter = cinfo.localFilter != nil
	p := newPrebuiltPath(rel, member)
	pages, tuples, qualOps := baseSeqScanCostInputs(cinfo, memberScanRoot(member), rows, parent.Width)
	p.Cost = costSeqscan(s.cp, pages, tuples, qualOps)
	p.DisabledNodes = prebuiltLeafDisabledNodes(s.cp, memberScanRoot(member))
	addPath(rel, p, "partitionwise-join.child")
	if fill != nil {
		rel.NCols = len(member.Output())
		s.addChildIndexPaths(rel, tbl)
	}
	setCheapest(rel)
	return rel, fill
}

// addChildIndexPaths runs the base-rel index producers over a child rel
// whose leaf is the partition's bare scan (create_index_paths on the child
// in set_append_rel_pathlist): the restriction and ordered index paths, and
// the parameterised index paths a nested loop over the child join binds
// (PG's partition_join plans are full of `Index Cond: (a = t1_1.a)` inner
// probes per partition). The index-only arm is not run: its coverage test
// reads the statement-wide needed-column set (ledgered).
func (s *searchCtx) addChildIndexPaths(rel *RelOptInfo, tbl *catalog.Table) {
	if s.cat == nil || tbl == nil {
		return
	}
	if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
		return
	}
	relTuples := float64(tableRows(tbl))
	if relTuples < 1 {
		relTuples = 1
	}
	relPages := baseRelPages(tbl, relTuples)
	totalPages := s.totalTablePages()
	conjuncts := extractFilterConjuncts(rel.baseLeaf)
	var colExprs map[string]Expr
	if s.hasUsefulPathkeys(rel) {
		colExprs = mergeableColumnExprsFor(rel.Relids, s.clausesAll())
		if i, ok := s.baseRelIndex2(rel.Relids); ok {
			addQueryPathkeyColumnExprs(colExprs, s.queryPathkeys, s.relInfos[i].sourceIdx)
		}
		addConstEquatedColumns(colExprs, rel)
	}
	var cands []paramIndexClause
	if s.clauses != nil {
		cands = indexableJoinClausesFor(rel.Relids, s.clauses.all)
	}
	saved := s.neededColsKnown
	s.neededColsKnown = false
	defer func() { s.neededColsKnown = saved }()
	for _, idx := range s.cat.IndexesOnTable(tbl) {
		if !idx.HasStorage() {
			continue
		}
		if len(conjuncts) > 0 {
			s.addOneRestrictionIndexPath(s.cat, rel, tbl, idx, conjuncts, colExprs, relPages, relTuples, totalPages)
		}
		if len(colExprs) > 0 {
			s.addOneOrderedIndexPath(rel, tbl, idx, colExprs, relPages, relTuples, totalPages)
		}
	}
	for _, req := range consideredParameterizations(cands) {
		s.addOneParameterizedIndexPath(rel, tbl, s.cat, cands, req, relPages, relTuples, totalPages)
	}
}

// baseRelIndex2 is the search position of the base rel with relids.
func (s *searchCtx) baseRelIndex2(relids RelSet) (int, bool) {
	if bits.OnesCount32(uint32(relids)) != 1 {
		return 0, false
	}
	i := bits.TrailingZeros32(uint32(relids))
	return i, i < len(s.relInfos)
}

// isTableoidWrapper reports whether p is wrapWithTableoid's Project: its
// child's columns in order, then one constant.
func isTableoidWrapper(p *Project) bool {
	in := p.Child.Output()
	if len(p.Targets) != len(in)+1 {
		return false
	}
	for i := range in {
		cr, ok := p.Targets[i].(*ColumnRef)
		if !ok || cr.Index != i {
			return false
		}
	}
	_, ok := p.Targets[len(in)].(*IntegerConst)
	return ok
}

// memberScanRoot peels an appendrel member's projections (the tableoid
// wrapper and a column remap) down to its `[Filter →] scan`.
func memberScanRoot(n Node) Node {
	for {
		p, ok := n.(*Project)
		if !ok {
			return n
		}
		n = p.Child
	}
}

// memberLocalFilter is the restriction a member scan carries (the parent's
// WHERE quals moved onto it, appendrel_pushdown.go).
func memberLocalFilter(n Node) Expr {
	if f, ok := memberScanRoot(n).(*Filter); ok {
		return f.Predicate
	}
	return nil
}

// cheapestUnparameterized is the cheapest-total path of rel that needs no
// outer parameter.
func cheapestUnparameterized(rel *RelOptInfo) *Path {
	var best *Path
	for _, p := range rel.Pathlist {
		if p == nil || p.RequiredOuter != 0 {
			continue
		}
		if best == nil || p.DisabledNodes < best.DisabledNodes ||
			(p.DisabledNodes == best.DisabledNodes && p.Cost.Total < best.Cost.Total) {
			best = p
		}
	}
	return best
}

// newPartitionwiseAppendPath is create_append_path over the child joins,
// costed as cost_append's unordered arm (costsize.c): the first child's
// startup, the sum of the children's totals and rows, plus cpu_tuple_cost *
// APPEND_CPU_COST_MULTIPLIER per row.
func newPartitionwiseAppendPath(joinrel *RelOptInfo, children []*Path, cp costParams) *Path {
	p := &Path{Kind: PathPartitionwiseAppend, Rel: joinrel, Children: children}
	var rows, total float64
	disabled := 0
	for i, c := range children {
		if i == 0 {
			p.Cost.Startup = c.Cost.Startup
		}
		total += c.Cost.Total
		rows += c.Rows
		disabled += c.DisabledNodes
	}
	p.Rows = rows
	p.Cost.Total = total + cp.cpuTupleCost*appendCPUCostMultiplier*rows
	p.DisabledNodes = disabled
	return p
}

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
		root = &SetOp{Left: root, Right: n, All: true}
	}
	if so, ok := root.(*SetOp); ok {
		so.PartitionwiseParents = p.pwParents
	}
	return root, lay
}

// reorderToLayout makes a child join emit want's column order. Each child
// join elects its own inner and outer, and a join emits outer++inner, so two
// children can emit the same columns in different orders; an Append needs
// one row shape. PG builds every child join's target list in the parent's
// order (build_child_join_rel / adjust_appendrel_attrs); goopg projects a
// child whose layout differs. A layout that is not a permutation of want
// cannot happen for one relset (same relids, same needed columns); it is
// returned unchanged.
func reorderToLayout(n Node, have, want outputLayout, fill map[int]Expr, fillSchema map[int]SchemaColumn) Node {
	if len(have) == len(want) && len(fill) == 0 {
		same := true
		for i := range have {
			if have[i] != want[i] {
				same = false
				break
			}
		}
		if same {
			return n
		}
	}
	at := have.bindingIndex()
	out := n.Output()
	targets := make([]Expr, len(want))
	schema := make(Schema, len(want))
	for j, coord := range want {
		if i, ok := at[coord]; ok && i < len(out) {
			col := out[i]
			targets[j] = &ColumnRef{Index: i, Name: col.Name, Type: col.Type, SourceTableIdx: col.SourceTableIdx}
			schema[j] = col
			continue
		}
		e, ok := fill[coord]
		sc, sok := fillSchema[coord]
		if !ok || !sok {
			// Cannot happen for one relset (same relids, same columns):
			// the plan would be malformed, so fail loudly.
			panic("partitionwise join: child join lacks a column of the parent join's layout")
		}
		targets[j] = e
		schema[j] = sc
	}
	return &Project{Child: n, Targets: targets, schema: schema}
}

// appendRelLeaf is a rel's partitioned appendrel and the restriction Filter
// above it, if any.
func appendRelLeaf(rel *RelOptInfo) (*SetOp, *Filter) {
	if rel == nil {
		return nil, nil
	}
	var f *Filter
	n := rel.baseLeaf
	if x, ok := n.(*Filter); ok {
		f, n = x, x.Child
	}
	so, _ := n.(*SetOp)
	return so, f
}

// cloneMemberChain copies a member's Project chain (the nodes a pushed
// restriction rewires), sharing the scan below it.
func cloneMemberChain(n Node) Node {
	p, ok := n.(*Project)
	if !ok {
		return n
	}
	cp := *p
	cp.Child = cloneMemberChain(p.Child)
	return &cp
}

// pwFillSchema is the output column for each filled (tableoid) coordinate,
// taken from the parent appendrels' own schemas.
func (p *Path) pwFillSchema() map[int]SchemaColumn {
	out := map[int]SchemaColumn{}
	if p.Rel == nil {
		return out
	}
	for _, rel := range p.Rel.baseMembers() {
		so, _ := appendRelLeaf(rel)
		if so == nil {
			continue
		}
		pout := so.Output()
		for j, c := range pout {
			out[rel.baseOffset+j] = c
		}
	}
	return out
}
