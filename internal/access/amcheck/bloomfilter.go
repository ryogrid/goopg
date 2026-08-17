// bloomfilter.go ports upstream PostgreSQL's space-efficient Bloom filter
// (postgres/src/backend/lib/bloomfilter.c, src/include/lib/bloomfilter.h) into
// the amcheck package. It is the foundational primitive for amcheck's B-tree
// "heapallindexed" verification tier: bt_check_every_level() fingerprints every
// index tuple into a Bloom filter, then scans the heap and asserts that the
// index tuple it forms for each visible heap tuple is present in that filter
// (verify_nbtree.c:bt_tuple_present_callback). A Bloom filter is ideal there
// because the contract it needs is exactly the one a Bloom filter gives:
//
//   - NO false negatives. If bt_index_check reports a heap tuple "missing" from
//     the index, it is genuinely missing — never a hashing artifact. This is
//     load-bearing: a false negative would be a spurious corruption report.
//   - Bounded (~1-2%) false positives. A heap tuple that is in fact absent from
//     the index may occasionally be reported present; this only weakens, never
//     falsifies, the check. Upstream accepts the same trade-off.
//
// This is a standalone data structure with no clog / TupleDesc / SQL coupling,
// so it is ported here engine-first/wire-later alongside verify_nbtree.go and
// verify_heapam.go; the heapallindexed scan that consumes it, and the
// bt_index_check SQL surface, are wired in a later loop once the tree is clean.
// See docs/design/0110-0006-amcheck-bloom-filter.md.
//
// goopg / upstream PG divergences handled here:
//
//   - Hash function. Upstream seeds enhanced double hashing from
//     hash_any_extended() (Jenkins lookup3, src/common/hashfn.c). goopg already
//     mirrors that as the unexported pgHashBytesExtended in internal/executor,
//     but importing it here would entangle the amcheck engine with the executor
//     package. Since the Bloom filter's correctness (no false negatives) holds
//     for ANY hash that bloomAddElement and bloomLacksElement share, and its
//     observable contract is purely "absent ⇒ definitely absent, present ⇒
//     probably present" rather than byte-for-byte parity with a PG bitset, this
//     port uses a self-contained seeded 64-bit hash (FNV-1a followed by the
//     MurmurHash3 fmix64 finalizer, so both 32-bit halves are well avalanched
//     for the double-hashing split below). Every other part of the algorithm —
//     bitset sizing, optimal_k, my_bloom_power, mod_m, and the enhanced double
//     hashing recurrence itself — is ported verbatim. When the tree is clean,
//     the Jenkins hash can be promoted to a shared package and substituted here
//     without changing the filter's contract.
package amcheck

import (
	"math"
	"math/bits"
)

// maxHashFuncs caps the number of hash functions the filter will use. Upstream
// MAX_HASH_FUNCS (bloomfilter.c).
const maxHashFuncs = 10

// bitsPerByte mirrors PG's BITS_PER_BYTE.
const bitsPerByte = 8

// bloomFilter is upstream's struct bloom_filter. The bitset is sized as a power
// of two number of bits (m), allowing mod_m to use a bitwise AND.
type bloomFilter struct {
	// kHashFuncs is the number of hash functions used, seeded by the caller's
	// seed (upstream k_hash_funcs).
	kHashFuncs int
	// seed is the caller-provided hash seed. Distinct seeds across runs make it
	// unlikely the same false positives recur when a set is fingerprinted twice.
	seed uint64
	// m is the bitset size in bits; always a power of two, at most 2^32.
	m uint64
	// bitset is the underlying bit array (m/8 bytes).
	bitset []byte
}

// bloomCreate creates a Bloom filter targeting a 1%-2% false positive rate when
// not constrained by memory. Ports bloom_create (bloomfilter.c).
//
// totalElems is an estimate of the final set size; the implementation copes
// well with it being off by a factor of five or more (Dillinger & Manolios,
// 2004). bloomWorkMem is sized in KB, like work_mem; it bounds the bitset,
// which is always a power-of-two number of bits and at most 512MB (2^32 bits).
func bloomCreate(totalElems int64, bloomWorkMem int, seed uint64) *bloomFilter {
	// Upstream callers always pass a positive estimate; guard against a
	// degenerate 0/negative estimate that would divide by zero in optimalK
	// (upstream relies on the caller for this; we make it explicit).
	if totalElems < 1 {
		totalElems = 1
	}

	// Aim for two bytes per element: enough for a sub-1% false positive rate
	// independent of bitset size or element count, and even after rounding the
	// bitset down to the next-lowest power of two the rate stays under 2% in
	// almost all cases.
	bitsetBytes := minU64(uint64(bloomWorkMem)*1024, uint64(totalElems)*2)
	bitsetBytes = maxU64(1024*1024, bitsetBytes)

	// Size in bits is the highest power of two <= target.
	bloomPower := myBloomPower(bitsetBytes * bitsPerByte)
	bitsetBits := uint64(1) << bloomPower
	bitsetBytes = bitsetBits / bitsPerByte

	return &bloomFilter{
		kHashFuncs: optimalK(bitsetBits, totalElems),
		seed:       seed,
		m:          bitsetBits,
		bitset:     make([]byte, bitsetBytes), // palloc0: all bits unset
	}
}

// bloomAddElement adds an element to the filter. Ports bloom_add_element.
func (f *bloomFilter) bloomAddElement(elem []byte) {
	hashes := f.kHashesValues(elem)
	// Map each bit-wise address to a byte-wise address + bit offset.
	for i := 0; i < f.kHashFuncs; i++ {
		f.bitset[hashes[i]>>3] |= 1 << (hashes[i] & 7)
	}
}

// bloomLacksElement reports whether the filter definitely lacks the element:
// true means "definitely not in the set", false means "probably present".
// Ports bloom_lacks_element.
func (f *bloomFilter) bloomLacksElement(elem []byte) bool {
	hashes := f.kHashesValues(elem)
	for i := 0; i < f.kHashFuncs; i++ {
		if f.bitset[hashes[i]>>3]&(1<<(hashes[i]&7)) == 0 {
			return true
		}
	}
	return false
}

// bloomPropBitsSet returns the proportion of bits currently set, expressed as a
// multiplier of filter size. With enough memory it stays close to 0.5 (more
// hash functions are used as more memory is available per element). This is the
// cheap instrumentation used to sanity-check filter saturation. Ports
// bloom_prop_bits_set.
func (f *bloomFilter) bloomPropBitsSet() float64 {
	var bitsSet uint64
	for _, b := range f.bitset {
		bitsSet += uint64(bits.OnesCount8(b))
	}
	return float64(bitsSet) / float64(f.m)
}

// myBloomPower returns the largest power i with 2^i <= targetBitsetBits, capped
// so the bitset never exceeds 2^32 bits (512MB), which lets us use 32-bit hash
// functions and stay under MaxAllocSize. Ports my_bloom_power.
func myBloomPower(targetBitsetBits uint64) int {
	bloomPower := -1
	for targetBitsetBits > 0 && bloomPower < 32 {
		bloomPower++
		targetBitsetBits >>= 1
	}
	return bloomPower
}

// optimalK returns the number of hash functions that minimizes the false
// positive rate for the given bitset size and projected element count, clamped
// to [1, maxHashFuncs]. Ports optimal_k.
func optimalK(bitsetBits uint64, totalElems int64) int {
	k := int(math.Round(math.Ln2 * float64(bitsetBits) / float64(totalElems)))
	if k < 1 {
		return 1
	}
	if k > maxHashFuncs {
		return maxHashFuncs
	}
	return k
}

// kHashesValues fills and returns the k bit addresses for an element. Ports
// k_hashes.
//
// Only two real independent hashes are computed (the two 32-bit halves of one
// 64-bit hash); the remaining addresses come from enhanced double hashing,
// which — unlike classic double hashing — avoids the collision issue classic
// double hashing has with power-of-two bitsets (Dillinger & Manolios).
func (f *bloomFilter) kHashesValues(elem []byte) [maxHashFuncs]uint32 {
	var hashes [maxHashFuncs]uint32

	// Use 64-bit hashing to get two independent 32-bit hashes.
	hash := bloomHash64(elem, f.seed)
	x := uint32(hash)
	y := uint32(hash >> 32)
	m := f.m

	x = modM(x, m)
	y = modM(y, m)

	hashes[0] = x
	for i := 1; i < f.kHashFuncs; i++ {
		x = modM(x+y, m)
		y = modM(y+uint32(i), m)
		hashes[i] = x
	}
	return hashes
}

// modM computes val MOD m cheaply, assuming m is a power of two (so a bitwise
// AND suffices and there is no modulo bias). Ports mod_m.
func modM(val uint32, m uint64) uint32 {
	// m is a power of two <= 2^32, so m-1 is a 32-bit mask.
	return uint32(uint64(val) & (m - 1))
}

// bloomHash64 is the seeded 64-bit hash backing enhanced double hashing. See
// the "Hash function" divergence note in the file header: FNV-1a folds in the
// seed, then the MurmurHash3 fmix64 finalizer avalanches every bit so both
// 32-bit halves are independently well-distributed.
func bloomHash64(elem []byte, seed uint64) uint64 {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := fnvOffset64 ^ seed
	for _, b := range elem {
		h ^= uint64(b)
		h *= fnvPrime64
	}
	// MurmurHash3 64-bit finalizer (fmix64).
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
