//go:build goexperiment.simd

package main

import (
	"testing"
	"unsafe"

	"simd/archsimd"
)

// simdFast: on little-endian, the page bytes ARE the LE uint32s, so the
// vector can be loaded straight off the page with no per-word decode.
func simdFast(page []byte) uint32 {
	words := unsafe.Slice((*uint32)(unsafe.Pointer(&page[0])), blockSize/4)
	var acc [4]archsimd.Uint32x8
	for k := 0; k < 4; k++ {
		acc[k] = archsimd.LoadUint32x8Slice(baseOffsets[k*8 : k*8+8])
	}
	prime := archsimd.BroadcastUint32x8(fnvPrime)
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		base := i * nSums
		for k := 0; k < 4; k++ {
			v := archsimd.LoadUint32x8Slice(words[base+k*8 : base+k*8+8])
			if i == 0 && k == 0 {
				// pd_checksum lives at word index 2: mask its low 16 bits.
				var tmp [8]uint32
				v.Store(&tmp)
				tmp[2] &= 0xFFFF0000
				v = archsimd.LoadUint32x8(&tmp)
			}
			t := acc[k].Xor(v)
			acc[k] = t.Mul(prime).Xor(t.ShiftAllRight(17))
		}
	}
	zero := archsimd.BroadcastUint32x8(0)
	for i := 0; i < 2; i++ {
		for k := 0; k < 4; k++ {
			t := acc[k].Xor(zero)
			acc[k] = t.Mul(prime).Xor(t.ShiftAllRight(17))
		}
	}
	var out [8]uint32
	acc[0].Xor(acc[1]).Xor(acc[2]).Xor(acc[3]).Store(&out)
	var r uint32
	for _, v := range out {
		r ^= v
	}
	return r
}

var page = func() []byte {
	p := make([]byte, blockSize)
	for i := range p {
		p[i] = byte(i*7 + 3)
	}
	return p
}()

func TestAgree(t *testing.T) {
	if s, f := scalar(page), simdFast(page); s != f {
		t.Fatalf("scalar=%08x simdFast=%08x", s, f)
	}
}

var sink uint32

func BenchmarkScalar(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = scalar(page)
	}
	b.SetBytes(blockSize)
}
func BenchmarkSIMDFast(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = simdFast(page)
	}
	b.SetBytes(blockSize)
}
