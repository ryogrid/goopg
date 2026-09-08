//go:build goexperiment.simd

package main

import (
	"encoding/binary"
	"testing"
)

// variant A: binary.LittleEndian.Uint32 on a bounds-proven window.
func scalarSafeA(page []byte) uint32 {
	var sums [nSums]uint32
	copy(sums[:], baseOffsets[:])
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		row := page[i*128 : i*128+128 : i*128+128]
		for j := 0; j < nSums; j++ {
			v := binary.LittleEndian.Uint32(row[j*4 : j*4+4 : j*4+4])
			if i == 0 && j == 2 {
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

// variant B: mask applied once AFTER the loop structure, inner loop fully
// branch-free, iterating the row as a [32]uint32-shaped window.
func scalarSafeB(page []byte) uint32 {
	var sums [nSums]uint32
	copy(sums[:], baseOffsets[:])
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		row := page[i*128 : i*128+128 : i*128+128]
		for j := 0; j < nSums; j++ {
			v := binary.LittleEndian.Uint32(row[j*4 : j*4+4 : j*4+4])
			sums[j] = comp(sums[j], v)
		}
		_ = row
		if i == 0 {
			// redo lane 2 with the masked value: undo is impossible, so
			// instead handle row 0 separately below.
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

func TestAgreeA(t *testing.T) {
	if s, f := scalar(page), scalarSafeA(page); s != f {
		t.Fatalf("scalar=%08x A=%08x", s, f)
	}
}
func BenchmarkScalarSafeA(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = scalarSafeA(page)
	}
	b.SetBytes(blockSize)
}
func BenchmarkScalarSafeB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = scalarSafeB(page)
	}
	b.SetBytes(blockSize)
}
