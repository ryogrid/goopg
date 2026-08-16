package nbtree

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/goopg/goopg/internal/storage"
)

// This file holds the UPSTREAM nbtree on-disk layout, byte-for-byte, as the
// foundation slice (M0130-S11.1) of converting goopg's private B-tree format
// to PostgreSQL's. It is deliberately separate from the legacy layout in
// btree.go (SizeOfBTPageOpaque = 272, HighKey carried inside the opaque area)
// so the two can coexist while later slices migrate the writers.
//
// Why this matters: a real PG 18.3 attached to a goopg cluster directory runs
// _bt_checkpage (postgres/src/backend/access/nbtree/nbtpage.c) on every index
// page it reads, and that function errors with ERRCODE_INDEX_CORRUPTED
// ("contains corrupted page at block %u", XX002) unless
//
//	PageGetSpecialSize(page) == MAXALIGN(sizeof(BTPageOpaqueData))
//
// i.e. unless the special area is exactly 16 bytes. goopg's 272-byte opaque
// fails on the very first page, which is what blocks the reverse-standby
// index scan in TestE2E_PGStandbyFullCycle. See
// docs/design/0130-0011-nbtree-pg-on-disk-format.md.
//
// Every size and offset below was verified against the oracle headers by
// compiling a sizeof/offsetof probe with -Ipostgres/src/include (see the
// design doc's "Layout verification" section); the resulting numbers are
// asserted again in Go by TestPGOpaqueLayoutMatchesUpstream and
// byte-compared against a real PG-written metapage by
// TestInitPGMetaPageMatchesRealPG.

// SizeOfBTPageOpaquePG is MAXALIGN(sizeof(BTPageOpaqueData)) from
// postgres/src/include/access/nbtree.h. The struct is
//
//	offset 0  btpo_prev    BlockNumber (4)
//	offset 4  btpo_next    BlockNumber (4)
//	offset 8  btpo_level   uint32      (4)
//	offset 12 btpo_flags   uint16      (2)
//	offset 14 btpo_cycleid BTCycleId   (2)
//
// which is 16 bytes with no padding, so MAXALIGN is a no-op here.
const SizeOfBTPageOpaquePG = 16

// SizeOfBTMetaPageDataPG is sizeof(BTMetaPageData). The trailing bool sits at
// offset 40 and the struct is padded out to 48 by the float8 member's 8-byte
// alignment; upstream's _bt_initmetapage sets pd_lower to
// (metad + sizeof(BTMetaPageData)) - page, so the padding IS part of the
// on-disk footprint and must be reproduced.
const SizeOfBTMetaPageDataPG = 48

// pgSpecialOffset is where the 16-byte opaque area starts on a PG-format
// B-tree page.
const pgSpecialOffset = storage.BlockSize - SizeOfBTPageOpaquePG

// Upstream btpo_flags bits (nbtree.h BTP_*). These are NOT all identical to
// this package's legacy BT* flags: BTHasHighKey (0x0008) collides with
// upstream's BTP_META, and BTIncompleteSplit/BTHalfDead are swapped relative
// to BTP_HALF_DEAD/BTP_SPLIT_END. Only BTP_LEAF, BTP_ROOT, BTP_DELETED and
// BTP_HAS_GARBAGE happen to agree. Never mix the two sets.
const (
	BTPLeaf            uint16 = 1 << 0 // leaf page, i.e. not internal
	BTPRoot            uint16 = 1 << 1 // root page (has no parent)
	BTPDeleted         uint16 = 1 << 2 // page has been deleted from tree
	BTPMeta            uint16 = 1 << 3 // meta-page
	BTPHalfDead        uint16 = 1 << 4 // empty, but still in tree
	BTPSplitEnd        uint16 = 1 << 5 // rightmost page of split group
	BTPHasGarbage      uint16 = 1 << 6 // page has LP_DEAD tuples (deprecated)
	BTPIncompleteSplit uint16 = 1 << 7 // right sibling's downlink is missing
	BTPHasFullXID      uint16 = 1 << 8 // contains BTDeletedPageData
)

// PNone is upstream's P_NONE — the sibling-link value for a leftmost or
// rightmost page. Upstream spells it 0, NOT InvalidBlockNumber: block 0 is
// the metapage and can therefore never be a sibling, so 0 is free to mean
// "no sibling". goopg's legacy opaque uses storage.InvalidBlockNumber for
// the same purpose, so the migrating slices must translate.
const PNone storage.BlockNumber = 0

// PGBTPageOpaque is the typed view over an upstream-format special area.
type PGBTPageOpaque struct {
	Prev    storage.BlockNumber
	Next    storage.BlockNumber
	Level   uint32
	Flags   uint16
	CycleID uint16
}

// IsLeaf mirrors P_ISLEAF.
func (o PGBTPageOpaque) IsLeaf() bool { return o.Flags&BTPLeaf != 0 }

// IsRoot mirrors P_ISROOT.
func (o PGBTPageOpaque) IsRoot() bool { return o.Flags&BTPRoot != 0 }

// IsDeleted mirrors P_ISDELETED.
func (o PGBTPageOpaque) IsDeleted() bool { return o.Flags&BTPDeleted != 0 }

// IsMeta reports the BTP_META bit that _bt_initmetapage stamps on block 0.
func (o PGBTPageOpaque) IsMeta() bool { return o.Flags&BTPMeta != 0 }

// IsHalfDead mirrors P_ISHALFDEAD.
func (o PGBTPageOpaque) IsHalfDead() bool { return o.Flags&BTPHalfDead != 0 }

// IsLeftmost mirrors P_LEFTMOST (btpo_prev == P_NONE).
func (o PGBTPageOpaque) IsLeftmost() bool { return o.Prev == PNone }

// IsRightmost mirrors P_RIGHTMOST (btpo_next == P_NONE).
func (o PGBTPageOpaque) IsRightmost() bool { return o.Next == PNone }

// IncompleteSplit mirrors P_INCOMPLETE_SPLIT.
func (o PGBTPageOpaque) IncompleteSplit() bool { return o.Flags&BTPIncompleteSplit != 0 }

// ReadPGOpaque decodes the upstream-format opaque area from page bytes. It
// does NOT validate pd_special — use CheckPGBTPage for the _bt_checkpage
// equivalent — so a caller can inspect a page it already knows the shape of.
func ReadPGOpaque(p storage.Page) PGBTPageOpaque {
	off := pgSpecialOffset
	return PGBTPageOpaque{
		Prev:    storage.BlockNumber(binary.LittleEndian.Uint32(p[off : off+4])),
		Next:    storage.BlockNumber(binary.LittleEndian.Uint32(p[off+4 : off+8])),
		Level:   binary.LittleEndian.Uint32(p[off+8 : off+12]),
		Flags:   binary.LittleEndian.Uint16(p[off+12 : off+14]),
		CycleID: binary.LittleEndian.Uint16(p[off+14 : off+16]),
	}
}

// WritePGOpaque persists an upstream-format opaque area into page bytes.
func WritePGOpaque(p storage.Page, o PGBTPageOpaque) {
	off := pgSpecialOffset
	binary.LittleEndian.PutUint32(p[off:off+4], uint32(o.Prev))
	binary.LittleEndian.PutUint32(p[off+4:off+8], uint32(o.Next))
	binary.LittleEndian.PutUint32(p[off+8:off+12], o.Level)
	binary.LittleEndian.PutUint16(p[off+12:off+14], o.Flags)
	binary.LittleEndian.PutUint16(p[off+14:off+16], o.CycleID)
}

// InitPGBTPage is _bt_pageinit: PageInit(page, BLCKSZ, sizeof(BTPageOpaqueData)).
// It zeroes the page, writes a fresh PageHeaderData, and reserves the 16-byte
// special area by setting pd_special (and pd_upper) to BLCKSZ - 16.
//
// storage.InitPage cannot be reused as-is because it hardcodes a zero-length
// special area (pd_special = BLCKSZ).
func InitPGBTPage(p storage.Page) error {
	if err := storage.InitPage(p); err != nil {
		return err
	}
	h := storage.MustHeader(p)
	h.SetUpper(uint16(pgSpecialOffset))
	h.SetSpecial(uint16(pgSpecialOffset))
	return nil
}

// PGBTMetaPage is BTMetaPageData. LastCleanupNumHeapTuples is a float8 and is
// -1.0 on a freshly initialised metapage (upstream's "unknown" sentinel), not
// 0 — a detail amcheck and _bt_vacuum_needs_cleanup both read.
type PGBTMetaPage struct {
	Magic                    uint32
	Version                  uint32
	Root                     storage.BlockNumber
	Level                    uint32
	FastRoot                 storage.BlockNumber
	FastLevel                uint32
	LastCleanupNumDelpages   uint32
	LastCleanupNumHeapTuples float64
	AllEqualImage            bool
}

// Upstream nbtree.h version constants. BTreeMagic/BTreeVersion in btree.go
// already carry the same numeric values (goopg copied them early), but they
// are re-declared here against the upstream spelling so the PG-format code
// does not depend on the legacy metapage's constants surviving migration.
const (
	BTreeMagicPG        uint32 = 0x053162 // BTREE_MAGIC
	BTreeVersionPG      uint32 = 4        // BTREE_VERSION
	BTreeMinVersionPG   uint32 = 2        // BTREE_MIN_VERSION
	BTreeNoVacVersionPG uint32 = 3        // BTREE_NOVAC_VERSION
)

// ReadPGMetaPage decodes BTMetaPageData from block 0 of a PG-format index.
// The struct starts immediately after the page header (BTPageGetMeta is
// `(BTMetaPageData *) PageGetContents(page)`).
func ReadPGMetaPage(p storage.Page) PGBTMetaPage {
	off := storage.SizeOfPageHeaderData
	return PGBTMetaPage{
		Magic:                    binary.LittleEndian.Uint32(p[off : off+4]),
		Version:                  binary.LittleEndian.Uint32(p[off+4 : off+8]),
		Root:                     storage.BlockNumber(binary.LittleEndian.Uint32(p[off+8 : off+12])),
		Level:                    binary.LittleEndian.Uint32(p[off+12 : off+16]),
		FastRoot:                 storage.BlockNumber(binary.LittleEndian.Uint32(p[off+16 : off+20])),
		FastLevel:                binary.LittleEndian.Uint32(p[off+20 : off+24]),
		LastCleanupNumDelpages:   binary.LittleEndian.Uint32(p[off+24 : off+28]),
		LastCleanupNumHeapTuples: math.Float64frombits(binary.LittleEndian.Uint64(p[off+32 : off+40])),
		AllEqualImage:            p[off+40] != 0,
	}
}

// WritePGMetaPage encodes BTMetaPageData at PageGetContents(page). Bytes
// 28..32 (the alignment hole before the float8) and 41..48 (the tail padding)
// are explicitly zeroed: upstream palloc0's the page, and leaving stale bytes
// there would make a byte-comparison against a PG-written metapage fail even
// though every field agrees.
func WritePGMetaPage(p storage.Page, m PGBTMetaPage) {
	off := storage.SizeOfPageHeaderData
	binary.LittleEndian.PutUint32(p[off:off+4], m.Magic)
	binary.LittleEndian.PutUint32(p[off+4:off+8], m.Version)
	binary.LittleEndian.PutUint32(p[off+8:off+12], uint32(m.Root))
	binary.LittleEndian.PutUint32(p[off+12:off+16], m.Level)
	binary.LittleEndian.PutUint32(p[off+16:off+20], uint32(m.FastRoot))
	binary.LittleEndian.PutUint32(p[off+20:off+24], m.FastLevel)
	binary.LittleEndian.PutUint32(p[off+24:off+28], m.LastCleanupNumDelpages)
	binary.LittleEndian.PutUint32(p[off+28:off+32], 0) // alignment hole
	binary.LittleEndian.PutUint64(p[off+32:off+40], math.Float64bits(m.LastCleanupNumHeapTuples))
	p[off+40] = 0
	if m.AllEqualImage {
		p[off+40] = 1
	}
	for i := off + 41; i < off+SizeOfBTMetaPageDataPG; i++ {
		p[i] = 0 // tail padding
	}
}

// InitPGMetaPage is _bt_initmetapage: a PG-format page with BTP_META in the
// opaque, the metadata struct at PageGetContents, and pd_lower advanced past
// the struct so full-page-image compression does not drop the metadata.
func InitPGMetaPage(p storage.Page, root storage.BlockNumber, level uint32, allEqualImage bool) error {
	if err := InitPGBTPage(p); err != nil {
		return err
	}
	WritePGMetaPage(p, PGBTMetaPage{
		Magic:                    BTreeMagicPG,
		Version:                  BTreeVersionPG,
		Root:                     root,
		Level:                    level,
		FastRoot:                 root,
		FastLevel:                level,
		LastCleanupNumDelpages:   0,
		LastCleanupNumHeapTuples: -1.0,
		AllEqualImage:            allEqualImage,
	})
	WritePGOpaque(p, PGBTPageOpaque{Flags: BTPMeta})
	storage.MustHeader(p).SetLower(uint16(storage.SizeOfPageHeaderData + SizeOfBTMetaPageDataPG))
	return nil
}

// CheckPGBTPage is the Go equivalent of _bt_checkpage — the exact test a real
// PG 18.3 applies to every B-tree page it reads. Keeping it here lets goopg
// gate its own writers on the oracle's acceptance criterion instead of
// discovering the mismatch through an XX002 from a live standby.
func CheckPGBTPage(p storage.Page, block storage.BlockNumber) error {
	if len(p) != storage.BlockSize {
		return fmt.Errorf("btree: page at block %d is %d bytes, want %d", block, len(p), storage.BlockSize)
	}
	if storage.IsNew(p) {
		return fmt.Errorf("btree: index contains unexpected zero page at block %d", block)
	}
	h := storage.MustHeader(p)
	if int(storage.BlockSize)-int(h.Special()) != SizeOfBTPageOpaquePG {
		return fmt.Errorf("btree: index contains corrupted page at block %d: special size %d, want %d",
			block, int(storage.BlockSize)-int(h.Special()), SizeOfBTPageOpaquePG)
	}
	return nil
}
