package nbtree

// pgsplitleft.go carries block 0 of upstream's split record — the LEFT half —
// as content instead of as a full-page image: the emit-side description
// (`DescribeSplitLeft` + `SplitLeftBlockData`) and the redo that consumes it
// (`ParseSplitLeftBlockData` + `ReplaySplitLeftPage`, upstream's
// `btree_xlog_split` left arm, nbtxlog.c:305-425).
//
// M0130-S11.5b-2. Producer and consumer live in one file for pgnewroot.go's
// reason: the payload is an untagged run of MAXALIGNed index tuples framed only
// by each tuple's own `t_info` size, so a disagreement between the halves
// mis-BUILDS the page rather than failing to parse.
//
// Why a DESCRIPTION rather than a straight encoding. Upstream's record says
// "origpage's items below firstrightoff, with newitem spliced in at newitemoff,
// under this new high key", and its redo rebuilds the left half from the page
// it already holds. goopg's `splitPage` does not work that way: it reads the
// whole page out, appends the new item, runs a DEDUP CONSOLIDATION pass over
// the merged list and refills both halves — which can produce posting tuples
// that were never on the original page, drop LP_DEAD-marked items that upstream
// would have copied, and (on a root split) leave BTP_ROOT set where upstream's
// `_bt_split` clears it. None of that is describable by three offsets.
//
// So the encoder does not assert the description is right; it DERIVES one from
// the three pages it holds and then replays it against a copy of the pre-split
// page, comparing the result with the left half the primary actually wrote —
// `CheckVacuumDelete`'s discipline (M0130-S11.5c), for the same reason. When
// the answer is no, block 0 stays a full-page image, which is upstream-LEGAL:
// its redo reaches the incremental arm only under BLK_NEEDS_REDO, and a
// restored image skips it along with all three offsets.
//
// Everything here is FORMAT-FREE (pgsplit.go's reason): recovery holds a
// relfilenode and no catalog, so items move as raw bytes and are never parsed
// as keys.

import (
	"bytes"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// SplitLeftDescription is upstream's block-0 story about a split: the three
// offsets of `xl_btree_split` that describe the left half, plus the two tuples
// its block data carries.
//
// Offsets are PHYSICAL offset numbers in the PRE-SPLIT page's numbering, which
// is what upstream's redo loop iterates (`for off = P_FIRSTDATAKEY(oopaque);
// off < xlrec->firstrightoff; off++`).
type SplitLeftDescription struct {
	// FirstRightOff is the first pre-split offset number that moved to the new
	// right page. The items copied to the left half are exactly
	// [P_FIRSTDATAKEY, FirstRightOff).
	FirstRightOff uint16
	// NewItemOff is the offset the newly inserted item would have occupied on
	// the pre-split page. It is meaningful on both sides: when the item landed
	// on the right half it is >= FirstRightOff and redo ignores it, exactly as
	// upstream's does.
	NewItemOff uint16
	// NewItemOnLeft selects XLOG_BTREE_SPLIT_L over _SPLIT_R and decides
	// whether the new item is carried in the block data at all — upstream
	// registers it only under `newitemonleft || postingoff != 0`.
	NewItemOnLeft bool
	// NewItem is the raw inserted tuple (carried only when NewItemOnLeft).
	NewItem []byte
	// HighKey is the left page's new high key — the separator the split just
	// installed. Always carried: it is the one item on the rebuilt page that
	// does not come from the pre-split page.
	HighKey []byte
}

// DescribeSplitLeft derives the block-0 description of a split from the three
// pages involved, and returns an error when the split the primary performed is
// not one upstream's record can describe (see the file header). The caller is
// expected to treat that error as "log a full-page image", not as a failure.
//
// prePage is the split page as it was BEFORE the rewrite; leftPage and
// rightPage are the two halves as written; newItem is the raw item whose
// insertion caused the split.
//
// The derivation is a three-way reconciliation, not a guess: the two halves'
// data items concatenated must equal the pre-split page's data items with
// newItem inserted at exactly one position. That single position IS
// newitemoff, and how many pre-split items precede the halves' boundary IS
// firstrightoff. Anything else — a dropped dead item, a posting list the dedup
// pass merged, a reordering — fails to reconcile and is reported.
func DescribeSplitLeft(prePage, leftPage, rightPage storage.Page, newItem []byte) (SplitLeftDescription, error) {
	var d SplitLeftDescription
	if len(prePage) != storage.BlockSize || len(leftPage) != storage.BlockSize || len(rightPage) != storage.BlockSize {
		return d, fmt.Errorf("btree: split-left describe needs three %d-byte pages", storage.BlockSize)
	}
	if len(newItem) < SizeOfIndexTupleData {
		return d, fmt.Errorf("btree: split-left describe: new item %d bytes < header %d", len(newItem), SizeOfIndexTupleData)
	}
	preItems, err := pgDataItemRaws(prePage)
	if err != nil {
		return d, err
	}
	leftItems, err := pgDataItemRaws(leftPage)
	if err != nil {
		return d, err
	}
	rightItems, err := pgDataItemRaws(rightPage)
	if err != nil {
		return d, err
	}
	if len(leftItems)+len(rightItems) != len(preItems)+1 {
		return d, fmt.Errorf("btree: split-left describe: halves hold %d+%d items, pre-split page had %d + the new item",
			len(leftItems), len(rightItems), len(preItems))
	}
	// Walk the merged halves against the pre-split items, allowing exactly one
	// position where the new item was spliced in.
	spliceAt := -1
	i := 0
	for j, m := range append(append([][]byte(nil), leftItems...), rightItems...) {
		if i < len(preItems) && bytes.Equal(m, preItems[i]) {
			i++
			continue
		}
		if spliceAt < 0 && bytes.Equal(m, newItem) {
			spliceAt = i
			if j < len(leftItems) {
				d.NewItemOnLeft = true
			}
			continue
		}
		return d, fmt.Errorf("btree: split-left describe: merged item %d is neither the next pre-split item nor the new item", j)
	}
	if spliceAt < 0 || i != len(preItems) {
		return d, fmt.Errorf("btree: split-left describe: the new item is not the halves' only addition")
	}
	hk, hasHK, err := PGHighKeyRaw(leftPage)
	if err != nil {
		return d, err
	}
	if !hasHK {
		return d, fmt.Errorf("btree: split-left describe: the left half has no high key")
	}
	first := pgFirstDataSlot(prePage)
	preOnLeft := len(leftItems)
	if d.NewItemOnLeft {
		preOnLeft--
	}
	d.FirstRightOff = first + uint16(preOnLeft)
	d.NewItemOff = first + uint16(spliceAt)
	d.HighKey = hk
	if d.NewItemOnLeft {
		d.NewItem = append([]byte(nil), newItem...)
	}
	return d, nil
}

// CheckSplitLeft reports whether the description REPRODUCES the left half the
// primary wrote: it replays the description against a copy of the pre-split
// page and compares item for item, plus the high key and the opaque header.
//
// This is where a goopg split that upstream's record cannot express is caught —
// the dedup pass having merged items, a dead item dropped by the rewrite, or a
// ROOT split (upstream's `_bt_split` clears BTP_ROOT on the left half and
// goopg's clears it in a later step, so the two pages disagree at this LSN).
// Enumerating those cases at the emit site and hoping the list stays complete
// is exactly what CheckVacuumDelete refuses to do.
func CheckSplitLeft(prePage, leftPage storage.Page, level uint32, rightBlk storage.BlockNumber, d SplitLeftDescription) error {
	if len(prePage) != storage.BlockSize || len(leftPage) != storage.BlockSize {
		return fmt.Errorf("btree: split-left check needs two %d-byte pages", storage.BlockSize)
	}
	replayed := make(storage.Page, storage.BlockSize)
	copy(replayed, prePage)
	if err := ReplaySplitLeftPage(replayed, level, rightBlk, d); err != nil {
		return err
	}
	if got, want := readOpaque(replayed), readOpaque(leftPage); got != want {
		return fmt.Errorf("btree: split-left replay opaque %+v != written %+v", got, want)
	}
	gotHK, gotHas, err := PGHighKeyRaw(replayed)
	if err != nil {
		return err
	}
	wantHK, wantHas, err := PGHighKeyRaw(leftPage)
	if err != nil {
		return err
	}
	if gotHas != wantHas || !bytes.Equal(gotHK, wantHK) {
		return fmt.Errorf("btree: split-left replay high key differs")
	}
	gotItems, err := pgDataItemRaws(replayed)
	if err != nil {
		return err
	}
	wantItems, err := pgDataItemRaws(leftPage)
	if err != nil {
		return err
	}
	if len(gotItems) != len(wantItems) {
		return fmt.Errorf("btree: split-left replay left %d items, written page has %d", len(gotItems), len(wantItems))
	}
	for i := range gotItems {
		if !bytes.Equal(gotItems[i], wantItems[i]) {
			return fmt.Errorf("btree: split-left replay item %d differs from the written page", i+1)
		}
	}
	return nil
}

// SplitLeftIsIncremental answers the single question the ENCODER and the
// PRIMARY must agree on: does the split record describe the left half
// incrementally (block 0 carries data, no image), or does it fall back to a
// full-page image of it?
//
// The encoder asks because it decides the record's shape. The primary asks
// (M0131-S26b) because the answer decides which pd_lsn stamp is legal for the
// left slot: advancing the slot's native-image watermark asserts "an image of
// this page exists in WAL at that LSN", which is true only under the image
// form. Under the incremental form the page owes its first-touch FPI for the
// epoch and MarkDirtyCoveredByRecordLocked must be used instead.
//
// It exists so the two askers cannot drift: both call THIS function rather than
// each re-deriving the condition. ok=false means "image form" for every reason —
// a nil pre-page or new item (bulk/pre-runtime callers), a split upstream's
// record cannot express, or a description that fails to reproduce the page the
// primary wrote.
func SplitLeftIsIncremental(prePage, leftPage, rightPage storage.Page, newItem []byte, level uint32, rightBlk storage.BlockNumber) (SplitLeftDescription, bool) {
	desc, err := DescribeSplitLeft(prePage, leftPage, rightPage, newItem)
	if err != nil {
		return SplitLeftDescription{}, false
	}
	if CheckSplitLeft(prePage, leftPage, level, rightBlk, desc) != nil {
		return SplitLeftDescription{}, false
	}
	return desc, true
}

// SplitLeftBlockData builds the block-0 payload upstream registers in
// `_bt_split` (nbtinsert.c:1990-2010): the new item when it landed on the left
// half, then the left page's new high key, each padded to MAXALIGN.
//
// The order is upstream's and is load-bearing — its redo peels the new item off
// the front under `newitemonleft || postingoff != 0` and takes the high key
// from whatever remains.
func SplitLeftBlockData(d SplitLeftDescription) []byte {
	out := make([]byte, 0, MaxAlign(len(d.NewItem))+MaxAlign(len(d.HighKey)))
	if d.NewItemOnLeft {
		out = append(out, d.NewItem...)
		out = append(out, make([]byte, MaxAlign(len(d.NewItem))-len(d.NewItem))...)
	}
	out = append(out, d.HighKey...)
	out = append(out, make([]byte, MaxAlign(len(d.HighKey))-len(d.HighKey))...)
	return out
}

// ParseSplitLeftBlockData is the consumer half of SplitLeftBlockData
// (nbtxlog.c:330-363). `newItemOnLeft` comes from the record's INFO byte
// (XLOG_BTREE_SPLIT_L vs _SPLIT_R), not from the payload: the run is untagged,
// so the reader has to be told how many tuples to expect.
//
// Upstream asserts the payload is exactly consumed; a trailing remainder means
// producer and consumer disagree about the framing and is rejected rather than
// ignored.
func ParseSplitLeftBlockData(data []byte, newItemOnLeft bool) (newItem, highKey []byte, err error) {
	off := 0
	take := func(what string) ([]byte, error) {
		if off+SizeOfIndexTupleData > len(data) {
			return nil, fmt.Errorf("btree: split-left parse: %d bytes left for %s < header %d", len(data)-off, what, SizeOfIndexTupleData)
		}
		size := PGIndexTupleSize(data[off:])
		if size < SizeOfIndexTupleData || off+size > len(data) {
			return nil, fmt.Errorf("btree: split-left parse: %s claims %d bytes (remaining %d)", what, size, len(data)-off)
		}
		raw := append([]byte(nil), data[off:off+size]...)
		off += MaxAlign(size)
		return raw, nil
	}
	if newItemOnLeft {
		if newItem, err = take("new item"); err != nil {
			return nil, nil, err
		}
	}
	if highKey, err = take("high key"); err != nil {
		return nil, nil, err
	}
	if off != len(data) {
		return nil, nil, fmt.Errorf("btree: split-left parse: %d trailing bytes", len(data)-off)
	}
	return newItem, highKey, nil
}

// ReplaySplitLeftPage is upstream's left-half rebuild (nbtxlog.c:365-425),
// applied to the page as it was before the split: copy the pre-split items
// below `FirstRightOff` in offset order, splicing the new item in at
// `NewItemOff` when it belongs on this half, under the record's new high key.
//
// Upstream builds a temp page and swaps it in "to retain the same physical
// order of the tuples that they had"; goopg's `resetPageItems` + append
// sequence is the same thing on the page itself, and is the sequence
// `splitPage` uses on the primary — which is what makes the two pages
// comparable item for item (CheckSplitLeft).
//
// The opaque header is stamped exactly as upstream stamps it: flags become
// BTP_INCOMPLETE_SPLIT (plus BTP_LEAF at level 0) and NOTHING else, so BTP_ROOT
// and the garbage hint are dropped, and btpo_next becomes the new right page.
// btpo_prev and btpo_level are left alone — the split does not move the page.
func ReplaySplitLeftPage(page storage.Page, level uint32, rightBlk storage.BlockNumber, d SplitLeftDescription) error {
	if len(d.HighKey) < SizeOfIndexTupleData {
		return fmt.Errorf("btree: replay split-left: high key %d bytes < header %d", len(d.HighKey), SizeOfIndexTupleData)
	}
	if d.NewItemOnLeft && len(d.NewItem) < SizeOfIndexTupleData {
		return fmt.Errorf("btree: replay split-left: new item %d bytes < header %d", len(d.NewItem), SizeOfIndexTupleData)
	}
	first := pgFirstDataSlot(page)
	count, err := PGDataItemCount(page)
	if err != nil {
		return err
	}
	if d.FirstRightOff < first || int(d.FirstRightOff-first) > count {
		return fmt.Errorf("btree: replay split-left: firstrightoff %d outside pre-split data range [%d,%d]",
			d.FirstRightOff, first, int(first)+count)
	}
	if d.NewItemOnLeft && (d.NewItemOff < first || d.NewItemOff > d.FirstRightOff) {
		return fmt.Errorf("btree: replay split-left: newitemoff %d outside left range [%d,%d]", d.NewItemOff, first, d.FirstRightOff)
	}
	kept := make([][]byte, 0, int(d.FirstRightOff-first)+1)
	for off := first; off < d.FirstRightOff; off++ {
		if d.NewItemOnLeft && off == d.NewItemOff {
			kept = append(kept, d.NewItem)
		}
		raw, err := pgGetItemRawAllowDead(page, off-first+1)
		if err != nil {
			return err
		}
		kept = append(kept, raw)
	}
	// Upstream's "cope with possibility that newitem goes at the end".
	if d.NewItemOnLeft && d.NewItemOff == d.FirstRightOff {
		kept = append(kept, d.NewItem)
	}

	resetPageItems(page)
	op := readOpaque(page)
	op.Next = rightBlk
	op.Flags = BTIncompleteSplit
	if level == 0 {
		op.Flags |= BTLeaf
	}
	writeOpaque(page, op)
	if err := pgSetHighKeyRaw(page, d.HighKey); err != nil {
		return fmt.Errorf("btree: replay split-left high key: %w", err)
	}
	for i, raw := range kept {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay split-left item %d: %w", i, err)
		}
	}
	return nil
}

// pgDataItemRaws returns a page's data items (high key excluded) as raw bytes
// in ascending offset order, LP_DEAD-marked items included — the split record
// describes the page PHYSICALLY, and upstream's redo copies dead items across
// like any other.
func pgDataItemRaws(p storage.Page) ([][]byte, error) {
	count, err := PGDataItemCount(p)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, count)
	for slot := uint16(1); slot <= uint16(count); slot++ {
		raw, err := pgGetItemRawAllowDead(p, slot)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}
