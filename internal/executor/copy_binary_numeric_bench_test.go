package executor

import (
	"encoding/binary"
	"testing"
)

// benchNumericPayload builds a PG binary-format NUMERIC with ndigits base-10000
// digits (the shape COPY BINARY carries).
func benchNumericPayload(ndigits int) []byte {
	p := make([]byte, 8+ndigits*2)
	binary.BigEndian.PutUint16(p[0:2], uint16(ndigits))
	binary.BigEndian.PutUint16(p[2:4], uint16(ndigits-1)) // weight
	binary.BigEndian.PutUint16(p[4:6], 0)                 // sign: positive
	binary.BigEndian.PutUint16(p[6:8], 0)                 // dscale
	for i := 0; i < ndigits; i++ {
		binary.BigEndian.PutUint16(p[8+i*2:], uint16(1000+i))
	}
	return p
}

// BenchmarkDecodeNumericBinary measures binary COPY's NUMERIC decode
// (review/260831 EC-16): a mantissa accumulation loop ran before every decode
// and its result was discarded.
func BenchmarkDecodeNumericBinary(b *testing.B) {
	for _, ndigits := range []int{2, 8} {
		payload := benchNumericPayload(ndigits)
		name := "digits=2"
		if ndigits != 2 {
			name = "digits=8"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decodeNumericBinary(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
