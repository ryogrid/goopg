package optimizer

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// M0148-0001c3a5 (a): generate_orderedappend_paths' partitions_are_ordered
// arm (allpaths.c) for the ORDERED upper rel. A range-partitioned table whose
// partitions are in bound order (no default partition) delivers its
// partition key's order through a PLAIN Append of per-partition ordered
// scans — no Merge Append, no Sort — when the ORDER BY leads with the
// partition key (build_partition_pathkeys) and every partition has an index
// supplying the whole ORDER BY. A descending ORDER BY reverses the partition
// order and scans each index backward.
//
// The appendrel reaches the upper stage as goopg's inheritance SetOp chain
// whose members are Project → SeqScan leaves (planScanRangeVar); this arm
// rebuilds the chain with each member's SeqScan replaced by an Index (Only)
// Scan over the member index, re-sequenced in bound order, and files it on
// the ORDERED rel for create_ordered_paths to weigh against Sort + Append.

// orderedPartitionAppendProducer labels the path in traces.
const orderedPartitionAppendProducer = "ordered.partition-append"

// partitionMember is one appendrel member: its root (the Project chain) and
// the SeqScan leaf the chain ends at.
type partitionMember struct {
	root Node
	scan *SeqScan
}

// addOrderedPartitionAppendPaths files the ordered plain Append over a range
// partitioned appendrel when its partitions deliver sortPathkeys.
func addOrderedPartitionAppendPaths(ordered *RelOptInfo, input Node, sortPathkeys []PathKey, cp costParams, cat catalog.Catalog) {
	if ordered == nil || input == nil || len(sortPathkeys) == 0 || cat == nil {
		return
	}
	// The input is the SetOp itself, or a column-only Project above it.
	var top *Project
	head, ok := input.(*SetOp)
	if !ok {
		pr, isPr := input.(*Project)
		if !isPr {
			return
		}
		h, isSet := pr.Child.(*SetOp)
		if !isSet {
			return
		}
		top, head = pr, h
	}
	if !isInheritanceAppendRel(head) {
		return
	}
	membersRoots := unionAllMembers(head)
	if len(membersRoots) < 2 {
		return
	}
	members := make([]partitionMember, 0, len(membersRoots))
	for _, m := range membersRoots {
		sc := memberSeqScan(m)
		if sc == nil || sc.Table == nil {
			return
		}
		members = append(members, partitionMember{root: m, scan: sc})
	}
	parent := partitionParentOf(cat, members[0].scan.Table)
	if parent == nil || !strings.EqualFold(parent.PartitionMethod, "RANGE") || len(parent.PartitionKey) == 0 {
		return
	}
	for _, m := range members {
		if m.scan.Table.PartitionParentOID != members[0].scan.Table.PartitionParentOID {
			return // a multi-level hierarchy: not taken
		}
	}

	// ORDER BY keys as the SetOp's output positions (through a top Project
	// that only re-orders or drops columns).
	headOut := head.Output()
	keyPos := make([]int, len(sortPathkeys))
	for i, pk := range sortPathkeys {
		cr, isCol := pk.Expr.(*ColumnRef)
		if !isCol {
			return
		}
		pos := cr.Index
		if top != nil {
			if pos < 0 || pos >= len(top.Targets) {
				return
			}
			tc, isTC := top.Targets[pos].(*ColumnRef)
			if !isTC {
				return
			}
			pos = tc.Index
		}
		if pos < 0 || pos >= len(headOut) {
			return
		}
		keyPos[i] = pos
	}
	// One direction for every key: ASC NULLS LAST is the partitions' and a
	// default btree index's forward order; DESC NULLS FIRST is backward.
	backward := !sortPathkeys[0].SortAsc
	for _, pk := range sortPathkeys {
		if pk.SortAsc == backward || pk.NullsFirst != backward {
			return
		}
	}

	// Per member: the base column each key names, and an index supplying
	// them in order.
	type memberPlan struct {
		member partitionMember
		cols   []string
		idx    *catalog.Index
	}
	plans := make([]memberPlan, 0, len(members))
	for _, m := range members {
		cols := make([]string, len(keyPos))
		for i, p := range keyPos {
			name, ok := memberBaseColumn(m.root, p)
			if !ok {
				return
			}
			cols[i] = name
		}
		// build_partition_pathkeys: the ORDER BY must lead with the partition
		// key (a prefix either way round).
		for i := 0; i < len(cols) && i < len(parent.PartitionKey); i++ {
			if !strings.EqualFold(cols[i], parent.PartitionKey[i]) {
				return
			}
		}
		idx := orderedMemberIndex(cat, m.scan.Table, cols)
		if idx == nil {
			return
		}
		plans = append(plans, memberPlan{member: m, cols: cols, idx: idx})
	}

	// partitions_are_ordered: RANGE with no default partition; the members
	// in lower-bound order.
	bounds := make([][]int64, len(plans))
	for i, mp := range plans {
		b, ok := rangeLowerBound(mp.member.scan.Table, len(parent.PartitionKey))
		if !ok {
			return
		}
		bounds[i] = b
	}
	order := make([]int, len(plans))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return compareBoundTuples(bounds[order[a]], bounds[order[b]]) < 0 })
	if backward {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}

	// Build each member's ordered scan and cost the Append.
	totalPages := 0.0
	for _, mp := range plans {
		totalPages += float64(baseRelPages(mp.member.scan.Table, minmaxRelTuples(cat, mp.member.scan.Table)))
	}
	newRoots := make([]Node, 0, len(plans))
	var startup, total, rows float64
	for _, oi := range order {
		mp := plans[oi]
		scan := orderedMemberScan(cat, cp, mp.member.scan, mp.idx, backward, totalPages)
		pc, _ := scan.(PlanCostCarrier).PlanCostInfo()
		startup += pc.StartupCost
		total += pc.TotalCost
		rows += pc.PlanRows
		newRoots = append(newRoots, replaceMemberLeaf(mp.member.root, scan))
	}
	total += cp.cpuTupleCost * appendCPUCostMultiplier * rows

	chain := rebuildUnionAllChain(head, newRoots)
	var node Node = chain
	if top != nil {
		cpTop := *top
		cpTop.Child = chain
		node = &cpTop
	}
	p := newPrebuiltPath(ordered, node)
	p.Rows = rows
	p.Cost = Cost{Startup: startup, Total: total}
	p.Pathkeys = sortPathkeys
	addPath(ordered, p, orderedPartitionAppendProducer)
}

// memberSeqScan is the SeqScan an appendrel member's Project chain ends at,
// or nil (a member with a Filter or any other node is not taken).
func memberSeqScan(n Node) *SeqScan {
	for depth := 0; depth < 8 && n != nil; depth++ {
		switch x := n.(type) {
		case *Project:
			n = x.Child
		case *SeqScan:
			return x
		default:
			return nil
		}
	}
	return nil
}

// memberBaseColumn follows output position pos of a member's Project chain
// down to the SeqScan's column name; false when some Project computes it.
func memberBaseColumn(n Node, pos int) (string, bool) {
	for depth := 0; depth < 8 && n != nil; depth++ {
		switch x := n.(type) {
		case *Project:
			if pos < 0 || pos >= len(x.Targets) {
				return "", false
			}
			cr, ok := x.Targets[pos].(*ColumnRef)
			if !ok {
				return "", false
			}
			pos, n = cr.Index, x.Child
		case *SeqScan:
			out := x.Output()
			if pos < 0 || pos >= len(out) {
				return "", false
			}
			return out[pos].Name, true
		default:
			return "", false
		}
	}
	return "", false
}

// replaceMemberLeaf copies a member's Project chain over a new leaf.
func replaceMemberLeaf(n Node, leaf Node) Node {
	if pr, ok := n.(*Project); ok {
		cp := *pr
		cp.Child = replaceMemberLeaf(pr.Child, leaf)
		return &cp
	}
	return leaf
}

// rebuildUnionAllChain is head's left-deep UNION ALL chain over members in
// the given order: each link a copy of the original link at that depth (so
// the appendrel's RTID / name stay on the top), costs cleared.
func rebuildUnionAllChain(head *SetOp, members []Node) *SetOp {
	var links []*SetOp
	for x := head; x != nil; {
		links = append(links, x)
		l, ok := x.Left.(*SetOp)
		if !ok || !unionAllAppendLink(l) {
			break
		}
		x = l
	}
	// links[0] is the top; links[len-1] joins the first two members.
	var chain Node = members[0]
	for i := 1; i < len(members); i++ {
		src := links[len(links)-i]
		cp := *src
		cp.PlanCost = PlanCost{}
		cp.InitPlanCharge = InitPlanCharge{}
		cp.Left, cp.Right = chain, members[i]
		chain = &cp
	}
	return chain.(*SetOp)
}

// partitionParentOf is the partitioned parent of a partition child.
func partitionParentOf(cat catalog.Catalog, child *catalog.Table) *catalog.Table {
	if child == nil || child.PartitionParentOID == 0 {
		return nil
	}
	type byOID interface {
		LookupTableByOID(oid uint32, dbOid ...uint32) (*catalog.Table, bool)
	}
	lk, ok := cat.(byOID)
	for depth := 0; !ok && depth < 4; depth++ {
		w, isW := cat.(interface{ Unwrap() catalog.Catalog })
		if !isW {
			break
		}
		cat = w.Unwrap()
		lk, ok = cat.(byOID)
	}
	if !ok {
		return nil
	}
	t, ok := lk.LookupTableByOID(child.PartitionParentOID)
	if !ok {
		return nil
	}
	return t
}

// orderedMemberIndex is a plain ascending btree index on tbl led by cols, in
// order; nil when there is none.
func orderedMemberIndex(cat catalog.Catalog, tbl *catalog.Table, cols []string) *catalog.Index {
	for _, ix := range cat.IndexesOnTable(tbl) {
		if !ix.HasStorage() || ix.HasPredicate || !isBTreeIndex(ix) || ix.DeclaredHash || len(ix.Columns) < len(cols) {
			continue
		}
		ok := true
		for i, c := range cols {
			if ix.Columns[i] == "" || !strings.EqualFold(ix.Columns[i], c) ||
				(i < len(ix.ColDescending) && ix.ColDescending[i]) || (i < len(ix.ColNullsFirst) && ix.ColNullsFirst[i]) {
				ok = false
				break
			}
		}
		if ok && indexUnboundKeysNotNull(tbl, ix, 0) {
			return ix
		}
	}
	return nil
}

// rangeLowerBound is a range partition's lower bound tuple over the first n
// key columns, integer-valued; MINVALUE sorts first. false for a default or
// list partition, or a non-integer bound.
func rangeLowerBound(tbl *catalog.Table, n int) ([]int64, bool) {
	if len(tbl.PartitionBounds) != 1 {
		return nil, false
	}
	b := tbl.PartitionBounds[0]
	if b.IsDefault || b.IsHash || len(b.InValues) > 0 || len(b.FromValues) < n {
		return nil, false
	}
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		switch {
		case i < len(b.FromUnbounded) && b.FromUnbounded[i] && !(i < len(b.FromUnboundMax) && b.FromUnboundMax[i]):
			out[i] = math.MinInt64
		case i < len(b.FromUnbounded) && b.FromUnbounded[i]:
			out[i] = math.MaxInt64
		default:
			v, err := strconv.ParseInt(strings.TrimSpace(b.FromValues[i]), 10, 64)
			if err != nil {
				return nil, false
			}
			out[i] = v
		}
	}
	return out, true
}

// compareBoundTuples orders two lower-bound tuples lexicographically.
func compareBoundTuples(a, b []int64) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// orderedMemberScan is a member's full ordered scan over idx: an Index Only
// Scan when idx covers every column of the table (the SeqScan's schema, so
// the member's Projects keep their positions), else an Index Scan; costed by
// cost_index over the whole index.
func orderedMemberScan(cat catalog.Catalog, cp costParams, seq *SeqScan, idx *catalog.Index, backward bool, totalPages float64) Node {
	tbl := seq.Table
	relTuples := minmaxRelTuples(cat, tbl)
	relPages := baseRelPages(tbl, relTuples)
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(cat, idx, tbl, relTuples)
	covers := true
	for _, c := range tbl.Columns {
		found := false
		for _, ic := range idx.Columns {
			if strings.EqualFold(ic, c.Name) {
				found = true
				break
			}
		}
		if !found {
			covers = false
			break
		}
	}
	in := indexScanInputs{
		relPages:        relPages,
		relTuples:       relTuples,
		indexPages:      indexPages,
		indexTuples:     indexTuples,
		treeHeight:      treeHeight,
		selectivity:     1,
		correlation:     indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
		loopCount:       1,
		indexOnly:       covers,
	}
	if covers {
		in.allVisFrac = relAllVisibleFraction(cat, tbl, relPages)
	}
	c := costIndexScan(cp, in)
	rows := math.Max(math.RoundToEven(relTuples), 1)
	if covers {
		ios := &IndexOnlyScan{
			pos:                   seq.pos,
			Table:                 tbl,
			Alias:                 seq.Alias,
			RTID:                  seq.RTID,
			Index:                 idx,
			Covered:               append([]catalog.Column(nil), tbl.Columns...),
			Backward:              backward,
			schema:                seq.schema,
			PrivilegeCheckRole:    seq.PrivilegeCheckRole,
			PrivilegeCheckRoleSet: seq.PrivilegeCheckRoleSet,
		}
		ios.setPlanCost(PlanCost{StartupCost: c.Startup, TotalCost: c.Total, PlanRows: rows, PlanWidth: TupleWidth(ios.Output())})
		return ios
	}
	is := &IndexScan{
		pos:                   seq.pos,
		Table:                 tbl,
		Alias:                 seq.Alias,
		RTID:                  seq.RTID,
		Index:                 idx,
		Backward:              backward,
		schema:                seq.schema,
		PrivilegeCheckRole:    seq.PrivilegeCheckRole,
		PrivilegeCheckRoleSet: seq.PrivilegeCheckRoleSet,
	}
	is.setPlanCost(PlanCost{StartupCost: c.Startup, TotalCost: c.Total, PlanRows: rows, PlanWidth: TupleWidth(is.Output())})
	return is
}

