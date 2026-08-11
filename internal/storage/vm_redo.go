package storage

import "fmt"

// VM redo helpers (M0131-S21a-2) — ports of visibilitymap.c's HEAPBLK_TO_*
// macros plus the two bit mutations WAL redo needs: the OR inside
// visibilitymap_set and the AND-NOT inside visibilitymap_clear.
//
// These are separate from the runtime VisibilityMap (vm.go), which is an
// in-memory map keyed by (relation, heap block) and is written out wholesale by
// VMSaveForks. Redo has no VisibilityMap handle — wal.ApplyRecord is given only
// a *storage.Manager — and it must not need one: crash recovery runs BEFORE
// Runtime.VM is populated (internal/initdb/open.go replays at line ~380 and
// calls VMLoadForks at ~2472), so a redo that mutates the on-disk _vm fork is
// picked up by the subsequent load. Mutating the fork page is therefore both
// the PG-faithful thing (upstream's redo writes the vm buffer) and the only
// thing that survives into the running server.
//
// The on-disk layout is already PG's, see vm_fork.go: 2 bits per heap block,
// 4 heap blocks per byte, data starting at MAXALIGN(SizeOfPageHeaderData) —
// PageGetContents. Only the addressing arithmetic is new here.
const (
	// VMHeapBlocksPerPage is upstream's HEAPBLOCKS_PER_PAGE: how many heap
	// blocks one visibility-map page covers (visibilitymap.c:107-108).
	VMHeapBlocksPerPage = vmMaxHeapPagesPerPage

	// VMValidBits is VISIBILITYMAP_VALID_BITS (visibilitymapdefs.h:22) — the
	// OR of every bit that may legally be stored in the map. The XLOG-only
	// flags (VISIBILITYMAP_XLOG_CATALOG_REL) must be masked off before a
	// record's flags byte reaches the page.
	VMValidBits uint8 = VMAllVisible | VMAllFrozen
)

// VMBlockForHeapBlock is HEAPBLK_TO_MAPBLOCK: which visibility-map fork block
// holds heapBlk's bits. Redo does not normally need it — the record carries the
// vm block number in its own block reference — but the heap-lock path, which
// only ever names the heap block, does.
func VMBlockForHeapBlock(heapBlk BlockNumber) BlockNumber {
	return BlockNumber(uint64(heapBlk) / uint64(VMHeapBlocksPerPage))
}

// vmBitPosition is HEAPBLK_TO_MAPBYTE + HEAPBLK_TO_OFFSET, resolved to an
// absolute byte index inside the page (i.e. PageGetContents already applied).
func vmBitPosition(heapBlk BlockNumber) (byteIdx int, shift uint) {
	within := int(uint64(heapBlk) % uint64(VMHeapBlocksPerPage))
	return vmDataOffset + within/VMPagesPerByte, uint((within % VMPagesPerByte) * VMBitsPerHeapPage)
}

// VMPageBits returns heapBlk's two visibility bits as stored in the vm page.
func VMPageBits(p Page, heapBlk BlockNumber) (uint8, error) {
	if len(p) != BlockSize {
		return 0, fmt.Errorf("vm page: bad length %d (want %d)", len(p), BlockSize)
	}
	byteIdx, shift := vmBitPosition(heapBlk)
	return (p[byteIdx] >> shift) & VMValidBits, nil
}

// VMPageSetBits is the page mutation inside visibilitymap_set
// (visibilitymap.c): OR the record's bits into heapBlk's slot. Upstream
// compares the current bits against the wanted ones first and does nothing when
// they already match — the returned bool mirrors that, so a caller can skip the
// page write (and the pd_lsn stamp) on a no-op exactly as upstream skips
// MarkBufferDirty.
//
// bits must already be masked with VMValidBits: VISIBILITYMAP_XLOG_CATALOG_REL
// lives in the WAL flags byte only and must never reach the map
// (visibilitymapdefs.h:29 "NB: VISIBILITYMAP_XLOG_* may not be passed to
// visibilitymap_set()").
func VMPageSetBits(p Page, heapBlk BlockNumber, bits uint8) (bool, error) {
	if len(p) != BlockSize {
		return false, fmt.Errorf("vm page: bad length %d (want %d)", len(p), BlockSize)
	}
	if bits&^VMValidBits != 0 {
		return false, fmt.Errorf("vm page: bits 0x%02x carry non-map flags (valid mask 0x%02x)", bits, VMValidBits)
	}
	byteIdx, shift := vmBitPosition(heapBlk)
	status := (p[byteIdx] >> shift) & VMValidBits
	if status == bits {
		return false, nil
	}
	p[byteIdx] |= bits << shift
	return true, nil
}

// VMPageClearBits is the page mutation inside visibilitymap_clear: AND away the
// given bits for heapBlk. Returns whether anything changed, matching upstream's
// `cleared` return.
//
// Upstream asserts the caller never clears ALL_VISIBLE while leaving ALL_FROZEN
// set — an all-frozen page that is not all-visible is a corrupt map state — so
// the same rule is enforced here rather than trusted.
func VMPageClearBits(p Page, heapBlk BlockNumber, bits uint8) (bool, error) {
	if len(p) != BlockSize {
		return false, fmt.Errorf("vm page: bad length %d (want %d)", len(p), BlockSize)
	}
	if bits&VMValidBits == 0 || bits&^VMValidBits != 0 {
		return false, fmt.Errorf("vm page: clear bits 0x%02x invalid (valid mask 0x%02x)", bits, VMValidBits)
	}
	if bits == VMAllVisible {
		return false, fmt.Errorf("vm page: refusing to clear ALL_VISIBLE alone (would leave ALL_FROZEN set)")
	}
	byteIdx, shift := vmBitPosition(heapBlk)
	mask := bits << shift
	if p[byteIdx]&mask == 0 {
		return false, nil
	}
	p[byteIdx] &^= mask
	return true, nil
}
