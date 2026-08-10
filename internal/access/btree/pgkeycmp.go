package btree

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-i — the comparison SEAM, behaviour-preserving.
//
// 3b-1 built `ComparePGIndexTuples` (`_bt_compare`'s tuple-vs-tuple body) and
// 3b-2a/2b built the opclass comparators and the catalog→descriptor mapper.
// What was still missing is the thing that lets any of them be REACHED: the
// engine called the package-level `CompareKeys` directly from ~20 places
// (descent, insert-slot search, the high-key overshoot tests, the split-path
// page rewrite, Search, rangeScanPos, and the two bulk-load sorts). Twenty
// direct calls cannot be flipped atomically, and the flip MUST be atomic —
// the sibling-path rule here is symmetric:
//
//   - a descriptor-derived READER against a blob-writing writer reads garbage;
//   - a datum-writing WRITER against a surviving `bytes.Compare` site ORDERS
//     garbage (a real PG datum is not order-preserving under bytes.Compare for
//     any type but bytea/text).
//
// So this file introduces ONE per-index comparer that every ordering decision
// in the package now routes through, and 3b-2c-ii flips it — plus the three
// key encoders in internal/executor — in a single REINDEX-required commit.
//
// Nothing about the on-disk format changes here: every BTree and bulk build is
// still constructed with a nil descriptor, and a nil descriptor IS
// `CompareKeys`, byte for byte.
// ---------------------------------------------------------------------------

// keyComparer orders two KEY OPERANDS of one index.
//
// "Key operand" is deliberately the index's own key representation rather than
// a fixed byte layout, because that representation is exactly what 3b-2c-ii
// changes:
//
//   - desc == nil (today, every index): the operand is goopg's opaque
//     order-preserving key encoding — `item.key`, or a search key from
//     `encodeCompositeBTreeKey`/`encodeIndexKeyFromCols`/`encodeArbiterKey` —
//     and the ordering is `CompareKeys` (bytes.Compare), which those encodings
//     are built to satisfy.
//   - desc != nil (3b-2c-ii): the operand is a whole on-page nbtree tuple
//     (`FormPGIndexTuple` output), because `ComparePGIndexTuples` needs the
//     parts of it a bare key payload cannot carry — t_info's null bitmap, and
//     t_tid's attribute count and heap TID. Search keys become tuple-shaped
//     for the same reason, which is why one operand type covers both sides.
//
// The zero value is the bytewise comparer, so a BTree built without a
// descriptor behaves exactly as it did before this seam existed.
type keyComparer struct {
	// desc is the index's key descriptor, or nil for bytewise ordering.
	desc *PGIndexKeyDesc
}

// compare is the seam. It returns the usual <0 / 0 / >0.
//
// It cannot return an error, on purpose: it is called from the innermost
// descent and page-search loops and from `sort.Search`/`sort.SliceStable`
// predicates, none of which have anywhere to put one — the same constraint
// upstream puts on an opclass support function, which is why `sk_func` is a
// plain comparison too. When the descriptor path cannot decode an operand
// (a corrupt tuple, or a posting-list tuple, which `ComparePGIndexTuples`
// refuses rather than guessing a heap TID for) it falls back to the bytewise
// order, matching 3b-2a's choice for a datum whose length does not match
// attlen. That keeps the ordering TOTAL and DETERMINISTIC, which is what a
// split needs to terminate; detecting the corruption itself is amcheck's job
// (internal/amcheck/verify_nbtree.go), not the descent loop's.
func (c keyComparer) compare(a, b []byte) int {
	if c.desc == nil {
		return CompareKeys(a, b)
	}
	res, err := ComparePGIndexTuples(c.desc, a, b)
	if err != nil {
		return CompareKeys(a, b)
	}
	return res
}

// KeyDesc reports the descriptor this tree orders by, or nil for the bytewise
// order. It exists so a caller that ASSEMBLED the tree can verify what it got
// (M0130-S11.4 slice 3b-2c-ii-A wires descriptors through nineteen open sites;
// "the descriptor reached the tree" is otherwise unobservable from outside the
// package) and so amcheck can report the ordering it is verifying against.
func (bt *BTree) KeyDesc() *PGIndexKeyDesc { return bt.cmp.desc }

// keyCmp is the comparer for this index. Reading it through a method (rather
// than touching the field) keeps the "a BTree assembled by a path that forgot
// to set it still compares bytewise" case explicit instead of accidental.
func (bt *BTree) keyCmp() keyComparer { return bt.cmp }
