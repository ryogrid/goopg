package nbtree

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

// indexFormat is ONE index's key format: how its key operands are ordered
// (this file) and how they are laid out in an on-page tuple (pgitemcodec.go).
//
// It was born in 3b-2c-i as `keyComparer`, an ordering-only object, and grew
// the item codec in 3b-2c-ii-B1 for the reason the rename records: the
// descriptor decides BOTH. A descriptor-bearing index does not merely compare
// differently, it stores a different thing in `item.key` (the whole
// FormPGIndexTuple image rather than a bare payload), so an ordering object and
// a layout object keyed by the same `desc` would be two names for one decision
// — and the failure mode of letting them disagree is a tree that is silently
// mis-ordered rather than one that fails to parse.
//
// As an ordering, indexFormat orders two KEY OPERANDS of one index.
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
type indexFormat struct {
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
func (c indexFormat) compare(a, b []byte) int {
	if c.desc == nil {
		return CompareKeys(a, b)
	}
	res, err := ComparePGIndexTuples(c.desc, a, b)
	if err != nil {
		return CompareKeys(a, b)
	}
	return res
}

// compareKeyAttrs orders two key operands by their KEY ATTRIBUTES ALONE,
// returning 0 for operands that name the same key value but different heap
// rows. It is `compare` minus the heap-TID tiebreak.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-vi. The distinction exists because a
// heapkeyspace tree has two different notions of "the same":
//
//   - ordering ("which comes first?") must be a TOTAL order, so it breaks ties
//     on the heap TID and therefore NEVER reports equality between two distinct
//     entries — that is `compare`, and it is what descent, split points and
//     the bulk sort want;
//   - grouping ("do these belong in one posting list?" / "does a unique build
//     reject this?") must ignore the TID, because the whole point of a posting
//     list is to hold the many TIDs that share ONE key.
//
// Under the blob format the two coincide byte for byte — an opaque key carries
// no TID, so `CompareKeys` already answers both — which is why the difference
// could stay invisible until the tuple format put the TID inside the key. The
// upstream pair is `_bt_keep_natts_fast` (nbtutils.c), which `_bt_load` and
// `_bt_dedup_pass` use to close a posting run, versus `_bt_compare`, which
// orders.
//
// The error fallback matches `compare`'s, for the same reason: a grouping
// predicate driving `sort`-shaped loops has nowhere to put an error, and the
// bytewise order keeps the answer deterministic.
func (c indexFormat) compareKeyAttrs(a, b []byte) int {
	if c.desc == nil {
		return CompareKeys(a, b)
	}
	res, err := ComparePGIndexTupleKeyAttrs(c.desc, a, b)
	if err != nil {
		return CompareKeys(a, b)
	}
	return res
}

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-B2-c-i — the prefix UPPER bound.
//
// A range scan's two bounds are not symmetric once keys are tuples, and the
// asymmetry is invisible while keys are opaque blobs. A search key that names
// only the FIRST k of an index's key attributes (probing a composite index on
// its leading column) is stamped as a pivot by `pgIndexTupleKey`, and
// `ComparePGIndexTuples` then applies upstream's truncation rule: equal on the
// attributes both operands store, the shorter one is MINUS INFINITY beyond
// them. That is exactly right for the LOW bound — the descent lands on the
// first entry of the prefix group and `compare(entry, lo) < 0` skips nothing —
// and exactly WRONG for the high bound, where `compare(entry, hi) > 0` is true
// for the very first member of the group, so the scan would stop before
// returning a single row.
//
// The blob format never had to say this out loud because it faked plus infinity
// with bytes: `appendCompositeUpperPadding` (internal/executor/operators_index.go)
// appends 64 0xFF bytes to a leading-column key so bytes.Compare orders the
// whole group below it. There is no such trick for a tuple — 0xFF bytes are a
// malformed attribute image, not a large one — and upstream does not use one
// either: nbtree carries the bound's strategy in the scan key and stops when the
// compared ATTRIBUTES exceed the bound (_bt_checkkeys / _bt_check_compare in
// nbtutils.c), never by inventing a maximal key.
//
// So the sense of a truncated bound becomes a property of the comparison, one
// per end of the range. `compare` is the low end (and every other ordering
// decision: descent, insert slot, split point — all of which want minus
// infinity); `compareHigh` is the high end.
// ---------------------------------------------------------------------------

// compareHigh orders a scan entry against a range scan's UPPER bound, returning
// >0 exactly when entry is past hi and the scan must stop.
//
// It differs from `compare` in one case only: hi stores FEWER attributes than
// entry and the two agree on all the attributes hi does store. `compare` calls
// that "entry > hi" (hi is minus infinity in the attributes it dropped);
// `compareHigh` calls it "entry <= hi", because a bound naming a prefix means
// "everything whose prefix is this", i.e. plus infinity in the dropped
// attributes. Where the operands genuinely differ on a shared attribute the two
// agree, so a bound that names every attribute behaves identically under both.
//
// For the blob format (desc == nil) this IS `compare`, byte for byte: an opaque
// key carries no attribute count, so there is no truncation to interpret and the
// caller's 0xFF padding keeps doing the job. That equivalence is what lets this
// land before the flip without moving any behaviour.
func (c indexFormat) compareHigh(entry, hi []byte) int {
	if c.desc == nil {
		return CompareKeys(entry, hi)
	}
	// KEY ATTRIBUTES ONLY — the heap TID is deliberately not weighed here.
	//
	// M0130-S11.4 slice 3b-2c-ii-B2-c (the flip) corrected this from `compare`.
	// A range bound is a bound on the index's KEY, never on a heap position: it
	// is built from expressions by `indexProbeKey`, so it always carries the
	// zero ItemPointer, which is minus infinity in the tiebreak. Weighing it
	// would put EVERY real entry (real TID > zero TID) above an upper bound that
	// names its exact key, so an equality scan on a full-key probe — the single
	// most common index scan there is — would stop before returning its first
	// row. Upstream has the same split: the scantid participates in `_bt_compare`
	// during descent, while the scan's stop condition (`_bt_check_compare`,
	// nbtutils.c) evaluates the scan keys, i.e. attributes.
	//
	// The low end keeps `compare` (with its tiebreak) and needs no such change:
	// there a zero-TID bound is minus infinity among its own duplicates, which is
	// exactly "start at the first of them".
	res, nEntry, nHi, err := comparePGIndexTupleAttrs(c.desc, entry, hi)
	if err != nil {
		return CompareKeys(entry, hi)
	}
	if res != 0 || nEntry == nHi {
		// A shared attribute decided it, or the operands store the same
		// attributes and agree on all of them (equal on the key: inside).
		return res
	}
	if nEntry < nHi {
		// entry is the shorter operand: minus infinity beyond its attributes.
		return -1
	}
	if nHi == 0 {
		// A bound storing no attribute is minus infinity, not a prefix.
		return 1
	}
	// hi stores fewer attributes than entry and the two agree on all of them:
	// equal on every attribute the bound names, i.e. inside a plus-infinity
	// upper bound. One pass answers it — before M0122-0012 slice 33 this
	// re-compared a heap copy of entry restamped to hi's attribute count, an
	// allocation and a second deform for every entry of a prefix group (TPC-DS
	// Q72's inventory skip scan: ~11 % of its CPU in that copy alone).
	return 0
}

// KeyDesc reports the descriptor this tree orders by, or nil for the bytewise
// order. It exists so a caller that ASSEMBLED the tree can verify what it got
// (M0130-S11.4 slice 3b-2c-ii-A wires descriptors through nineteen open sites;
// "the descriptor reached the tree" is otherwise unobservable from outside the
// package) and so amcheck can report the ordering it is verifying against.
func (bt *BTree) KeyDesc() *PGIndexKeyDesc { return bt.keyFmt.desc }

// format is the key format of this index — the comparer AND the item codec.
// Reading it through a method (rather than touching the field) keeps the "a
// BTree assembled by a path that forgot to set it still uses the blob format"
// case explicit instead of accidental.
func (bt *BTree) format() indexFormat { return bt.keyFmt }

// CompareLowBound and CompareHighBound are the two bound comparisons a range
// scan applies to each leaf entry (scanLeafItems): an inclusive low bound
// admits `CompareLowBound(key, lo) >= 0`, an inclusive high bound admits
// `CompareHighBound(key, hi) <= 0`. They are exported for the executor's
// skip-scan leaf walk (M0145-0008af), which checks each entry against its
// current prefix group's probe the way `_bt_checkkeys` checks an advanced
// skip array, instead of opening a cursor bounded by that probe.
func (bt *BTree) CompareLowBound(key, lo []byte) int { return bt.format().compare(key, lo) }

// CompareHighBound — see CompareLowBound.
func (bt *BTree) CompareHighBound(key, hi []byte) int { return bt.format().compareHigh(key, hi) }
