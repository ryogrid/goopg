//go:build goexperiment.simd && amd64

package storage

import (
	"unsafe"

	"simd/archsimd"
)

// AVX2 kernel for the PG data-page checksum. Compiled ONLY when the module is
// built with GOEXPERIMENT=simd on amd64; every other build takes
// checksum_nosimd.go. See docs/design/not_ralph/try-simd/DESIGN.md.
//
// WHY THIS EXISTS AND WHAT IT IS NOT. The measured share of CPU spent in
// pageChecksumBlock is 0.043% (TPC-H SF=1 cold), 0.084% (TPC-DS SF0.5), 0.125%
// (pgbench OLTP) and 0.4% (COPY load). This kernel is ~6x faster than the
// tidied scalar routine and ~13x faster than the routine as it stood before,
// which buys at most ~0.1% end to end. It is an experiment with a verified
// result, deliberately NOT the default build, and no end-to-end win is claimed
// for it.
//
// PG's own algorithm is shaped for this: N_SUMS = 32 independent lanes exist
// precisely so the mixing chain can be done in parallel
// (postgres/src/include/storage/checksum_impl.h:44-48). Note upstream sized 32
// lanes for 128-bit SSE registers and says explicitly that "for future
// processors with 256bit vector registers this will leave some performance on
// the table" (checksum_impl.h:90-93) — so 4 x Uint32x8 uses only 4 of 16 YMM
// registers, and the ceiling here is set by the algorithm's SSE-era shape.
//
// THREE ARCHSIMD PERFORMANCE TRAPS, each worth more than an order of
// magnitude, all three hit while developing this file:
//
//  1. Never hold the accumulators in an aggregate. `var acc [4]Uint32x8`
//     indexed by a loop variable spills and reloads through the stack every
//     iteration and made the kernel 6.4x SLOWER than scalar. archsimd's own
//     doc warns of it ("not recommended to ... put it in an aggregate type");
//     the warning is worth 6x, not style. Hence a0..a3 as separate variables.
//  2. ShiftAllRight(constant) does not lower to an immediate shift. It emits
//     MOVL $17, R9; MOVQ R9, X3; VPSRLD X3, ... rematerialising the count on
//     every call. Hoisting a broadcast count and using the per-lane
//     ShiftRight took the kernel from 3920 ns to 171 ns -- 23x from that one
//     change. Hence `sh` hoisted out of the loop.
//  3. LoadUint32x8Slice(s[i:i+8]) emits a bounds check per load and the
//     compiler hoists nothing; that alone left an otherwise-correct kernel
//     1.8x slower than scalar. Hence raw-pointer loads below.
//
// The unsafe pointer arithmetic is confined to this file and is safe on two
// grounds: the caller guarantees len(page) == BlockSize (pageChecksumBlock's
// contract), and the loads are unaligned (VMOVDQU), so no alignment
// precondition is imposed on the page. Buffer-pool pages are in fact
// 4 KiB-aligned (internal/storage/arena.go), but PageSetChecksumCopy and
// extendBatch pass plain make([]byte, ...) buffers and must also be correct.

// simdChecksumAvailable reports whether the vector kernel may run. The build
// tag is not sufficient on its own: GOEXPERIMENT=simd compiles happily for a
// pre-AVX2 amd64 CPU, where these instructions would fault. Both gates are
// required.
var simdChecksumOK = archsimd.X86.AVX2()

func simdChecksumAvailable() bool { return simdChecksumOK }

// pageChecksumBlockSIMD computes the same value as pageChecksumBlockScalar.
// The equivalence is asserted over a random corpus by
// TestChecksumSIMDMatchesScalar, which is mutation-checked.
func pageChecksumBlockSIMD(page []byte) uint32 {
	base := unsafe.Pointer(unsafe.SliceData(page))

	// The 32 lanes are four 8-wide vectors, held as SEPARATE values (trap 1),
	// seeded from the base offsets exactly as the scalar routine seeds sums[].
	a0 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Pointer(&checksumBaseOffsets[0])))
	a1 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Pointer(&checksumBaseOffsets[8])))
	a2 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Pointer(&checksumBaseOffsets[16])))
	a3 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Pointer(&checksumBaseOffsets[24])))

	prime := archsimd.BroadcastUint32x8(checksumFNVPrime)
	sh := archsimd.BroadcastUint32x8(17) // hoisted; see trap 2

	const rows = BlockSize / (4 * nChecksumSums)
	const rowBytes = nChecksumSums * 4
	for i := 0; i < rows; i++ {
		off := i * rowBytes
		v0 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Add(base, off)))
		v1 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Add(base, off+32)))
		v2 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Add(base, off+64)))
		v3 := archsimd.LoadUint32x8((*[8]uint32)(unsafe.Add(base, off+96)))
		if i == 0 {
			// pd_checksum is the low 16 bits of word 2, which lives in v0.
			// Masking it matches upstream's transient zeroing exactly; the
			// oracle review verified bit-equivalence over 200 vectors.
			var tmp [8]uint32
			v0.Store(&tmp)
			tmp[2] &= 0xFFFF0000
			v0 = archsimd.LoadUint32x8(&tmp)
		}
		t0, t1, t2, t3 := a0.Xor(v0), a1.Xor(v1), a2.Xor(v2), a3.Xor(v3)
		a0 = t0.Mul(prime).Xor(t0.ShiftRight(sh))
		a1 = t1.Mul(prime).Xor(t1.ShiftRight(sh))
		a2 = t2.Mul(prime).Xor(t2.ShiftRight(sh))
		a3 = t3.Mul(prime).Xor(t3.ShiftRight(sh))
	}

	// Two final rounds of zeroes, matching checksum_impl.h:164-167.
	zero := archsimd.BroadcastUint32x8(0)
	for i := 0; i < 2; i++ {
		t0, t1, t2, t3 := a0.Xor(zero), a1.Xor(zero), a2.Xor(zero), a3.Xor(zero)
		a0 = t0.Mul(prime).Xor(t0.ShiftRight(sh))
		a1 = t1.Mul(prime).Xor(t1.ShiftRight(sh))
		a2 = t2.Mul(prime).Xor(t2.ShiftRight(sh))
		a3 = t3.Mul(prime).Xor(t3.ShiftRight(sh))
	}

	var out [8]uint32
	a0.Xor(a1).Xor(a2).Xor(a3).Store(&out)
	var result uint32
	for _, v := range out {
		result ^= v
	}
	return result
}
