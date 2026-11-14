package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// Partition scheme of an appendrel (M0148-0001c3a9 slice 2).
//
// PG's RelOptInfo for a partitioned table carries part_scheme (strategy,
// key count, key types / opfamilies / collations), boundinfo, nparts,
// part_rels and partexprs (set_baserel_partition_key_exprs,
// optimizer/util/plancat.c set_relation_partition_info). Partitionwise join
// (try_partitionwise_join, joinrels.c) reads them to decide whether two
// rels can be joined partition by partition:
//
//   - build_joinrel_partition_info requires both sides to share one
//     PartitionScheme (find_partition_scheme interns schemes by strategy,
//     partnatts, partopfamily, partopcintype, partcollation), and
//     have_partkey_equi_join to find an equi-join clause on every key
//     column (match_expr_to_partition_keys);
//   - try_partitionwise_join then requires the two rels' bounds to be equal
//     (partition_bounds_equal) — or mergeable, partition_bounds_merge.
//
// goopg's appendrel reaches the join search as a pre-built UNION ALL chain.
// partScheme is the same information, stamped on that chain's top SetOp when
// a partitioned table is expanded, so the search can ask PG's questions.
// Scope today: a one-level partitioned table (every leaf is a direct
// partition) keyed by plain columns. Sub-partitioned and expression-keyed
// tables carry no scheme (PG compares sub-partition schemes recursively and
// matches key expressions; ledgered).
type partScheme struct {
	parent *catalog.Table
	// parts are the partitions in PartitionDesc order, one per appendrel
	// member in member order.
	parts []*catalog.Table
	// keyPos are the partition key columns' positions in the appendrel's
	// output (the parent's columns, then tableoid).
	keyPos []int
}

// newPartScheme builds the scheme for parent expanded into leaves (already in
// PartitionDesc order), or nil when the table is outside today's scope.
func newPartScheme(parent *catalog.Table, leaves []*catalog.Table) *partScheme {
	if parent == nil || len(parent.PartitionKey) == 0 || len(leaves) < 2 {
		return nil
	}
	for i := range parent.PartitionKey {
		if i < len(parent.PartitionKeyExprs) && parent.PartitionKeyExprs[i] != nil {
			return nil
		}
	}
	for _, l := range leaves {
		if l.PartitionParentOID != parent.OID {
			return nil
		}
		if _, ok := ownPartitionBound(l); !ok {
			return nil
		}
	}
	keyPos := make([]int, len(parent.PartitionKey))
	for i, k := range parent.PartitionKey {
		keyPos[i] = -1
		for j, c := range parent.Columns {
			if strings.EqualFold(c.Name, k) {
				keyPos[i] = j
				break
			}
		}
		if keyPos[i] < 0 {
			return nil
		}
	}
	return &partScheme{parent: parent, parts: leaves, keyPos: keyPos}
}

// keyType is the i-th partition key column's type.
func (ps *partScheme) keyType(i int) catalog.Type {
	return ps.parent.Columns[ps.keyPos[i]].Type
}

// samePartitionScheme is find_partition_scheme's identity test: the same
// strategy, key count, and per key the same type, operator class and
// collation.
func samePartitionScheme(a, b *partScheme) bool {
	if a == nil || b == nil {
		return false
	}
	pa, pb := a.parent, b.parent
	if !strings.EqualFold(pa.PartitionMethod, pb.PartitionMethod) || len(a.keyPos) != len(b.keyPos) {
		return false
	}
	for i := range a.keyPos {
		ta, tb := a.keyType(i), b.keyType(i)
		if !strings.EqualFold(ta.Name, tb.Name) || ta.IsArray != tb.IsArray {
			return false
		}
		if !strings.EqualFold(sliceAt(pa.PartitionKeyOpClasses, i), sliceAt(pb.PartitionKeyOpClasses, i)) ||
			!strings.EqualFold(sliceAt(pa.PartitionKeyCollations, i), sliceAt(pb.PartitionKeyCollations, i)) {
			return false
		}
	}
	return true
}

func sliceAt(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// partitionBoundsEqual is partition_bounds_equal (partbounds.c) for two rels
// of one scheme: the same number of partitions and, partition by partition
// in PartitionDesc order, the same bound — so partition i of one side can
// only hold rows that join with partition i of the other.
func partitionBoundsEqual(a, b *partScheme) bool {
	if !samePartitionScheme(a, b) || len(a.parts) != len(b.parts) {
		return false
	}
	kinds := make([]boundKind, len(a.keyPos))
	for i := range kinds {
		kinds[i] = partitionKeyBoundKind(a.parent, i)
	}
	for i := range a.parts {
		ba, _ := ownPartitionBound(a.parts[i])
		bb, _ := ownPartitionBound(b.parts[i])
		if !boundsEqual(kinds, ba, bb) {
			return false
		}
	}
	return true
}

// boundsEqual compares two partitions' own bounds value by value.
func boundsEqual(kinds []boundKind, a, b catalog.PartitionBound) bool {
	if a.IsDefault || b.IsDefault {
		return a.IsDefault && b.IsDefault
	}
	if a.IsHash || b.IsHash {
		return a.IsHash && b.IsHash && a.Modulus == b.Modulus && a.Remainder == b.Remainder
	}
	if len(a.InValues) > 0 || len(b.InValues) > 0 {
		return listBoundsEqual(kinds[0], a, b)
	}
	return rangeEdgeEqual(kinds, a.FromValues, b.FromValues, a.FromUnbounded, b.FromUnbounded, a.FromUnboundMax, b.FromUnboundMax) &&
		rangeEdgeEqual(kinds, a.ToValues, b.ToValues, a.ToUnbounded, b.ToUnbounded, a.ToUnboundMax, b.ToUnboundMax)
}

func rangeEdgeEqual(kinds []boundKind, av, bv []string, au, bu, am, bm []bool) bool {
	if len(av) != len(kinds) || len(bv) != len(kinds) {
		return false
	}
	flag := func(f []bool, i int) bool { return i < len(f) && f[i] }
	for i := range kinds {
		ua, ub := flag(au, i), flag(bu, i)
		if ua || ub {
			if ua != ub || flag(am, i) != flag(bm, i) {
				return false
			}
			continue
		}
		c, ok := compareBoundValue(kinds[i], av[i], bv[i])
		if !ok || c != 0 {
			return false
		}
	}
	return true
}

// listBoundsEqual: the same set of values, NULL included.
func listBoundsEqual(k boundKind, a, b catalog.PartitionBound) bool {
	collect := func(pb catalog.PartitionBound) (vals []string, hasNull bool) {
		for i, raw := range pb.InValues {
			if listValueIsNull(pb, i, raw) {
				hasNull = true
				continue
			}
			vals = append(vals, raw)
		}
		return
	}
	av, an := collect(a)
	bv, bn := collect(b)
	if an != bn {
		return false
	}
	contains := func(set []string, v string) (bool, bool) {
		for _, s := range set {
			c, ok := compareBoundValue(k, s, v)
			if !ok {
				return false, false
			}
			if c == 0 {
				return true, true
			}
		}
		return false, true
	}
	for _, pair := range [][2][]string{{av, bv}, {bv, av}} {
		for _, v := range pair[0] {
			in, ok := contains(pair[1], v)
			if !ok || !in {
				return false
			}
		}
	}
	return true
}

// partkeyEquiJoined is have_partkey_equi_join (joinrels.c): every partition
// key column of the outer appendrel is equated, by a mergeable equi-join
// clause, with the same-numbered key column of the inner one. The appendrels'
// output columns are named after their parents' columns and tagged with the
// FROM item they came from, so a key operand is a plain ColumnRef of that
// source and name. A clause equating key i with key j ≠ i does not count,
// as upstream's match_expr_to_partition_keys pairs key positions.
func partkeyEquiJoined(outer, inner *partScheme, outerSrc, innerSrc int16, clauses []*restrictInfo) bool {
	if outer == nil || inner == nil || len(outer.keyPos) != len(inner.keyPos) {
		return false
	}
	isKey := func(ps *partScheme, src int16, e Expr) int {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.SourceTableIdx != src {
			return -1
		}
		for i, pos := range ps.keyPos {
			if strings.EqualFold(cr.Name, ps.parent.Columns[pos].Name) {
				return i
			}
		}
		return -1
	}
	matched := make([]bool, len(outer.keyPos))
	for _, ri := range clauses {
		if ri == nil || !ri.isEquijoin {
			continue
		}
		for _, sides := range [2][2]Expr{{ri.leftKey, ri.rightKey}, {ri.rightKey, ri.leftKey}} {
			o, in := isKey(outer, outerSrc, sides[0]), isKey(inner, innerSrc, sides[1])
			if o >= 0 && o == in {
				matched[o] = true
			}
		}
	}
	for _, m := range matched {
		if !m {
			return false
		}
	}
	return true
}

// relPartScheme is the partition scheme of a search rel whose leaf is a
// partitioned table's appendrel, or nil.
func relPartScheme(rel *RelOptInfo) *partScheme {
	if rel == nil {
		return nil
	}
	if so, ok := rel.baseLeaf.(*SetOp); ok {
		return so.partScheme
	}
	return nil
}
