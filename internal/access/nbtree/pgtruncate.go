package nbtree

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-3c — `_bt_truncate` suffix truncation.
//
// A separator key is not a key: it is a BOUNDARY between two pages, and the
// only thing it has to do is sit strictly above every key on the left page and
// at-or-below every key on the right one. Upstream exploits that
// (postgres/src/backend/access/nbtree/nbtutils.c `_bt_truncate`): it keeps only
// the attributes that actually distinguish the last left tuple from the first
// right one and throws the rest away, which is what keeps a composite index's
// internal levels small — an index on (a, b, c) whose split falls between two
// different `a` values stores a ONE-attribute separator, not the whole tuple.
//
// Until the flip (3b-2c-ii-B2-c) goopg could not do this: `item.key` was one
// opaque blob with no attribute boundary to cut at, so every separator was the
// whole right-hand key (see PGBTPivotRaw's comment). With descriptor-bearing
// tuples the boundary exists, and this file is the cut.
//
// The half that is a CORRECTNESS fix rather than a size one is `_bt_truncate`'s
// second branch. When the split point falls between two entries whose key
// attributes are all equal — a duplicate-heavy index, or a posting chunk split
// across pages — there is no attribute that separates them, and upstream keeps
// the heap TID of `lastleft` as an extra, thirteenth-bit-flagged attribute
// (BT_PIVOT_HEAP_TID_ATTR). goopg wrote no tiebreaker: the separator was the
// full right-hand key with its t_tid overwritten by the downlink, i.e. a pivot
// with NO heap TID, which `ComparePGIndexTuples` reads as minus infinity in the
// implicit last key attribute. Every left-page entry sharing that key value
// therefore compared GREATER than its own page's high key — the exact invariant
// `itemOvershootsHighKey` and amcheck enforce — so a descent for one of those
// rows walked right past the page holding it. That was invisible before the
// flip (a blob key carries no TID, so the two sides compared equal) and is
// reachable now, which is why the tiebreaker lands here rather than staying a
// deferral.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// truncateSeparator is `_bt_truncate`: the separator that belongs between
// `lastleft` (the final entry of the left page) and `firstright` (the first
// entry of the right one), as a KEY OPERAND — the caller wraps it in
// `highKeyItem`/`downlinkItem` and `indexFormat.marshal` stamps the downlink
// and the attribute count.
//
// The blob format returns `firstright`'s key unchanged: an opaque payload has
// no attribute boundary to cut at, so there is nothing to truncate and the
// bytes stay exactly what they were before this slice existed.
//
// It cannot fail. Every decode problem falls back to the untruncated key, which
// is always a legal separator (upstream's own pre-9.6 behaviour) — the same
// discipline `indexFormat.compare` follows, and for the same reason: the split
// path has to make progress, and diagnosing a corrupt tuple is amcheck's job.
func (f indexFormat) truncateSeparator(lastleft, firstright item) []byte {
	full := append([]byte(nil), firstright.key...)
	if !f.tupleKeys() {
		return full
	}
	if len(firstright.key) < SizeOfIndexTupleData || len(lastleft.key) < SizeOfIndexTupleData {
		return full
	}
	// Only two LEAF tuples have attributes to compare. A pivot operand means
	// the caller is splitting an internal page, where upstream copies the
	// firstright pivot verbatim (it was truncated when its own leaf split) —
	// re-truncating it would need its already-dropped attributes.
	if firstright.pivot || lastleft.pivot || BTreeTupleIsPosting(firstright.key) {
		return full
	}
	keep, err := f.keepNAtts(lastleft.key, firstright.key)
	if err != nil {
		return full
	}
	nkey := f.desc.NKeyAtts()
	pivot, err := f.formTruncated(firstright.key, min(keep, nkey))
	if err != nil {
		return full
	}
	if keep <= nkey {
		// `index_truncate_tuple` + BTreeTupleSetNAtts(keepnatts, false): the
		// attributes past `keep` are minus infinity, which is what makes this
		// smaller tuple order between the two halves.
		if err := BTreeTupleSetNAtts(pivot, uint16(keep), false); err != nil {
			return full
		}
		return pivot
	}
	// keep == nkey+1: every key attribute is equal, so the heap TID of
	// `lastleft` becomes the tiebreaker attribute. Upstream takes the MAXIMUM
	// heap TID of lastleft (BTreeTupleGetMaxHeapTID), which for a posting list
	// is its last TID, so that the separator is >= every entry left of it.
	tid, ok := f.maxHeapTIDOf(lastleft)
	if !ok {
		return full
	}
	// Upstream's newsize: MAXALIGN(IndexTupleSize(pivot)) +
	// MAXALIGN(sizeof(ItemPointerData)), with the TID at the very end. The
	// alignment padding lands BETWEEN the key data and the TID, where nothing
	// reads it — deforming stops after nkeyatts attributes and
	// BTreeTupleGetHeapTID indexes back from t_info's size — so this is one
	// MAXALIGN goopg can honour today, unlike `index_form_tuple`'s (3b-3b),
	// which would round the size a blob key's length is derived from.
	newsize := MaxAlign(len(pivot)) + MaxAlign(SizeOfItemPointerData)
	out := make([]byte, newsize)
	copy(out, pivot)
	pgPutIndexTupleSize(out, newsize)
	if err := BTreeTupleSetNAtts(out, uint16(nkey), true); err != nil {
		return full
	}
	PutPGItemPointer(out[newsize-SizeOfItemPointerData:], tid)
	return out
}

// keepNAtts is `_bt_keep_natts` (nbtutils.c): the number of key attributes a
// separator between `lastleft` and `firstright` must keep — the 1-based index
// of the first attribute on which they differ, or nkeyatts+1 when they agree on
// every one of them.
//
// Upstream has two of these: `_bt_keep_natts`, which asks the OPCLASS whether
// two datums are equal, and `_bt_keep_natts_fast`, which asks whether their
// images are identical. This is the former, because that is what `_bt_truncate`
// uses and because goopg's per-attribute comparator is exactly the opclass
// question (`PGKeyAttr.compareAttr`, which also applies NULLS FIRST/LAST — two
// NULLs are equal and a NULL against a non-NULL is a difference, matching
// upstream's isNull1 != isNull2 test).
func (f indexFormat) keepNAtts(lastleft, firstright []byte) (int, error) {
	nkey := f.desc.NKeyAtts()
	if nkey == 0 {
		return 0, fmt.Errorf("btree: keepNAtts needs a key descriptor with at least one attribute")
	}
	phys := f.desc.Physical()
	lv, ln, err := DeformPGIndexTuple(lastleft, phys, nkey)
	if err != nil {
		return 0, fmt.Errorf("btree: keepNAtts: left tuple: %w", err)
	}
	rv, rn, err := DeformPGIndexTuple(firstright, phys, nkey)
	if err != nil {
		return 0, fmt.Errorf("btree: keepNAtts: right tuple: %w", err)
	}
	for i := range nkey {
		if f.desc.Attrs[i].compareAttr(lv[i], ln[i], rv[i], rn[i]) != 0 {
			return i + 1, nil
		}
	}
	return nkey + 1, nil
}

// formTruncated is `index_truncate_tuple`: a copy of `raw` re-formed with only
// its first `natts` attributes, so the bytes of the dropped ones (and of any
// INCLUDE column, which a pivot never stores) actually leave the page.
//
// Re-forming rather than restamping the attribute count is the difference
// between truncation and merely CLAIMING it: `truncatedPGIndexTuple`
// (pgkeycmp.go) restamps, because it builds a throwaway comparison operand and
// the extra bytes are never read; a separator is written to disk, where those
// bytes are the whole cost the truncation exists to avoid.
func (f indexFormat) formTruncated(raw []byte, natts int) ([]byte, error) {
	phys := f.desc.Physical()
	if natts <= 0 || natts > len(phys) {
		return nil, fmt.Errorf("btree: formTruncated: %d attributes out of range for a %d-attribute descriptor", natts, len(phys))
	}
	vals, nulls, err := DeformPGIndexTuple(raw, phys, natts)
	if err != nil {
		return nil, err
	}
	// The zero TID is deliberate: a pivot's t_tid is not a heap pointer, and
	// every caller overwrites it (BTreeTupleSetDownLink in marshal, or the
	// tiebreaker branch above).
	return FormPGIndexTuple(phys[:natts], vals, nulls, storage.ItemPointer{})
}

// rawSeparatorOperand adapts a bulk-loader `rawItem` to the LEFT operand of
// truncateSeparator. The item path hands over `item`s directly; the raw path
// has already marshalled its entries, and the two facts truncation needs — the
// key attributes and the MAXIMUM heap TID — live in the marshalled bytes rather
// than in `rawItem.key`, which for a posting run is the run's first entry
// (lowest TID) and would under-state the boundary.
func (f indexFormat) rawSeparatorOperand(ri rawItem) item {
	if !f.tupleKeys() || len(ri.raw) < SizeOfIndexTupleData {
		return item{key: ri.key}
	}
	if BTreeTupleIsPosting(ri.raw) {
		// maxHeapTIDOf reads the last element of the TID array; the key
		// attributes deform from the same data area a plain tuple's do.
		return item{key: ri.raw}
	}
	return item{key: ri.raw, ptr: PGIndexTupleTID(ri.raw)}
}

// maxHeapTIDOf is `BTreeTupleGetMaxHeapTID`: the highest heap TID an entry
// stands for — its own for a plain tuple, the LAST element of the array for a
// posting list, since a posting list's TIDs are stored ascending.
//
// `item.ptr` rather than the tuple's t_tid is the plain-tuple answer for the
// same reason `indexFormat.marshal` stamps it: the in-memory item is what every
// in-package caller reasons about, and a rebuilt item (posting split, dedup
// recovery) may not have had its header restamped yet.
func (f indexFormat) maxHeapTIDOf(it item) (storage.ItemPointer, bool) {
	if len(it.key) >= SizeOfIndexTupleData && BTreeTupleIsPosting(it.key) {
		_, tids, err := f.parsePostingRaw(it.key)
		if err != nil || len(tids) == 0 {
			return storage.ItemPointer{}, false
		}
		return tids[len(tids)-1], true
	}
	if it.pivot {
		// A pivot's t_tid is a downlink; only a tiebreaker pivot has a TID at
		// all, and it is not `item.ptr`.
		return BTreeTupleGetHeapTID(it.key)
	}
	return it.ptr, true
}
