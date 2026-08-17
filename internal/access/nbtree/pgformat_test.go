package nbtree

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// The goldens below are bytes of a metapage written by a real
// PostgreSQL 18.3 — block 0 of an empty catalog B-tree index taken from the
// TPC-H reference cluster (bench/tpch/runtime/pgdata/base/1). Only the three
// regions this slice owns are reproduced here; the rest of the page is zero.
//
// Capturing the oracle's own bytes (rather than re-deriving them from
// nbtree.h) is the point: it pins the alignment hole at offset 28, the
// -1.0 float8 sentinel, and the 7 bytes of tail padding after
// btm_allequalimage — three details a hand-written encoder gets wrong
// silently, because every named field still round-trips.
const (
	// bytes 12..24 of PageHeaderData: pd_lower, pd_upper, pd_special,
	// pd_pagesize_version, pd_prune_xid.
	pgRealMetaHeaderTailHex = "4800f01ff01f042000000000"
	// BTMetaPageData at PageGetContents (offset 24), 48 bytes.
	pgRealMetaStructHex = "62310500040000000000000000000000000000000000000000000000" +
		"00000000000000000000f0bf0100000000000000"
	// BTPageOpaqueData at BLCKSZ-16.
	pgRealMetaOpaqueHex = "00000000000000000000000008000000"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	return b
}

// TestPGOpaqueLayoutMatchesUpstream pins the sizes that _bt_checkpage keys on.
// A change here is a change to the on-disk contract with a real PG.
func TestPGOpaqueLayoutMatchesUpstream(t *testing.T) {
	// Verified against the oracle headers with a sizeof/offsetof probe
	// compiled -Ipostgres/src/include: sizeof(BTPageOpaqueData)=16
	// (prev=0 next=4 level=8 flags=12 cycleid=14), sizeof(BTMetaPageData)=48
	// (magic=0 version=4 root=8 level=12 fastroot=16 fastlevel=20
	// delpages=24 heaptuples=32 allequalimage=40).
	if SizeOfBTPageOpaquePG != 16 {
		t.Fatalf("SizeOfBTPageOpaquePG = %d, want 16 (MAXALIGN(sizeof(BTPageOpaqueData)))", SizeOfBTPageOpaquePG)
	}
	if SizeOfBTMetaPageDataPG != 48 {
		t.Fatalf("SizeOfBTMetaPageDataPG = %d, want 48 (sizeof(BTMetaPageData))", SizeOfBTMetaPageDataPG)
	}
	if pgSpecialOffset != storage.BlockSize-16 {
		t.Fatalf("pgSpecialOffset = %d, want %d", pgSpecialOffset, storage.BlockSize-16)
	}
	// The upstream flag bits must not be confused with this package's legacy
	// set. Bit 3 is the one that used to collide: goopg spelled it
	// BTHasHighKey, upstream spells it BTP_META. The bit is now unclaimed on
	// the goopg side (high-key presence is derived from btpo_next), and no
	// legacy flag may ever map onto it again — a page that told a real PG it
	// was the metapage would fail every subsequent lookup.
	if BTPMeta != 0x0008 {
		t.Fatalf("BTPMeta = %#x, want 0x0008", BTPMeta)
	}
	for _, m := range flagTranslation {
		if m.pg == BTPMeta {
			t.Fatalf("legacy flag %#x translates to BTP_META", m.legacy)
		}
	}
	if BTPHalfDead == BTHalfDead {
		t.Fatalf("BTP_HALF_DEAD (%#x) must differ from the legacy BTHalfDead (%#x); "+
			"a migrating writer that reuses the legacy value marks pages BTP_SPLIT_END to PG",
			BTPHalfDead, BTHalfDead)
	}
}

// TestInitPGBTPageMatchesBtPageinit checks the page-header side of
// _bt_pageinit: pd_special and pd_upper both land at BLCKSZ-16, and the
// result satisfies the oracle's own _bt_checkpage test.
func TestInitPGBTPageMatchesBtPageinit(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatalf("InitPGBTPage: %v", err)
	}
	h := storage.MustHeader(p)
	if got := h.Special(); got != storage.BlockSize-16 {
		t.Fatalf("pd_special = %d, want %d", got, storage.BlockSize-16)
	}
	if got := h.Upper(); got != storage.BlockSize-16 {
		t.Fatalf("pd_upper = %d, want %d", got, storage.BlockSize-16)
	}
	if got := h.Lower(); got != storage.SizeOfPageHeaderData {
		t.Fatalf("pd_lower = %d, want %d", got, storage.SizeOfPageHeaderData)
	}
	if err := CheckPGBTPage(p, 1); err != nil {
		t.Fatalf("CheckPGBTPage on a freshly initialised page: %v", err)
	}
}

// TestCheckPGBTPageRejectsLegacyOpaque is the regression this whole slice
// exists for: goopg's OLD 272-byte opaque is exactly what a real PG rejects
// with XX002 "contains corrupted page at block 0". The size is spelled
// literally here rather than through btSpecialOffset because S11.2b pointed
// that constant at the upstream 16-byte layout — the very fix under test.
func TestCheckPGBTPageRejectsLegacyOpaque(t *testing.T) {
	const legacySpecialOffset = storage.BlockSize - 272
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	h := storage.MustHeader(p)
	h.SetUpper(uint16(legacySpecialOffset))
	h.SetSpecial(uint16(legacySpecialOffset))
	err := CheckPGBTPage(p, 0)
	if err == nil {
		t.Fatal("CheckPGBTPage accepted a legacy 272-byte opaque; _bt_checkpage would not")
	}

	// And a genuinely zero page is the other _bt_checkpage arm.
	zero := make(storage.Page, storage.BlockSize)
	if err := CheckPGBTPage(zero, 3); err == nil {
		t.Fatal("CheckPGBTPage accepted an all-zero page")
	}
}

// TestPGOpaqueRoundTrip covers every field, including btpo_cycleid, which the
// legacy opaque has no analogue for and which a partial encoder would drop.
func TestPGOpaqueRoundTrip(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	want := PGBTPageOpaque{
		Prev:    12345,
		Next:    67890,
		Level:   7,
		Flags:   BTPLeaf | BTPHasGarbage | BTPIncompleteSplit,
		CycleID: 0xABCD,
	}
	WritePGOpaque(p, want)
	if got := ReadPGOpaque(p); got != want {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
	// Byte-level check of the field offsets, independent of the decoder.
	off := pgSpecialOffset
	if v := binary.LittleEndian.Uint32(p[off+8 : off+12]); v != 7 {
		t.Fatalf("btpo_level not at offset 8 (read %d)", v)
	}
	if v := binary.LittleEndian.Uint16(p[off+14 : off+16]); v != 0xABCD {
		t.Fatalf("btpo_cycleid not at offset 14 (read %#x)", v)
	}
	// A leftmost/rightmost page uses P_NONE == 0, not InvalidBlockNumber.
	WritePGOpaque(p, PGBTPageOpaque{Prev: PNone, Next: PNone, Flags: BTPLeaf | BTPRoot})
	o := ReadPGOpaque(p)
	if !o.IsLeftmost() || !o.IsRightmost() {
		t.Fatalf("P_NONE siblings not recognised: %+v", o)
	}
	if PNone == storage.InvalidBlockNumber {
		t.Fatal("P_NONE must be 0, not InvalidBlockNumber")
	}
}

// TestInitPGMetaPageMatchesRealPG byte-compares an initialised metapage
// against one a real PostgreSQL 18.3 wrote.
func TestInitPGMetaPageMatchesRealPG(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	// The captured page is an empty index: root/level/fastroot/fastlevel all
	// zero, allequalimage true.
	if err := InitPGMetaPage(p, 0, 0, true); err != nil {
		t.Fatalf("InitPGMetaPage: %v", err)
	}

	if got, want := p[12:24], mustHex(t, pgRealMetaHeaderTailHex); !bytes.Equal(got, want) {
		t.Fatalf("page header tail:\n got %x\nwant %x", got, want)
	}
	off := storage.SizeOfPageHeaderData
	if got, want := p[off:off+SizeOfBTMetaPageDataPG], mustHex(t, pgRealMetaStructHex); !bytes.Equal(got, want) {
		t.Fatalf("BTMetaPageData:\n got %x\nwant %x", got, want)
	}
	if got, want := p[pgSpecialOffset:], mustHex(t, pgRealMetaOpaqueHex); !bytes.Equal(got, want) {
		t.Fatalf("metapage opaque:\n got %x\nwant %x", got, want)
	}
	// Everything between pd_lower and pd_special is untouched free space.
	for i := off + SizeOfBTMetaPageDataPG; i < pgSpecialOffset; i++ {
		if p[i] != 0 {
			t.Fatalf("non-zero byte %#x at offset %d in the metapage free space", p[i], i)
		}
	}
	if err := CheckPGBTPage(p, 0); err != nil {
		t.Fatalf("CheckPGBTPage on the metapage: %v", err)
	}
}

// TestPGMetaPageRoundTrip exercises the non-default field values that the
// empty-index golden cannot cover, and pins the -1.0 sentinel.
func TestPGMetaPageRoundTrip(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	want := PGBTMetaPage{
		Magic:                    BTreeMagicPG,
		Version:                  BTreeVersionPG,
		Root:                     41,
		Level:                    2,
		FastRoot:                 41,
		FastLevel:                2,
		LastCleanupNumDelpages:   9,
		LastCleanupNumHeapTuples: 12345.5,
		AllEqualImage:            false,
	}
	WritePGMetaPage(p, want)
	if got := ReadPGMetaPage(p); got != want {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}

	// A fresh metapage carries -1.0, not 0.0 — _bt_vacuum_needs_cleanup
	// treats 0 as "no heap tuples", which is a different decision.
	if err := InitPGMetaPage(p, 1, 0, true); err != nil {
		t.Fatalf("InitPGMetaPage: %v", err)
	}
	m := ReadPGMetaPage(p)
	if m.LastCleanupNumHeapTuples != -1.0 {
		t.Fatalf("btm_last_cleanup_num_heap_tuples = %v, want -1.0", m.LastCleanupNumHeapTuples)
	}
	if math.Signbit(m.LastCleanupNumHeapTuples) != true {
		t.Fatal("sentinel lost its sign bit")
	}
	if m.Root != 1 || m.FastRoot != 1 {
		t.Fatalf("root/fastroot = %d/%d, want 1/1", m.Root, m.FastRoot)
	}
	// pd_lower must sit past the struct so an FPI cannot compress the
	// metadata away (the comment upstream flags as "essential").
	if got := storage.MustHeader(p).Lower(); got != storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG {
		t.Fatalf("pd_lower = %d, want %d", got, storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG)
	}
}

// TestWritePGMetaPageClearsStalePadding guards the failure mode a
// field-by-field encoder cannot see: reusing a dirty buffer leaves garbage in
// the alignment hole and the tail padding, so the page stops being
// byte-identical to PG's even though every field decodes correctly.
func TestWritePGMetaPageClearsStalePadding(t *testing.T) {
	p := make(storage.Page, storage.BlockSize)
	for i := range p {
		p[i] = 0xFF
	}
	// WritePGMetaPage directly, NOT via InitPGMetaPage: the latter zeroes the
	// whole page through storage.InitPage first, which would hide the bug.
	// The real caller shape this models is a metapage rewrite in place (root
	// moved after a split), where the buffer already holds the old contents.
	WritePGMetaPage(p, PGBTMetaPage{
		Magic:                    BTreeMagicPG,
		Version:                  BTreeVersionPG,
		Root:                     0,
		Level:                    0,
		FastRoot:                 0,
		FastLevel:                0,
		LastCleanupNumDelpages:   0,
		LastCleanupNumHeapTuples: -1.0,
		AllEqualImage:            true,
	})
	off := storage.SizeOfPageHeaderData
	if got, want := p[off:off+SizeOfBTMetaPageDataPG], mustHex(t, pgRealMetaStructHex); !bytes.Equal(got, want) {
		t.Fatalf("stale bytes survived into BTMetaPageData:\n got %x\nwant %x", got, want)
	}
}
