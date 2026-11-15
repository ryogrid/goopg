package optimizer

import (
	"math/bits"
	"strings"

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
	cinfo.baseRows = childRelTuples(member, tbl)
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

// childScanRows is a child rel's tuple count as set_append_rel_size sizes
// each member: its own scan's estimate_rel_size (with the 10-page floor for a
// never-vacuumed, empty-looking partition), as a single-leaf base rel is sized
// (cardinality.go). The raw pg_class count (tableRows) is 0 for such a
// partition and priced its index paths as if it held one row, where PG prices
// the 10-page estimate (M0148-0001c3a10 slice 6, regress aggregates' p_t1).
func childScanRows(member Node, tbl *catalog.Table) int64 {
	if scan := leafBaseScan(memberScanRoot(member)); scan != nil {
		if r := EstimateRows(scan); r >= 1 {
			return r
		}
	}
	if r := tableRows(tbl); r >= 1 {
		return r
	}
	return 1
}

// membersCarryFilters reports whether any appendrel member's scan already
// carries a local filter.
func membersCarryFilters(so *SetOp) bool {
	for _, m := range unionAllMembers(so) {
		if memberLocalFilter(m) != nil {
			return true
		}
	}
	return false
}

// childRelTuples is the tuple count the child producers price with. An
// unrestricted child (slice 6: an ordering-only child rel) takes PG's
// estimate_rel_size (childScanRows). A restricted one keeps the pg_class
// count slices 1-5 priced with: moving it to the scan estimate exposes a
// missing child index-only path for boolean-column quals (regress inherit's
// bool_rp: `Filter: b` instead of `Index Cond: (b = true)`), which turns
// PG's Index Only Scans into Seq Scans (ledgered).
func childRelTuples(member Node, tbl *catalog.Table) int64 {
	if len(extractFilterConjuncts(memberScanRoot(member))) == 0 {
		return childScanRows(member, tbl)
	}
	// Unclamped, as slices 1-5 passed it: a zero count makes the child's seq
	// scan cost fall back to the scan's own page estimate
	// (baseSeqScanCostInputs); the index producers clamp to one row.
	return tableRows(tbl)
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
	relTuples := float64(childRelTuples(rel.baseLeaf, tbl))
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
	// The index-only arm (check_index_only on the child): the parent's
	// needed columns, read under the member's alias, against the child
	// table — a partition carries the parent's column names. Run before the
	// other producers turn the statement-wide set off below.
	if s.neededColsKnown && !indexOnlyHardDisabled(s.cat) {
		s.addChildIndexOnlyPaths(rel, tbl, colExprs, relPages, relTuples, totalPages)
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
	// create_index_paths' bitmap half: one bitmap heap path per usable index
	// over the child's own restriction (M0148-0001c3a10).
	if len(conjuncts) > 0 {
		T := float64(relPages)
		if T < 1 {
			T = 1
		}
		maxEntries := bitmapMaxEntries(s.cp.workMem)
		for _, idx := range s.cat.IndexesOnTable(tbl) {
			if !idx.HasStorage() {
				continue
			}
			if p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, totalPages, maxEntries, rel.baseLeaf); p != nil {
				addPath(rel, p, "appendrel-child.bitmap")
			}
		}
	}
}

// addAppendRelChildPaths is set_append_rel_pathlist for a partitioned base
// rel (M0148-0001c3a10): every partition is planned as a child rel of its
// own — its bare scan, the parent's restriction moved onto it, and every
// index, parameterised index and bitmap path its indexes allow — and the
// appendrel gains an Append over the cheapest path of each child
// (add_paths_to_append_rel's cheapest-total arm). It competes in add_path
// with the pre-built UNION ALL of member seq scans, so a selective
// restriction now reaches the partitions' indexes:
// `Append -> Bitmap Heap Scan on pa_1 … Index Cond: (b = 'v12'::text)`.
// Statements that keep appendrels whole (row locks, DML) are skipped, and so
// is any problem with more than one relation (see the scope note below).
func (s *searchCtx) addAppendRelChildPaths(cat catalog.Catalog) {
	if s == nil || cat == nil || s.cp.keepAppendRelsWhole {
		return
	}
	// Scope (slice 1 of c3a10): a one-relation problem — the partitioned
	// table is the whole FROM list. Inside a join the Append's columns reach
	// EXPLAIN's key chase and parameterised quals through paths not yet
	// taught about it (regress partition_join regressed there); ledgered.
	if s.nrels != 1 {
		return
	}
	if s.cat == nil {
		// The child producers read s.cat; the base-rel producer pass hands
		// the problem's catalog in, which a search built without the trace
		// context may not have stamped on s.
		s.cat = cat
	}
	for i, rel := range s.levelRels(1) {
		if i >= len(s.relInfos) {
			break
		}
		ps := relPartScheme(rel)
		if ps == nil {
			continue
		}
		so, f := appendRelLeaf(rel)
		// Without a restriction the child rels only matter for an ordering:
		// slice 6 files just the ordered Append / Merge Append over them, so
		// they compete with the Sort over the appendrel as PG's
		// generate_orderedappend_paths do. The unordered Append over the
		// children's cheapest paths is filed only with a restriction — an
		// unrestricted one would let the children's ordering-only index
		// paths undercut the appendrel's seq scans on goopg's cost model
		// where PG keeps them (regress aggregates' p_t1).
		unrestricted := f == nil || f.Predicate == nil
		if unrestricted && (len(s.queryPathkeys) == 0 || membersCarryFilters(so)) {
			// Members whose scans already carry the WHERE quals (pushed
			// down before the search) are a restricted appendrel without
			// the parent Filter slices 1-5 key on; they stay whole.
			continue
		}
		// A correlated restriction (a sublink body's qual reading the outer
		// row) stays on the appendrel: PG plans those statements as semi /
		// anti joins, and per-member copies of a SubPlan's parameterised
		// qual only multiply the divergence (regress partition_join).
		if !unrestricted && exprHasOuterRef(f.Predicate) {
			continue
		}
		members := unionAllMembers(so)
		if len(members) != len(ps.parts) {
			continue
		}
		children := make([]*Path, 0, len(members))
		childRels := make([]*RelOptInfo, 0, len(members))
		fills := make([]map[int]Expr, 0, len(members))
		ok := true
		for j, m := range members {
			c, fill := s.appendChildRel(rel, m, f, so.Output(), s.relInfos[i], ps, j)
			if c == nil {
				ok = false
				break
			}
			best := cheapestUnparameterized(c)
			if best == nil {
				ok = false
				break
			}
			children = append(children, best)
			childRels = append(childRels, c)
			fills = append(fills, fill)
		}
		if !ok {
			continue
		}
		if !unrestricted {
			pa := newPartitionwiseAppendPath(rel, children, s.cp)
			pa.pwParents = []*SetOp{so}
			pa.pwFills = fills
			addPath(rel, pa, "appendrel-children")
		}
		s.addOrderedAppendRelChildPath(rel, so, ps, childRels, fills, unrestricted)
		setCheapest(rel)
	}
}

// addOrderedAppendRelChildPath is generate_orderedappend_paths' plain-Append
// arm for partitions_are_ordered (allpaths.c / partbounds.c): a RANGE
// partitioned table without a default partition whose partition key leads
// the query's ordering delivers that ordering through an Append of child
// paths each sorted the same way — in bound order, or reversed (each child
// scanned backward) for a descending key. Each child contributes its
// cheapest-total path that satisfies the query pathkeys; a child with no such
// path declines the whole Append, as PG would need a Merge Append or a Sort
// there (M0148-0001c3a10 slice 3). This is what PG's per-partition min / max
// rides: `Limit -> Append -> Index Only Scan Backward … (c = 3)`.
//
// For an unrestricted appendrel (slice 6) only the Merge Append arm runs: the
// partitions-ordered plain Append over full index scans is
// addOrderedPartitionAppendPaths' (orderedpartitionappend.go), which already
// files PG's Index Only Scans there; a child rel's full index-only scan
// carries no ordering yet (ledgered).
func (s *searchCtx) addOrderedAppendRelChildPath(rel *RelOptInfo, so *SetOp, ps *partScheme, childRels []*RelOptInfo, fills []map[int]Expr, unrestricted bool) {
	qk := s.queryPathkeys
	if len(qk) == 0 || len(ps.keyPos) == 0 {
		return
	}
	// Every query key must be a plain column of this appendrel: the child
	// rels' paths order by those columns only.
	src := appendRelLeafSource(so.Output())
	for _, pk := range qk {
		c, ok := pk.Expr.(*ColumnRef)
		if !ok || c.SourceTableIdx != src {
			return
		}
	}
	if !rangePartitionsOrderedOn(ps, qk[0].Expr.(*ColumnRef)) {
		// Not partitions_are_ordered on the leading key: the ordering is a
		// Merge Append of the children (slice 5), for every useful ordering
		// a child delivers.
		for _, keys := range childOrderingPrefixes(childRels, qk) {
			s.addMergeAppendRelChildPaths(rel, so, childRels, fills, keys)
		}
		return
	}
	if unrestricted {
		return
	}
	backward := !qk[0].SortAsc
	ordered := make([]*Path, len(childRels))
	for j, c := range childRels {
		var best *Path
		for _, p := range c.Pathlist {
			if p == nil || p.RequiredOuter != 0 || !pathkeysContainedIn(p.Pathkeys, qk) {
				continue
			}
			if best == nil || p.DisabledNodes < best.DisabledNodes ||
				(p.DisabledNodes == best.DisabledNodes && p.Cost.Total < best.Cost.Total) {
				best = p
			}
		}
		if best == nil {
			return
		}
		ordered[j] = best
	}
	ofills := append([]map[int]Expr(nil), fills...)
	if backward {
		for a, b := 0, len(ordered)-1; a < b; a, b = a+1, b-1 {
			ordered[a], ordered[b] = ordered[b], ordered[a]
			ofills[a], ofills[b] = ofills[b], ofills[a]
		}
	}
	pa := newPartitionwiseAppendPath(rel, ordered, s.cp)
	pa.pwParents = []*SetOp{so}
	pa.pwFills = ofills
	pa.Pathkeys = qk
	addPath(rel, pa, "appendrel-children.ordered")
}

// rangePartitionsOrderedOn is partitions_are_ordered plus
// build_partition_pathkeys' leading-key test for the shapes goopg recognises:
// a RANGE partitioned table with no default partition whose (first)
// partition key column is col.
func rangePartitionsOrderedOn(ps *partScheme, col *ColumnRef) bool {
	if !strings.EqualFold(ps.parent.PartitionMethod, "RANGE") {
		return false
	}
	for _, part := range ps.parts {
		if b, ok := ownPartitionBound(part); !ok || b.IsDefault {
			return false
		}
	}
	return strings.EqualFold(col.Name, ps.parent.Columns[ps.keyPos[0]].Name)
}

// addMergeAppendRelChildPaths is generate_orderedappend_paths' Merge Append
// arm (allpaths.c) for the query ordering qk over a partitioned table's child
// rels when the partitions are not ordered on it (a default partition, LIST,
// a non-leading partition key). Per child it takes the cheapest-startup and
// cheapest-total paths delivering qk — or, when the child has none, its
// cheapest-total path, sorted (create_merge_append_path) — and under a LIMIT
// the cheapest fractional one too; it files the startup Merge Append, the
// total one when they differ, and the fractional one. As all_child_pathkeys
// only proposes orderings some child delivers, it declines when no child
// does. M0148-0001c3a10 slice 5.
func (s *searchCtx) addMergeAppendRelChildPaths(rel *RelOptInfo, so *SetOp, childRels []*RelOptInfo, fills []map[int]Expr, qk []PathKey) {
	n := len(childRels)
	startup, total := make([]*Path, n), make([]*Path, n)
	var fractional []*Path
	if s.tupleFraction > 0 {
		fractional = make([]*Path, n)
	}
	anyOrdered, neq := false, false
	for j, c := range childRels {
		st := getCheapestPathForPathkeys(c.Pathlist, qk, startupCost)
		tt := getCheapestPathForPathkeys(c.Pathlist, qk, totalCost)
		if st == nil || tt == nil {
			st = cheapestUnparameterized(c)
			tt = st
		} else {
			anyOrdered = true
		}
		if st == nil {
			return
		}
		if fractional != nil {
			f := s.tupleFraction
			if f >= 1 && tt.Rows > 0 {
				f /= tt.Rows
			}
			fr := cheapestFractionalPathForPathkeys(c.Pathlist, qk, f)
			if fr == nil {
				fr = tt
			}
			fractional[j] = fr
		}
		if st != tt {
			neq = true
		}
		startup[j], total[j] = st, tt
	}
	if !anyOrdered {
		return
	}
	addPath(rel, s.mergeAppendRelChildPath(rel, so, startup, fills, qk), "appendrel-children.merge")
	if neq {
		addPath(rel, s.mergeAppendRelChildPath(rel, so, total, fills, qk), "appendrel-children.merge")
	}
	if fractional != nil {
		addPath(rel, s.mergeAppendRelChildPath(rel, so, fractional, fills, qk), "appendrel-children.merge")
	}
}

// mergeAppendRelChildPath is create_merge_append_path over subs: a child not
// already in qk order gets a Sort, bounded by the query's LIMIT as the rel is
// the whole query (pathnode->limit_tuples), and cost_merge_append prices the
// merge over the children's startup and total costs.
func (s *searchCtx) mergeAppendRelChildPath(rel *RelOptInfo, so *SetOp, subs []*Path, fills []map[int]Expr, qk []PathKey) *Path {
	limit := -1.0
	if s.tupleFraction >= 1 {
		limit = s.tupleFraction
	}
	children := make([]*Path, len(subs))
	var st, tt, rows float64
	disabled := 0
	for j, p := range subs {
		if !pathkeysContainedIn(p.Pathkeys, qk) {
			p = sortPathForBounded(p, qk, s.cp, limit)
		}
		children[j] = p
		st += p.Cost.Startup
		tt += p.Cost.Total
		rows += p.Rows
		disabled += p.DisabledNodes
	}
	pa := newPartitionwiseAppendPath(rel, children, s.cp)
	pa.Cost = mergeAppendCost(s.cp, len(children), rows, st, tt)
	pa.DisabledNodes = disabled
	pa.Pathkeys = qk
	pa.pwMerge = true
	pa.pwParents = []*SetOp{so}
	pa.pwFills = append([]map[int]Expr(nil), fills...)
	return pa
}

// childOrderingPrefixes is all_child_pathkeys (add_paths_to_append_rel,
// allpaths.c) cut to what the one-relation query can use: each distinct
// leading part of qk some child's unparameterised path delivers. A Merge
// Append on a strict prefix leaves the rest of the ORDER BY to an Incremental
// Sort above it (PG's `Presorted Key: pm.a` over `Merge Append`).
func childOrderingPrefixes(childRels []*RelOptInfo, qk []PathKey) [][]PathKey {
	seen := make([]bool, len(qk)+1)
	for _, c := range childRels {
		for _, p := range c.Pathlist {
			if p == nil || p.RequiredOuter != 0 {
				continue
			}
			n := 0
			for n < len(p.Pathkeys) && n < len(qk) && pathKeyEqual(p.Pathkeys[n], qk[n]) {
				n++
			}
			seen[n] = true
		}
	}
	var out [][]PathKey
	for n := len(qk); n > 0; n-- {
		if seen[n] {
			out = append(out, qk[:n])
		}
	}
	return out
}

// cheapestFractionalPathForPathkeys is
// get_cheapest_fractional_path_for_pathkeys (pathkeys.c): the unparameterised
// path delivering keys that is cheapest to fetch fraction of its rows from.
func cheapestFractionalPathForPathkeys(paths []*Path, keys []PathKey, fraction float64) *Path {
	var best *Path
	for _, p := range paths {
		if p == nil || p.RequiredOuter != 0 {
			continue
		}
		if best != nil && compareFractionalPathCosts(best, p, fraction) <= 0 {
			continue
		}
		if pathkeysContainedInRel(p.Rel, p.Pathkeys, keys) {
			best = p
		}
	}
	return best
}

// appendRelLeafSource is the binding id an appendrel's output columns carry.
func appendRelLeafSource(out Schema) int16 {
	for _, c := range out {
		if c.SourceTableIdx != 0 {
			return c.SourceTableIdx
		}
	}
	return 0
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
	return appendRelLeafNode(rel.baseLeaf)
}

// appendRelLeafNode is appendRelLeaf over a leaf node.
func appendRelLeafNode(n Node) (*SetOp, *Filter) {
	var f *Filter
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

// addChildIndexOnlyPaths is addIndexOnlyPaths' per-rel body for a partition
// child rel (M0148-0001c3a10 slice 3).
func (s *searchCtx) addChildIndexOnlyPaths(rel *RelOptInfo, tbl *catalog.Table, colExprs map[string]Expr, relPages int64, relTuples, totalPages float64) {
	bare := scanLeafIsBare(rel.baseLeaf)
	var conjuncts []Expr
	if !bare {
		conjuncts = extractFilterConjuncts(rel.baseLeaf)
		if len(conjuncts) == 0 {
			return
		}
	}
	needed := s.neededColumnsOfRel(rel, tbl)
	if len(needed) == 0 {
		return
	}
	for _, idx := range s.cat.IndexesOnTable(tbl) {
		if !idx.HasStorage() {
			continue
		}
		var clauses []indexPathClause
		if !bare {
			var ok bool
			clauses, ok = s.indexOnlyLeafClauses(s.cat, rel, tbl, idx, needed, conjuncts)
			if !ok {
				continue
			}
		} else if !indexUnboundKeysNotNull(tbl, idx, 0) {
			continue
		}
		s.addOneIndexOnlyPath(s.cat, rel, tbl, idx, needed, clauses, colExprs, relPages, relTuples, totalPages)
	}
}
