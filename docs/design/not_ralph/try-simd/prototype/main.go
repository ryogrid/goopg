//go:build goexperiment.simd

package main

import (
	"encoding/binary"
	"fmt"
	"simd/archsimd"
)

const (
	nSums     = 32
	fnvPrime  = 16777619
	blockSize = 8192
)

var baseOffsets = [nSums]uint32{
	0x5B1F36E9, 0xB8525960, 0x02AB50AA, 0x1DE66D2A,
	0x79FF467A, 0x9BB9F8A3, 0x217E7CD2, 0x83E13D2C,
	0xF8D4474F, 0xE39EB970, 0x42C6AE16, 0x993216FA,
	0x7B093B5D, 0x98DAFF3C, 0xF718902A, 0x0B1C9CDB,
	0xE58F764B, 0x187636BC, 0x5D7B3BB1, 0xE73DE7DE,
	0x92BEC979, 0xCCA6C0B2, 0x304A0979, 0x85AA43D4,
	0x783125BB, 0x6CA8EAA2, 0xE407EAC6, 0x4B5CFC3E,
	0x9FBF8C76, 0x15CA20BE, 0xF2CA9FD3, 0x959BD756,
}

func comp(c, v uint32) uint32 { t := c ^ v; return t*fnvPrime ^ (t >> 17) }

func scalar(page []byte) uint32 {
	var sums [nSums]uint32
	copy(sums[:], baseOffsets[:])
	const rows = blockSize / (4 * nSums)
	for i := 0; i < rows; i++ {
		base := i * nSums * 4
		for j := 0; j < nSums; j++ {
			off := base + j*4
			v := binary.LittleEndian.Uint32(page[off:])
			if off == 8 {
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

func simd(page []byte) uint32 {
	// 4 accumulators of 8 lanes = the 32 lanes.
	var acc [4]archsimd.Uint32x8
	for k := 0; k < 4; k++ {
		acc[k] = archsimd.LoadUint32x8Slice(baseOffsets[k*8 : k*8+8])
	}
	prime := archsimd.BroadcastUint32x8(fnvPrime)
	const rows = blockSize / (4 * nSums)
	var w [8]uint32
	for i := 0; i < rows; i++ {
		base := i * nSums * 4
		for k := 0; k < 4; k++ {
			for l := 0; l < 8; l++ {
				off := base + (k*8+l)*4
				v := binary.LittleEndian.Uint32(page[off:])
				if off == 8 {
					v &= 0xFFFF0000
				}
				w[l] = v
			}
			vv := archsimd.LoadUint32x8(&w)
			t := acc[k].Xor(vv)
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
	res := acc[0].Xor(acc[1]).Xor(acc[2]).Xor(acc[3])
	res.Store(&out)
	var r uint32
	for _, v := range out {
		r ^= v
	}
	return r
}

func main() {
	fmt.Println("AVX2:", archsimd.X86.AVX2())
	page := make([]byte, blockSize)
	for i := range page {
		page[i] = byte(i*7 + 3)
	}
	s, v := scalar(page), simd(page)
	fmt.Printf("scalar=%08x simd=%08x match=%v\n", s, v, s == v)
}
