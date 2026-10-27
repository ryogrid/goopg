package storage

import (
	"encoding/binary"
	"unsafe"
)

// Data-page checksums.
//
// This is a faithful Go port of upstream's page-checksum primitive
// (postgres/src/include/storage/checksum_impl.h). The algorithm is an
// FNV-1a variant computed over the 8 KiB page as a 64×32 grid of 32-bit
// little-endian words, with 32 partial sums accumulated in parallel and
// then xor-folded. The pd_checksum field itself (bytes 8..10) is excluded
// from the computation, and the block number is mixed in at the end so a
// page relocated to a different block fails verification.
//
// goopg targets little-endian on-disk format, matching the only platforms
// where PostgreSQL data-page checksums are defined (they are not portable
// across endianness). The 32-bit words are therefore read little-endian.
//
// See docs/design/0102-0019-initdb-data-checksums.md.

const (
	// nChecksumSums is the number of partial checksums accumulated in
	// parallel (N_SUMS in checksum_impl.h). It is a fixed part of the
	// algorithm: changing it changes the result.
	nChecksumSums = 32
	// checksumFNVPrime is the FNV-1a prime multiplier.
	checksumFNVPrime = 16777619
)

// checksumBaseOffsets seeds each of the 32 parallel FNV hashes into a
// distinct initial state. Copied verbatim from checksum_impl.h.
var checksumBaseOffsets = [nChecksumSums]uint32{
	0x5B1F36E9, 0xB8525960, 0x02AB50AA, 0x1DE66D2A,
	0x79FF467A, 0x9BB9F8A3, 0x217E7CD2, 0x83E13D2C,
	0xF8D4474F, 0xE39EB970, 0x42C6AE16, 0x993216FA,
	0x7B093B5D, 0x98DAFF3C, 0xF718902A, 0x0B1C9CDB,
	0xE58F764B, 0x187636BC, 0x5D7B3BB1, 0xE73DE7DE,
	0x92BEC979, 0xCCA6C0B2, 0x304A0979, 0x85AA43D4,
	0x783125BB, 0x6CA8EAA2, 0xE407EAC6, 0x4B5CFC3E,
	0x9FBF8C76, 0x15CA20BE, 0xF2CA9FD3, 0x959BD756,
}

// checksumComp applies one round of the checksum mixing formula
// (CHECKSUM_COMP in checksum_impl.h):
//
//	tmp = checksum ^ value; checksum = tmp*FNV_PRIME ^ (tmp >> 17)
func checksumComp(checksum, value uint32) uint32 {
	tmp := checksum ^ value
	return tmp*checksumFNVPrime ^ (tmp >> 17)
}

// pageChecksumBlock computes the raw 32-bit block checksum over page,
// mirroring pg_checksum_block. The pd_checksum word (low 16 bits of the
// uint32 at byte offset 8) is treated as zero so the stored checksum does
// not feed back into the computation. page must be exactly BlockSize bytes.
//
// On a little-endian host with a 4-byte-aligned whole page the words are
// read in place as 32-lane rows (pageChecksumBlockRows); pageChecksumBlock-
// Portable is the byte-assembling form, kept for every other case and as the
// oracle the rows form is tested against.
func pageChecksumBlock(page []byte) uint32 {
	if hostLittleEndian && len(page) == BlockSize && uintptr(unsafe.Pointer(unsafe.SliceData(page)))%4 == 0 {
		return pageChecksumBlockRows(page)
	}
	return pageChecksumBlockPortable(page)
}

// hostLittleEndian reports whether uint32 loads read the on-disk
// little-endian words directly.
var hostLittleEndian = binary.NativeEndian.Uint16([]byte{1, 0}) == 1

// pageChecksumBlockRows is pg_checksum_block over the page viewed as rows of
// nChecksumSums uint32 words — the layout checksum_impl.h's loop is written
// for so a C compiler vectorises it. Each row is an array pointer, so the
// inner loop carries no bounds checks, and pd_checksum is masked once in row
// 0 instead of being tested for at every word: the old loop's per-word work
// was 12 % of TPC-H Q17, which verifies every lineitem page it reads
// (M0122-0012 C19).
func pageChecksumBlockRows(page []byte) uint32 {
	sums := checksumBaseOffsets
	const rows = BlockSize / (4 * nChecksumSums)
	words := unsafe.Slice((*uint32)(unsafe.Pointer(unsafe.SliceData(page))), BlockSize/4)
	// pd_checksum is the low 16 bits of word 2 of row 0 (bytes 8..10).
	r := (*[nChecksumSums]uint32)(words[0:nChecksumSums])
	for j := range nChecksumSums {
		v := r[j]
		if j == 2 {
			v &= 0xFFFF0000
		}
		sums[j] = checksumComp(sums[j], v)
	}
	for i := 1; i < rows; i++ {
		r = (*[nChecksumSums]uint32)(words[i*nChecksumSums : (i+1)*nChecksumSums])
		for j := range nChecksumSums {
			tmp := sums[j] ^ r[j]
			sums[j] = tmp*checksumFNVPrime ^ (tmp >> 17)
		}
	}
	for j := range nChecksumSums {
		sums[j] = checksumComp(sums[j], 0)
	}
	for j := range nChecksumSums {
		sums[j] = checksumComp(sums[j], 0)
	}
	var result uint32
	for j := range nChecksumSums {
		result ^= sums[j]
	}
	return result
}

// pageChecksumBlockPortable is pg_checksum_block assembling each word from
// little-endian bytes.
func pageChecksumBlockPortable(page []byte) uint32 {
	var sums [nChecksumSums]uint32
	copy(sums[:], checksumBaseOffsets[:])

	// The page is a (BlockSize / (4*N_SUMS)) × N_SUMS grid of LE uint32s.
	const rows = BlockSize / (4 * nChecksumSums)
	for i := 0; i < rows; i++ {
		base := i * nChecksumSums * 4
		for j := 0; j < nChecksumSums; j++ {
			off := base + j*4
			value := binary.LittleEndian.Uint32(page[off:])
			// pd_checksum occupies bytes 8..10, i.e. the low 16 bits
			// of the uint32 at offset 8 (i==0, j==2). Mask it out so
			// the existing stored checksum is excluded, exactly as
			// upstream transiently zeroes pd_checksum.
			if off == 8 {
				value &= 0xFFFF0000
			}
			sums[j] = checksumComp(sums[j], value)
		}
	}

	// Two final rounds of zeroes mix the bits of the last words added.
	for i := 0; i < 2; i++ {
		for j := 0; j < nChecksumSums; j++ {
			sums[j] = checksumComp(sums[j], 0)
		}
	}

	var result uint32
	for j := 0; j < nChecksumSums; j++ {
		result ^= sums[j]
	}
	return result
}

// PageChecksum computes the PG-compatible data-page checksum for page at
// block number blkno, mirroring pg_checksum_page. The returned value is
// what belongs in pd_checksum (bytes 8..10); it is never zero (zero is
// reserved to mean "no checksum"). page must be exactly BlockSize bytes
// and must not be an all-new (uninitialised) page — new pages carry no
// checksum (see IsNew); callers skip them.
func PageChecksum(page []byte, blkno BlockNumber) uint16 {
	checksum := pageChecksumBlock(page)
	// Mix in the block number to detect transposed pages.
	checksum ^= uint32(blkno)
	// Reduce to uint16 with a +1 offset so the result is never zero.
	return uint16((checksum % 65535) + 1)
}

// PageSetChecksumCopy returns a fresh BlockSize copy of page with its
// pd_checksum field set for block number blkno, mirroring upstream's
// PageSetChecksumCopy (postgres/src/backend/storage/page/bufpage.c). The
// input page is never mutated, so a shared in-memory buffer can be flushed
// without disturbing concurrent readers. A new (all-zero / uninitialised)
// page is returned verbatim — new pages carry no checksum, exactly as
// upstream skips them (PageIsNew). page must be exactly BlockSize bytes.
func PageSetChecksumCopy(page []byte, blkno BlockNumber) []byte {
	out := make([]byte, BlockSize)
	copy(out, page)
	if IsNew(out) {
		return out
	}
	MustHeader(out).SetChecksum(PageChecksum(out, blkno))
	return out
}

// VerifyPage reports whether page carries a valid data-page checksum for
// block number blkno, mirroring the checksum half of upstream's
// PageIsVerifiedExtended (postgres/src/backend/storage/page/bufpage.c). A
// new (all-zero / uninitialised) page is always considered valid — it
// carries no checksum. page must be exactly BlockSize bytes.
func VerifyPage(page []byte, blkno BlockNumber) bool {
	if len(page) != BlockSize {
		return false
	}
	if IsNew(page) {
		return true
	}
	return MustHeader(page).Checksum() == PageChecksum(page, blkno)
}
