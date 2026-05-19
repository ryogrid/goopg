// pglz.go — minimal PGLZ compressor for bootstrap pg_node_tree fields.
//
// PG stores large varlena values (ev_action, ev_qual) as inline-compressed
// data using its own PGLZ algorithm (src/common/pg_lzcompress.c). The
// compressed wire format is:
//
//	[4-byte varlena header] [4-byte tcinfo] [compressed payload]
//
// Header: (totalSize << 2) | 0x02  (VARATT_IS_4B_C, bits 1-0 = 0b10)
// tcinfo: (rawSize << 2) | 0x00    (ToastCompressionId_PGLZ = 0)
//
// This file only implements compression. Decompression of PGLZ blobs read
// from the wire is not yet implemented in codec.go (it reports
// "compressed varlena not supported") — but goopg does not need to decode
// its own bootstrap ev_action blobs at runtime.

package initdb

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/executor"
)

// pglzToastThreshold is the minimum size at which PG applies inline TOAST
// compression. Matches TOAST_TUPLE_THRESHOLD from PG18 heaptoast.h (2KB).
const pglzToastThreshold = 2048

// pglzVarlenaDatum returns a Datum for a pg_node_tree column. If the text
// payload is large enough to benefit from compression, it returns a
// KindBytes datum whose BytesValue() is the complete compressed varlena
// (header + tcinfo + compressed payload). The codec's pg_node_tree KindBytes
// passthrough emits those bytes verbatim.
//
// For small payloads the function falls back to a plain string datum so the
// codec encodes it as an uncompressed 1-byte or 4-byte varlena.
func pglzVarlenaDatum(s string) executor.Datum {
	data := []byte(s)
	if len(data) < pglzToastThreshold {
		return executor.NewStringDatum(s)
	}
	compressed := pglzCompressRaw(data)
	totalSize := 8 + len(compressed)
	if totalSize >= len(data)+4 {
		// Compression didn't help; store uncompressed 4-byte varlena.
		return executor.NewStringDatum(s)
	}
	buf := make([]byte, totalSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(totalSize)<<2|0x02) // VARATT_IS_4B_C
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(data))<<2|0x00) // PGLZ method
	copy(buf[8:], compressed)
	return executor.NewBytesDatum(buf)
}

// pglzCompressRaw compresses data with the PGLZ algorithm and returns the
// compressed payload bytes (WITHOUT the varlena header / tcinfo prefix).
//
// PGLZ output format (matches src/common/pg_lzcompress.c):
//   - Control byte (1 byte): bits 0..7, each indicating the following token.
//     Bit = 1 → literal byte. Bit = 0 → 2-byte copy sequence.
//   - Literal: 1 byte copied verbatim to output.
//   - Copy: 2 bytes: upper = (len-3)<<4 | (off>>8), lower = off&0xFF.
//     Decodes as: copy 'len' bytes from dp-off; len ∈ [3,18], off ∈ [1,4095].
//
// This is a greedy O(n·window) compressor. The window is at most 4095 bytes.
// It runs only at initdb time (once per bootstrap), so throughput is not
// critical.
func pglzCompressRaw(data []byte) []byte {
	n := len(data)
	if n == 0 {
		return nil
	}

	out := make([]byte, 0, n/2+64)
	i := 0
	for i < n {
		ctrlIdx := len(out)
		out = append(out, 0) // control byte placeholder; filled in below
		var ctrl byte

		for bit := uint(0); bit < 8 && i < n; bit++ {
			bestLen := 0
			bestOff := 0

			// Search history window [max(0,i-4095)..i-1] for the longest match.
			maxOff := i
			if maxOff > 4095 {
				maxOff = 4095
			}
			maxMatchLen := n - i
			if maxMatchLen > 18 {
				maxMatchLen = 18
			}
			for off := 1; off <= maxOff; off++ {
				src := i - off
				l := 0
				for l < maxMatchLen && data[src+l] == data[i+l] {
					l++
				}
				if l >= 3 && l > bestLen {
					bestLen = l
					bestOff = off
					if bestLen == 18 {
						break // maximum match length reached
					}
				}
			}

			if bestLen >= 3 {
				// Emit copy sequence (ctrl bit stays 0).
				b0 := byte((bestLen-3)<<4) | byte(bestOff>>8)
				b1 := byte(bestOff & 0xFF)
				out = append(out, b0, b1)
				i += bestLen
			} else {
				// Emit literal byte.
				ctrl |= 1 << bit
				out = append(out, data[i])
				i++
			}
		}
		out[ctrlIdx] = ctrl
	}
	return out
}
