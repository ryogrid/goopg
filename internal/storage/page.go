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
// we use a Go uint64 in memory and serialise the high 32 bits at
// offset 0 (LE uint32) and the low 32 bits at offset 4 (LE uint32),
// byte-for-byte matching PG18's PageXLogRecPtrSet / PageXLogRecPtrGet
// (postgres/src/include/storage/bufpage.h:100-112).
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

// RelFileNode is the (tablespace, database, relation, fork) tuple used by
// smgr to resolve a backing file. Upstream calls this RelFileLocator; we
// keep the older name because it's shorter and the v0 codebase doesn't yet
// have to disambiguate from upstream's variants.
//
// TblOid is 0 for the default tablespace (mirrors catalog.Table.Tablespace/
// catalog.Index.Tablespace's "0 means pg_default" convention — see
// resolveTablespaceClause in internal/executor/operators_ddl.go), in which
// case the file resolves under the existing base/<DBOid>/ layout unchanged.
// A non-zero TblOid routes through pg_tblspc/<TblOid>/... (M0122-0007
// tablespace physical relocation).
type RelFileNode struct {
	TblOid uint32
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

// LSN reads pd_lsn in PG18's two-uint32 PageXLogRecPtr layout:
// xlogid (high 32 bits) as LE uint32 at offset 0, xrecoff (low 32
// bits) as LE uint32 at offset 4. Mirrors PageXLogRecPtrGet at
// postgres/src/include/storage/bufpage.h:105.
func (h PageHeader) LSN() LSN {
	hi := binary.LittleEndian.Uint32(h.Page[0:4])
	lo := binary.LittleEndian.Uint32(h.Page[4:8])
	return LSN(uint64(hi)<<32 | uint64(lo))
}

// SetLSN writes pd_lsn in PG18's two-uint32 PageXLogRecPtr layout
// (high at offset 0, low at offset 4), matching PageXLogRecPtrSet at
// postgres/src/include/storage/bufpage.h:110. The previous u64-LE
// encoding shipped pages to PG with the two halves swapped, which
// surfaced as "xlog flush request <high>/0 is not satisfied" on a
// PG18 standby reading a basebackup-snapshot page (M0106-0010
// batched-54).
func (h PageHeader) SetLSN(v LSN) {
	binary.LittleEndian.PutUint32(h.Page[0:4], uint32(uint64(v)>>32))
	binary.LittleEndian.PutUint32(h.Page[4:8], uint32(uint64(v)))
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

// tagPartition returns the partition index (0–127) for a BufferTag.
// Uses FNV-1a mixing to spread tags across partitions.
// M0098-0003: 128 partitions replace the single poolMu + byTag.
func tagPartition(tag BufferTag) int {
	const fnvPrime = uint64(1099511628211)
	h := uint64(14695981039346656037)
	h ^= uint64(tag.Rel.DBOid)
	h *= fnvPrime
	h ^= uint64(tag.Rel.RelOid)
	h *= fnvPrime
	h ^= uint64(tag.Rel.Fork)
	h *= fnvPrime
	h ^= uint64(tag.Block)
	h *= fnvPrime
	return int(h & 127)
}
