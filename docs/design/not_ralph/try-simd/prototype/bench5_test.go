//go:build goexperiment.simd

package main

import "testing"

// scalarSafe: no unsafe at all. Hoist the row reslice, use a three-index
// slice so the compiler can prove the 4-byte window, and hoist the
// pd_checksum branch out of the inner loop (off==8 <=> i==0 && j==2).
func scalarSafe(page []byte) uint32 {
	var sums [nSums]uint32
	copy(sums[:], baseOffsets[:])
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		row := page[i*nSums*4 : (i+1)*nSums*4 : (i+1)*nSums*4]
		for j := 0; j < nSums; j++ {
			w := row[j*4 : j*4+4 : j*4+4]
			v := uint32(w[0]) | uint32(w[1])<<8 | uint32(w[2])<<16 | uint32(w[3])<<24
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

func TestAgreeSafe(t *testing.T) {
	if s, f := scalar(page), scalarSafe(page); s != f {
		t.Fatalf("scalar=%08x scalarSafe=%08x", s, f)
	}
}

func BenchmarkScalarSafe(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = scalarSafe(page)
	}
	b.SetBytes(blockSize)
}
