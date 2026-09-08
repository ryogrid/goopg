//go:build goexperiment.simd

package main

import (
	"testing"
	"unsafe"

	"simd/archsimd"
)

// simdHoisted: ShiftAllRight(17) lowers to MOVL/MOVQ/VPSRLD every call (the
// shift count is materialised into an XMM each time). Hoisting a broadcast
// count and using the per-lane variable shift VPSRLVD avoids that.
func simdHoisted(page []byte) uint32 {
	words := unsafe.Slice((*uint32)(unsafe.Pointer(&page[0])), blockSize/4)
	a0 := archsimd.LoadUint32x8Slice(baseOffsets[0:8])
	a1 := archsimd.LoadUint32x8Slice(baseOffsets[8:16])
	a2 := archsimd.LoadUint32x8Slice(baseOffsets[16:24])
	a3 := archsimd.LoadUint32x8Slice(baseOffsets[24:32])
	prime := archsimd.BroadcastUint32x8(fnvPrime)
	sh := archsimd.BroadcastUint32x8(17)

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
		t0 := a0.Xor(v0)
		t1 := a1.Xor(v1)
		t2 := a2.Xor(v2)
		t3 := a3.Xor(v3)
		a0 = t0.Mul(prime).Xor(t0.ShiftRight(sh))
		a1 = t1.Mul(prime).Xor(t1.ShiftRight(sh))
		a2 = t2.Mul(prime).Xor(t2.ShiftRight(sh))
		a3 = t3.Mul(prime).Xor(t3.ShiftRight(sh))
	}
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
	var r uint32
	for _, v := range out {
		r ^= v
	}
	return r
}

func TestAgreeHoisted(t *testing.T) {
	if s, f := scalar(page), simdHoisted(page); s != f {
		t.Fatalf("scalar=%08x simdHoisted=%08x", s, f)
	}
}

func BenchmarkSIMDHoisted(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = simdHoisted(page)
	}
	b.SetBytes(blockSize)
}
