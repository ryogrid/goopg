// Package storage implements the goopg buffer manager (Pool) and the
// per-relation storage manager (Manager) it sits on. v0 scope is
// documented in docs/design/0005-buffer-manager.md; the on-disk page
// layout is documented in docs/design/0006-storage-format.md.
package storage

import (
	"encoding/binary"
	"fmt"
)

// BlockSize is the fixed page size, matching upstream's BLCKSZ
// (postgres/src/include/pg_config_manual.h). 8 KiB is the canonical
// PostgreSQL value and is what every operator-facing tool assumes.
const BlockSize = 8192

// SizeOfPageHeaderData mirrors postgres/src/include/storage/bufpage.h.
const SizeOfPageHeaderData = 24

// pgPageLayoutVersion mirrors PG_PAGE_LAYOUT_VERSION (4 as of upstream
// PG 18). It packs into pd_pagesize_version with BlockSize as the high
// bits.
const pgPageLayoutVersion = 4

// pdPagesizeVersion is the value written into pd_pagesize_version on a
// freshly initialised page. (BlockSize | layoutVersion).
const pdPagesizeVersion = BlockSize | pgPageLayoutVersion

// PageFlag is one bit of pd_flags.
type PageFlag uint16

const (
	PDHasFreeLines PageFlag = 0x0001
	PDPageFull     PageFlag = 0x0002
	PDAllVisible   PageFlag = 0x0004
)

// LSN is the 64-bit log sequence number stored in pd_lsn. Upstream
// stores it as two 32-bit halves in struct PageXLogRecPtr (high,low);
// we use a Go uint64 in memory and serialise (low, high) on the wire,
// matching upstream's PageSetLSN macro.
type LSN uint64

// BlockNumber is a relation-relative block index. Upstream uses
// uint32 (postgres/src/include/storage/block.h). 4 GiB / 8 KiB =
// 32 TiB per relation, the same hard limit upstream lives with.
type BlockNumber uint32

// InvalidBlockNumber is the sentinel for "no such block".
const InvalidBlockNumber BlockNumber = 0xFFFFFFFF

// ForkNumber identifies which fork of a relation a request targets.
// Mirrors upstream's ForkNumber enum.
type ForkNumber int8

const (
	MainFork          ForkNumber = 0
	FSMFork           ForkNumber = 1
	VisibilityMapFork ForkNumber = 2
	InitFork          ForkNumber = 3
)

// RelFileNode is the (database, relation, fork) triple used by smgr to
// resolve a backing file. Upstream calls this RelFileLocator; we keep
// the older name because it's shorter and the v0 codebase doesn't yet
// have to disambiguate from upstream's variants.
type RelFileNode struct {
	DBOid  uint32
	RelOid uint32
	Fork   ForkNumber
}

// BufferTag is RelFileNode + BlockNumber. Used as the buffer-pool
// hash-table key.
type BufferTag struct {
	Rel   RelFileNode
	Block BlockNumber
}

// Page is a fixed-size byte slice carrying one block of storage. The
// underlying memory is owned by a Pool (the buffer manager) or by a
// caller's allocator; the Page type itself is just a typed view.
type Page []byte

// PageHeader is a typed view over the first SizeOfPageHeaderData bytes
// of a Page. All accessors validate the page length so callers can
// receive a Page from anywhere without re-asserting size.
//
// Field offsets and semantics mirror upstream's PageHeaderData; see
// docs/design/0006-storage-format.md for the layout table.
type PageHeader struct {
	Page Page
}

// Header constructs a PageHeader view. Returns an error if the slice
// is the wrong length.
func Header(p Page) (PageHeader, error) {
	if len(p) != BlockSize {
		return PageHeader{}, fmt.Errorf("page is %d bytes, want %d", len(p), BlockSize)
	}
	return PageHeader{Page: p}, nil
}

// MustHeader is the panicking constructor for callers that have
// already validated page size (e.g. buffer-pool slot pages).
func MustHeader(p Page) PageHeader {
	h, err := Header(p)
	if err != nil {
		panic(err)
	}
	return h
}

// LSN reads pd_lsn. Upstream stores it as two 32-bit halves in
// (high, low) order using PageXLogRecPtr; the in-file byte order is
// little-endian high then little-endian low (a u64 LE).
func (h PageHeader) LSN() LSN {
	return LSN(binary.LittleEndian.Uint64(h.Page[0:8]))
}

// SetLSN writes pd_lsn.
func (h PageHeader) SetLSN(v LSN) {
	binary.LittleEndian.PutUint64(h.Page[0:8], uint64(v))
}

func (h PageHeader) Checksum() uint16 { return binary.LittleEndian.Uint16(h.Page[8:10]) }
func (h PageHeader) SetChecksum(v uint16) {
	binary.LittleEndian.PutUint16(h.Page[8:10], v)
}

func (h PageHeader) Flags() PageFlag { return PageFlag(binary.LittleEndian.Uint16(h.Page[10:12])) }
func (h PageHeader) SetFlags(v PageFlag) {
	binary.LittleEndian.PutUint16(h.Page[10:12], uint16(v))
}

func (h PageHeader) Lower() uint16 { return binary.LittleEndian.Uint16(h.Page[12:14]) }
func (h PageHeader) SetLower(v uint16) {
	binary.LittleEndian.PutUint16(h.Page[12:14], v)
}

func (h PageHeader) Upper() uint16 { return binary.LittleEndian.Uint16(h.Page[14:16]) }
func (h PageHeader) SetUpper(v uint16) {
	binary.LittleEndian.PutUint16(h.Page[14:16], v)
}

func (h PageHeader) Special() uint16 { return binary.LittleEndian.Uint16(h.Page[16:18]) }
func (h PageHeader) SetSpecial(v uint16) {
	binary.LittleEndian.PutUint16(h.Page[16:18], v)
}

func (h PageHeader) PagesizeVersion() uint16 {
	return binary.LittleEndian.Uint16(h.Page[18:20])
}
func (h PageHeader) SetPagesizeVersion(v uint16) {
	binary.LittleEndian.PutUint16(h.Page[18:20], v)
}

func (h PageHeader) PruneXID() uint32 { return binary.LittleEndian.Uint32(h.Page[20:24]) }
func (h PageHeader) SetPruneXID(v uint32) {
	binary.LittleEndian.PutUint32(h.Page[20:24], v)
}

// InitPage zeroes p and writes a freshly-initialised PageHeaderData.
// The page has no line pointers, no tuples, no special area: the full
// page (after the 24-byte header) is free.
func InitPage(p Page) error {
	if len(p) != BlockSize {
		return fmt.Errorf("page is %d bytes, want %d", len(p), BlockSize)
	}
	for i := range p {
		p[i] = 0
	}
	h := MustHeader(p)
	h.SetLower(SizeOfPageHeaderData)
	h.SetUpper(BlockSize)
	h.SetSpecial(BlockSize)
	h.SetPagesizeVersion(pdPagesizeVersion)
	return nil
}

// IsNew reports whether the page is bytes-zero (an uninitialised slot
// freshly read from disk before InitPage).
func IsNew(p Page) bool {
	if len(p) != BlockSize {
		return false
	}
	// PageIsNew in upstream checks pd_upper == 0.
	return MustHeader(p).Upper() == 0
}

// FreeSpace reports the number of contiguous free bytes between the
// line-pointer array and the tuple region.
func (h PageHeader) FreeSpace() int {
	upper := int(h.Upper())
	lower := int(h.Lower())
	if upper < lower {
		return 0
	}
	return upper - lower
}
