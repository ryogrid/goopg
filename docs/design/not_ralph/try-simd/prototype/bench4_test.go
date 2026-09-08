//go:build goexperiment.simd

package main

import (
	"testing"
	"unsafe"
)

// scalarFair: identical to `scalar` but reads the page through the SAME
// unsafe word view the SIMD path uses, so the comparison isolates
// vectorisation rather than byte-decode + bounds-check overhead.
func scalarFair(page []byte) uint32 {
	words := unsafe.Slice((*uint32)(unsafe.Pointer(&page[0])), blockSize/4)
	var sums [nSums]uint32
	copy(sums[:], baseOffsets[:])
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		base := i * nSums
		for j := 0; j < nSums; j++ {
			v := words[base+j]
			if base+j == 2 {
				v &= 0xFFFF0000
			}
			sums[j] = comp(sums[j], v)
		}
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < nSums; j++ {
			sums[j] = comp(sums[j], 0)
		}
	}
	var r uint32
	for j := 0; j < nSums; j++ {
		r ^= sums[j]
	}
	return r
}

func TestAgreeFair(t *testing.T) {
	if s, f := scalar(page), scalarFair(page); s != f {
		t.Fatalf("scalar=%08x scalarFair=%08x", s, f)
	}
}

func BenchmarkScalarFair(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = scalarFair(page)
	}
	b.SetBytes(blockSize)
}
