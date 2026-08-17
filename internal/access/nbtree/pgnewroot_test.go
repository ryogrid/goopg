package nbtree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// buildRestoreTestPage lays out a page with the given item bodies at ascending
// offset numbers, exactly as a writer would.
func buildRestoreTestPage(t *testing.T, keys ...string) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatal(err)
	}
	WritePGOpaque(p, PGBTPageOpaque{Prev: PNone, Next: PNone, Level: 1, Flags: BTPRoot})
	for _, k := range keys {
		var raw []byte
		if k == "" {
			raw = PGBTPivotRaw(nil, 7)
		} else {
			raw = PGBTPivotRaw([]byte(k), 8)
		}
		if _, err := storage.PageAddItemRaw(p, raw); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// TestPGRestorePageDataRoundTrip is the sibling-pair guard for the untagged
// payload `_bt_restore_page` consumes: it has no count, no offsets and no
// terminator, so producer and consumer agree only by both using the tuple's own
// t_info size with a MAXALIGN stride. A disagreement is a silently mis-built
// page, never a parse error, so the round trip has to be pinned directly.
//
// Keys of deliberately mixed length put at least one item on a non-MAXALIGN
// boundary; without the stride the second item's header would be read from the
// first item's padding.
func TestPGRestorePageDataRoundTrip(t *testing.T) {
	page := buildRestoreTestPage(t, "", "a", "bcde", "fghijklmn", "op")

	data, err := PGRestorePageData(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%8 != 0 {
		t.Errorf("payload length %d is not a MAXALIGN multiple", len(data))
	}
	items, err := PGParseRestorePageData(data)
	if err != nil {
		t.Fatal(err)
	}
	n, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != n {
		t.Fatalf("parsed %d items, want %d", len(items), n)
	}
	for slot := 1; slot <= n; slot++ {
		want, err := storage.PageGetItemRaw(page, uint16(slot))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(items[slot-1], want) {
			t.Errorf("item at offset %d does not round-trip", slot)
		}
	}

	// The payload is in DESCENDING offset order — upstream compensates by
	// adding the items to the page in reverse. Pin that, or a producer that
	// emitted ascending order would still "round-trip" through a matching
	// consumer while producing pages a real PG reverses.
	first := PGIndexTupleSize(data)
	last, err := storage.PageGetItemRaw(page, uint16(n))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:first], last) {
		t.Errorf("payload does not start with the LAST item; _bt_restore_page would reverse the page")
	}

	// Feeding the parsed items to the replay page builder reproduces the
	// writer's page item for item.
	replayed := make(storage.Page, storage.BlockSize)
	if err := ReplayNewRootPage(replayed, 1, items); err != nil {
		t.Fatal(err)
	}
	for slot := 1; slot <= n; slot++ {
		want, err := storage.PageGetItemRaw(page, uint16(slot))
		if err != nil {
			t.Fatal(err)
		}
		got, err := storage.PageGetItemRaw(replayed, uint16(slot))
		if err != nil {
			t.Fatalf("replayed page has no offset %d: %v", slot, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("replayed offset %d differs from the writer's", slot)
		}
	}
}

// TestPGParseRestorePageDataRejectsMalformed keeps a truncated or over-long size
// field from producing a short page instead of an error — a partially restored
// root is a lost subtree.
func TestPGParseRestorePageDataRejectsMalformed(t *testing.T) {
	page := buildRestoreTestPage(t, "", "abc")
	data, err := PGRestorePageData(page)
	if err != nil {
		t.Fatal(err)
	}
	for name, corrupt := range map[string][]byte{
		"truncated run":  data[:len(data)-4],
		"header only":    data[:SizeOfIndexTupleData-1],
		// t_info is the uint16 at offset 6; its low 13 bits (IndexSizeMask) are
		// the tuple size, so raising the high size bits claims ~8 KiB.
		"oversized size": func() []byte { c := append([]byte(nil), data...); c[7] |= 0x1F; return c }(),
	} {
		if _, err := PGParseRestorePageData(corrupt); err == nil {
			t.Errorf("%s: want an error, got a successful parse", name)
		}
	}
}

// TestReplayRestoreMetaPageRebuildsFromRecord pins _bt_restore_meta's two
// non-obvious properties: the metapage is rebuilt from scratch (not
// read-modify-written, so stale fields cannot survive), and pd_lower is advanced
// past the struct — without which a later full-page image would compress the
// metadata into the free-space hole and lose it.
func TestReplayRestoreMetaPageRebuildsFromRecord(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGMetaPage(p, 1, 0, false); err != nil {
		t.Fatal(err)
	}
	// Stale state a read-modify-write would carry forward.
	stale := ReadPGMetaPage(p)
	stale.LastCleanupNumHeapTuples = 12345
	stale.LastCleanupNumDelpages = 9
	WritePGMetaPage(p, stale)

	want := PGBTMetaPage{Version: BTreeVersionPG, Root: 42, Level: 3, FastRoot: 42, FastLevel: 3, AllEqualImage: true}
	if err := ReplayRestoreMetaPage(p, want); err != nil {
		t.Fatal(err)
	}
	got := ReadPGMetaPage(p)
	if got.Magic != BTreeMagicPG || got.Root != 42 || got.FastRoot != 42 || got.Level != 3 || got.FastLevel != 3 || !got.AllEqualImage {
		t.Errorf("metapage = %+v, want root/fastroot 42 level 3 allequalimage", got)
	}
	if got.LastCleanupNumDelpages != 0 {
		t.Errorf("btm_last_cleanup_num_delpages = %d, want 0 from the record", got.LastCleanupNumDelpages)
	}
	if got.LastCleanupNumHeapTuples != -1.0 {
		t.Errorf("btm_last_cleanup_num_heap_tuples = %v, want -1 (upstream does not log it)", got.LastCleanupNumHeapTuples)
	}
	if !ReadPGOpaque(p).IsMeta() {
		t.Errorf("BTP_META not set")
	}
	if lower := storage.MustHeader(p).Lower(); int(lower) != storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG {
		t.Errorf("pd_lower = %d, want %d", lower, storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG)
	}
	if err := ReplayRestoreMetaPage(p, PGBTMetaPage{Version: 99}); err == nil {
		t.Errorf("want an error for an out-of-range btm_version")
	}
}

// TestReplayClearIncompleteSplitIsIdempotent covers the block-1 limb: it must
// clear the flag when set and leave every other opaque field alone, and it must
// be a no-op on a page goopg's runtime already cleared.
func TestReplayClearIncompleteSplitIsIdempotent(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatal(err)
	}
	WritePGOpaque(p, PGBTPageOpaque{Prev: 5, Next: 6, Level: 2, Flags: BTPIncompleteSplit | BTPHasGarbage})
	if err := ReplayClearIncompleteSplit(p); err != nil {
		t.Fatal(err)
	}
	op := ReadPGOpaque(p)
	if op.Flags&BTPIncompleteSplit != 0 {
		t.Errorf("incomplete-split flag still set")
	}
	if op.Flags&BTPHasGarbage == 0 || op.Prev != 5 || op.Next != 6 || op.Level != 2 {
		t.Errorf("opaque = %+v, want prev 5 next 6 level 2 with BTP_HAS_GARBAGE kept", op)
	}
	before := append([]byte(nil), p...)
	if err := ReplayClearIncompleteSplit(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, p) {
		t.Errorf("second clear mutated the page")
	}
}
