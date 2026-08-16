package nbtree

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-b-ii guards: the B-tree redo entry points no
// longer know an index's key format.
//
// Before this slice, redo re-derived an insert's slot by COMPARING keys, which
// forced internal/wal to name a format — and it could only ever name the blob
// one, because recovery holds a relfilenode and the catalog that would turn one
// into a key descriptor is itself being replayed. Under the tuple format
// (3b-2c-ii-B2-c) that hard-wiring is not imprecise but wrong in two
// independent ways at once, so the fix is upstream's: place the recorded bytes
// at the recorded offset number (btree_xlog_insert, nbtxlog.c) and never
// compare anything.
//
// These tests pin the two properties that makes the offset replay trustworthy:
// it reproduces the writer's page exactly (in BOTH formats and on both page
// shapes), and it is the only thing that can — a by-key replay of a
// tuple-format page demonstrably files the item at the wrong slot.

// replayInsertCase is one emitted insert: the bytes and the offset number the
// writer placed them at, i.e. exactly what rides in xl_btree_insert.
type replayInsertCase struct {
	raw    []byte
	offnum uint16
}

// buildPageByKey inserts every item through the writer's own key-ordered path
// and returns the emitted (raw, offnum) records in emit order — the same values
// the three LogBtreeInsert call sites now pass, via pgPhysOffnum.
func buildPageByKey(t *testing.T, f indexFormat, page storage.Page, items []item) []replayInsertCase {
	t.Helper()
	recs := make([]replayInsertCase, 0, len(items))
	for _, it := range items {
		raw := f.marshal(it)
		idx, err := insertItemSorted(f, page, it)
		if err != nil {
			t.Fatalf("insertItemSorted: %v", err)
		}
		recs = append(recs, replayInsertCase{raw: raw, offnum: pgPhysOffnum(page, idx)})
	}
	return recs
}

// pageItemRaws is every physical item on the page, high key included, so a
// comparison covers the P_FIRSTDATAKEY bias rather than assuming it.
func pageItemRaws(t *testing.T, p storage.Page) [][]byte {
	t.Helper()
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		t.Fatalf("PageLinePointerCount: %v", err)
	}
	out := make([][]byte, 0, count)
	for slot := 1; slot <= count; slot++ {
		raw, err := storage.PageGetItemRawAllowDead(p, uint16(slot))
		if err != nil {
			t.Fatalf("PageGetItemRawAllowDead(%d): %v", slot, err)
		}
		out = append(out, raw)
	}
	return out
}

// newReplayPage is a leaf page shaped like the writer's: `next` decides whether
// it is rightmost, and a non-rightmost page carries a high key at P_HIKEY, so
// the recorded offset numbers are biased by one exactly as on a real page.
func newReplayPage(t *testing.T, next storage.BlockNumber) storage.Page {
	t.Helper()
	p := newPGPage(t, next)
	if next != PNone {
		if _, err := storage.PageAddItemRaw(p, PGBTPivotRaw([]byte("zzzz-high-key"), 0)); err != nil {
			t.Fatalf("add high key: %v", err)
		}
	}
	return p
}

// TestApplyInsertRecordAtReproducesWriterPage is the core guard: replaying the
// emitted (raw, offnum) pairs in emit order onto a fresh page of the same shape
// yields byte-identical items, in both formats and on both page shapes. The
// items are inserted in scrambled key order so the writer's slots really are
// interleaved and a "replay just appends" implementation cannot pass.
func TestApplyInsertRecordAtReproducesWriterPage(t *testing.T) {
	keys := []int32{50, 10, 90, 30, -7, 70, 20}
	desc := int4Desc()

	for _, tc := range []struct {
		name string
		f    indexFormat
		next storage.BlockNumber
	}{
		{"blob/rightmost", blobFormat, PNone},
		{"blob/has-high-key", blobFormat, 9},
		{"tuple/rightmost", indexFormat{desc: desc}, PNone},
		{"tuple/has-high-key", indexFormat{desc: desc}, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]item, 0, len(keys))
			for i, k := range keys {
				tid := storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: 1}
				if tc.f.tupleKeys() {
					items = append(items, item{ptr: tid, key: tup(t, desc.Attrs, [][]byte{int4Val(k)}, tid)})
					continue
				}
				items = append(items, item{ptr: tid, key: EncodeInt4(k)})
			}

			writer := newReplayPage(t, tc.next)
			recs := buildPageByKey(t, tc.f, writer, items)

			replay := newReplayPage(t, tc.next)
			for i, rec := range recs {
				if err := ApplyInsertRecordAt(replay, rec.raw, rec.offnum); err != nil {
					t.Fatalf("ApplyInsertRecordAt(#%d, offnum=%d): %v", i, rec.offnum, err)
				}
			}

			want, got := pageItemRaws(t, writer), pageItemRaws(t, replay)
			if len(want) != len(got) {
				t.Fatalf("replayed page has %d items, writer has %d", len(got), len(want))
			}
			for i := range want {
				if !bytes.Equal(want[i], got[i]) {
					t.Fatalf("physical slot %d: replay %x, writer %x", i+1, got[i], want[i])
				}
			}
		})
	}
}

// TestApplyInsertRecordAtBeatsByKeyReplayOnTupleFormat is the motivation, made
// executable. `oldByKeyReplay` below IS the retired ApplyInsertRecord body, and
// on a tuple-format page it puts the item at the wrong slot: int4 -1 is
// 0xffffffff on disk, so ordering the header-less bytes bytewise sorts it AFTER
// +1 while the descriptor's comparator sorts it before. A standby replaying
// this way would silently hold a differently-ordered index than its primary.
func TestApplyInsertRecordAtBeatsByKeyReplayOnTupleFormat(t *testing.T) {
	desc := int4Desc()
	f := indexFormat{desc: desc}

	// Descriptor order is -1 then +1; insert +1 first so the writer's own path
	// has to place -1 in front of it.
	var items []item
	for i, k := range []int32{1, -1} {
		tid := storage.ItemPointer{Block: storage.BlockNumber(200 + i), Offset: 1}
		items = append(items, item{ptr: tid, key: tup(t, desc.Attrs, [][]byte{int4Val(k)}, tid)})
	}
	writer := newReplayPage(t, PNone)
	recs := buildPageByKey(t, f, writer, items)

	offsetPage := newReplayPage(t, PNone)
	byKeyPage := newReplayPage(t, PNone)
	for i, rec := range recs {
		if err := ApplyInsertRecordAt(offsetPage, rec.raw, rec.offnum); err != nil {
			t.Fatalf("ApplyInsertRecordAt(#%d): %v", i, err)
		}
		if err := oldByKeyReplay(byKeyPage, rec.raw); err != nil {
			t.Fatalf("oldByKeyReplay(#%d): %v", i, err)
		}
	}

	want := pageItemRaws(t, writer)
	if got := pageItemRaws(t, offsetPage); !bytes.Equal(got[0], want[0]) {
		t.Fatalf("offset replay slot 1 = %x, want the writer's %x", got[0], want[0])
	}
	// The point of the slice: the retired path disagrees, so it was not a
	// stylistic difference that B2-c could have left alone.
	if got := pageItemRaws(t, byKeyPage); bytes.Equal(got[0], want[0]) {
		t.Fatalf("by-key replay agreed with the writer (%x) — the divergence this guard pins is gone; "+
			"re-derive the case (int4 negative-vs-positive on-disk byte order) before deleting it", got[0])
	}
}

// oldByKeyReplay is the pre-B2-b-ii replay: parse the item under the blob
// format and re-insert it by comparing keys. Kept here, in a test, as the
// counter-example the guard above needs.
func oldByKeyReplay(page storage.Page, raw []byte) error {
	it, err := blobFormat.parse(raw)
	if err != nil {
		return err
	}
	if !blobFormat.pageHasSpaceFor(page, it) {
		return fmt.Errorf("btree: replay of insert: page has no space for keyLen=%d", len(it.key))
	}
	mustInsertItemSorted(blobFormat, page, it)
	return nil
}

// TestApplyInsertRecordAtRejectsPlaceholderOffnum: records emitted before this
// slice carried offnum=0 because replay re-derived the slot. There is no way to
// honour one now and guessing would corrupt the page, so it must be a loud
// error rather than a slot-1 insert.
func TestApplyInsertRecordAtRejectsPlaceholderOffnum(t *testing.T) {
	page := newReplayPage(t, PNone)
	err := ApplyInsertRecordAt(page, PGBTItemRaw(EncodeInt4(1), storage.ItemPointer{Block: 1, Offset: 1}), 0)
	if err == nil {
		t.Fatal("ApplyInsertRecordAt(offnum=0) succeeded, want an error")
	}
	if count, _ := storage.PageLinePointerCount(page); count != 0 {
		t.Fatalf("rejected apply still added %d items", count)
	}
}

// TestMinusInfinityPivotIsFormatIndependent pins the byte identity
// ReplayRemoveParentDownlink relies on to rebuild a leftmost downlink without
// knowing the format: a zero-attribute pivot has no key bytes to encode, so
// blob and tuple marshalling produce the same tuple. If a future format change
// breaks this, the replay must take a format again.
func TestMinusInfinityPivotIsFormatIndependent(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	for _, child := range []storage.BlockNumber{1, 4096, 0x00FF00FF} {
		blob := PGBTPivotRaw(nil, child)
		tuple := f.marshal(downlinkItem(nil, child))
		if !bytes.Equal(blob, tuple) {
			t.Fatalf("child %d: blob pivot %x != tuple pivot %x", child, blob, tuple)
		}
		if len(blob) != SizeOfIndexTupleData {
			t.Fatalf("child %d: minus-infinity pivot is %d bytes, want %d", child, len(blob), SizeOfIndexTupleData)
		}
	}
}

// TestReplayRemoveParentDownlinkIsFormatFree drives the second redo entry point
// over a TUPLE-format internal page — the case the old format-taking version
// would have mis-marshalled — and pins both halves of its contract: surviving
// items keep their exact bytes, and a removed leftmost slot leaves the new
// first item as a minus-infinity pivot pointing at its own child.
func TestReplayRemoveParentDownlinkIsFormatFree(t *testing.T) {
	desc := int4Desc()
	f := indexFormat{desc: desc}

	// A real internal page: minus-infinity leftmost pivot, then keyed pivots.
	build := func(t *testing.T) (storage.Page, [][]byte) {
		t.Helper()
		p := newPGPage(t, 9)
		if _, err := storage.PageAddItemRaw(p, PGBTPivotRaw([]byte("zzzz"), 0)); err != nil {
			t.Fatalf("add high key: %v", err)
		}
		raws := [][]byte{f.marshal(downlinkItem(nil, 11))}
		for i, k := range []int32{40, 80} {
			child := storage.BlockNumber(12 + i)
			key := tup(t, desc.Attrs, [][]byte{int4Val(k)}, storage.ItemPointer{Block: child})
			raws = append(raws, f.marshal(item{ptr: storage.ItemPointer{Block: child}, key: key, pivot: true}))
		}
		for _, raw := range raws {
			if _, err := pgAddItemRaw(p, raw); err != nil {
				t.Fatalf("pgAddItemRaw: %v", err)
			}
		}
		return p, raws
	}

	t.Run("middle slot keeps every survivor verbatim", func(t *testing.T) {
		p, raws := build(t)
		if err := ReplayRemoveParentDownlink(p, 2); err != nil {
			t.Fatalf("ReplayRemoveParentDownlink: %v", err)
		}
		want := [][]byte{raws[0], raws[2]}
		assertDataItems(t, p, want)
	})

	t.Run("leftmost slot demotes the successor to minus infinity", func(t *testing.T) {
		p, raws := build(t)
		if err := ReplayRemoveParentDownlink(p, 1); err != nil {
			t.Fatalf("ReplayRemoveParentDownlink: %v", err)
		}
		// Slot 1 becomes a zero-attribute pivot still addressing block 12 (the
		// old slot 2's child); slot 2 is the old slot 3, untouched.
		assertDataItems(t, p, [][]byte{PGBTPivotRaw(nil, 12), raws[2]})
	})

	t.Run("out-of-range slot is an idempotent no-op", func(t *testing.T) {
		p, raws := build(t)
		if err := ReplayRemoveParentDownlink(p, 9); err != nil {
			t.Fatalf("ReplayRemoveParentDownlink: %v", err)
		}
		assertDataItems(t, p, raws)
	})
}

// assertDataItems compares the page's DATA items (high key excluded) against
// the expected raw bytes, and re-checks that the high key survived the page
// rewrite — resetPageItems preserves it, and a regression there would silently
// renumber every data slot.
func assertDataItems(t *testing.T, p storage.Page, want [][]byte) {
	t.Helper()
	count, err := PGDataItemCount(p)
	if err != nil {
		t.Fatalf("PGDataItemCount: %v", err)
	}
	if count != len(want) {
		t.Fatalf("page has %d data items, want %d", count, len(want))
	}
	for i, w := range want {
		got, err := pgGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("pgGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("data slot %d = %x, want %x", i+1, got, w)
		}
	}
	if _, ok, err := PGHighKeyRaw(p); err != nil || !ok {
		t.Fatalf("high key lost: ok=%v err=%v", ok, err)
	}
}
