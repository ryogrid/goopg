//go:build goexperiment.simd

package main

import (
	"testing"
	"unsafe"

	"simd/archsimd"
)

// simdUnrolled keeps the four accumulators as SEPARATE VALUES, never in an
// array. archsimd's doc is explicit: do not put a vector type in an aggregate.
// An array indexed by a loop variable forces a spill/reload every iteration.
func simdUnrolled(page []byte) uint32 {
	words := unsafe.Slice((*uint32)(unsafe.Pointer(&page[0])), blockSize/4)
	a0 := archsimd.LoadUint32x8Slice(baseOffsets[0:8])
	a1 := archsimd.LoadUint32x8Slice(baseOffsets[8:16])
	a2 := archsimd.LoadUint32x8Slice(baseOffsets[16:24])
	a3 := archsimd.LoadUint32x8Slice(baseOffsets[24:32])
	prime := archsimd.BroadcastUint32x8(fnvPrime)

	step := func(acc, v archsimd.Uint32x8) archsimd.Uint32x8 {
		t := acc.Xor(v)
		return t.Mul(prime).Xor(t.ShiftAllRight(17))
	}

	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		b := i * nSums
		v0 := archsimd.LoadUint32x8Slice(words[b : b+8])
		v1 := archsimd.LoadUint32x8Slice(words[b+8 : b+16])
		v2 := archsimd.LoadUint32x8Slice(words[b+16 : b+24])
		v3 := archsimd.LoadUint32x8Slice(words[b+24 : b+32])
		if i == 0 {
			var tmp [8]uint32
			v0.Store(&tmp)
			tmp[2] &= 0xFFFF0000
			v0 = archsimd.LoadUint32x8(&tmp)
		}
		a0 = step(a0, v0)
		a1 = step(a1, v1)
		a2 = step(a2, v2)
		a3 = step(a3, v3)
	}
	zero := archsimd.BroadcastUint32x8(0)
	for i := 0; i < 2; i++ {
		a0, a1, a2, a3 = step(a0, zero), step(a1, zero), step(a2, zero), step(a3, zero)
	}
	var out [8]uint32
	a0.Xor(a1).Xor(a2).Xor(a3).Store(&out)
	var r uint32
	for _, v := range out {
		r ^= v
	}
	return r
}

func TestAgreeUnrolled(t *testing.T) {
	if s, f := scalar(page), simdUnrolled(page); s != f {
		t.Fatalf("scalar=%08x simdUnrolled=%08x", s, f)
	}
}

func BenchmarkSIMDUnrolled(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = simdUnrolled(page)
	}
	b.SetBytes(blockSize)
}
