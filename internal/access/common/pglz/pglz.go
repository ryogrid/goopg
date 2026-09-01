// Package pglz implements PostgreSQL's PGLZ (Lempel-Ziv) compression used for
// inline-compressed varlena values (VARATT_IS_4B_C). It is a faithful port of
// the wire format defined by src/common/pg_lzcompress.c and src/include/
// varatt.h, so that:
//
//   - varlena values goopg compresses (e.g. bootstrap pg_rewrite.ev_action
//     pg_node_tree blobs) can be decompressed by a real PostgreSQL reading the
//     catalog — for instance an attached PG standby, and
//   - inline-compressed varlena values produced by real PostgreSQL can be
//     decompressed by goopg (logical-replication decode, heap/catalog reads).
//
// The compressed varlena layout (little-endian, per varatt.h) is:
//
//	[4B va_header = (totalSize<<2)|0x02]  VARATT_IS_4B_C
//	[4B va_tcinfo = rawSize | (method<<30)]
//	[compressed PGLZ token stream]
//
// va_tcinfo stores the original (decompressed) size in the low 30 bits and the
// ToastCompressionId in the top 2 bits (PGLZ = 0, LZ4 = 1). Only PGLZ is
// implemented here.
package pglz

import (
	"encoding/binary"
	"fmt"
)

// CompressionMethodPGLZ is ToastCompressionId_PGLZ, the value stored in the top
// two bits of a compressed varlena's va_tcinfo. LZ4 (1) is not implemented.
const CompressionMethodPGLZ = 0

const (
	// minMatchLen is the shortest back-reference PGLZ encodes; shorter runs
	// are emitted as literals.
	minMatchLen = 3
	// maxMatchLen is the longest match PGLZ can encode: the 4-bit length
	// nibble saturates at 0x0f (=> base 18) and an optional extension byte
	// adds up to 255 more.
	maxMatchLen = 18 + 255
	// maxOffset is the largest back-reference distance the 12-bit offset
	// field can encode.
	maxOffset = 4095
	// histSize is PGLZ_HISTORY_SIZE (pg_lzcompress.c): the number of hash
	// buckets and, equivalently, the size of the position ring the match
	// search walks. It is one more than maxOffset, so a ring slot can only be
	// overwritten by a position that is already out of encodable range.
	histSize = 4096
	histMask = histSize - 1
	// goodMatch / goodDrop are upstream's default strategy knobs
	// (strategy_default_data): stop the history walk once a match this long is
	// found, and lower that bar by this percentage per visited candidate. They
	// bound the chain walk without which a highly repetitive input degenerates
	// to the old brute-force cost.
	goodMatch = 128
	goodDrop  = 10
)

// extSizeMask is VARLENA_EXTSIZE_MASK: the low 30 bits of va_tcinfo hold the
// original (decompressed) size; the top 2 bits hold the compression method.
const extSizeMask = (uint32(1) << 30) - 1

// Compress encodes data as a raw PGLZ token stream (WITHOUT the varlena header
// / va_tcinfo prefix — use BuildCompressedVarlena for the full on-disk value).
// The output is a valid PGLZ stream per pg_lzcompress.c and can be decoded by
// PostgreSQL's pglz_decompress as well as by Decompress. Returns nil for empty
// input.
//
// This is a greedy longest-match compressor over a 4095-byte window, using the
// same hash-chain matcher as PostgreSQL (pg_lzcompress.c pglz_find_match plus
// the default strategy's good_match/good_drop bounds). Its byte output for the
// same input may still differ from upstream's; that is fine, because any valid
// PGLZ token stream round-trips through either implementation's decompressor
// regardless of the encoder's match choices.
//
// review/260831 NB-17: the match search used to scan all 4095 history offsets
// per input byte, comparing up to 273 bytes each — ~1.1M comparisons per byte
// in the worst case, i.e. quadratic in the value size on the TOAST write path.
// The hash chain visits only positions whose next four bytes hash equal.
func Compress(data []byte) []byte {
	n := len(data)
	if n == 0 {
		return nil
	}
	out := make([]byte, 0, n/2+64)
	// head[h] is the most recent position whose four bytes hash to h, and
	// ring[pos&histMask] is the position before that one with the same hash —
	// upstream's hist_start[] / doubly-linked hist_entries[], flattened. A slot
	// is only reused after histSize further positions, by which point the old
	// occupant is out of the 12-bit offset range anyway, so no explicit
	// recycling is needed. -1 marks "no entry".
	var head [histSize]int32
	for h := range head {
		head[h] = -1
	}
	ring := make([]int32, histSize)
	for k := range ring {
		ring[k] = -1
	}
	addPos := func(pos int) {
		h := histIdx(data, pos, n)
		ring[pos&histMask] = head[h]
		head[h] = int32(pos)
	}
	i := 0
	for i < n {
		ctrlIdx := len(out)
		out = append(out, 0) // control-byte placeholder, filled in below
		var ctrl byte
		for bit := uint(0); bit < 8 && i < n; bit++ {
			bestLen, bestOff := 0, 0

			maxLen := n - i
			if maxLen > maxMatchLen {
				maxLen = maxMatchLen
			}
			// Walk this position's hash chain newest-first, exactly as
			// pglz_find_match does, stopping at the first candidate that has
			// fallen out of offset range (the chain is ordered by position, so
			// everything past it is out of range too).
			bar := goodMatch
			for cand := head[histIdx(data, i, n)]; cand >= 0; cand = ring[int(cand)&histMask] {
				off := i - int(cand)
				if off > maxOffset {
					break
				}
				l := 0
				for l < maxLen && data[int(cand)+l] == data[i+l] {
					l++
				}
				if l > bestLen {
					bestLen, bestOff = l, off
					if bestLen == maxLen {
						break
					}
				}
				// Be happy with lesser matches the deeper we walk.
				if bestLen >= bar {
					break
				}
				bar -= bar * goodDrop / 100
			}
			if bestLen < minMatchLen {
				bestLen, bestOff = 0, 0
			}

			if bestLen >= minMatchLen {
				// Match tag: control bit SET (1). First byte's low nibble is
				// (len-3) — saturating to 0x0f with an extension byte — and its
				// high nibble is the upper 4 bits of the offset. Second byte is
				// the low 8 bits of the offset.
				ctrl |= 1 << bit
				offHi := byte((bestOff >> 8) << 4)
				lenCode := bestLen - minMatchLen // 0..270
				if lenCode < 0x0f {
					out = append(out, byte(lenCode)|offHi, byte(bestOff&0xFF))
				} else {
					// Extended length: nibble 0x0f => base len 18, then one
					// extension byte carrying (len-18).
					out = append(out, 0x0f|offHi, byte(bestOff&0xFF), byte(bestLen-18))
				}
				for k := 0; k < bestLen; k++ {
					addPos(i + k)
				}
				i += bestLen
			} else {
				// Literal: control bit CLEAR (0); copy one byte verbatim.
				out = append(out, data[i])
				addPos(i)
				i++
			}
		}
		out[ctrlIdx] = ctrl
	}
	return out
}

// histIdx is pglz_hist_idx: the hash of the four bytes starting at pos, or of
// the single byte at pos when fewer than four remain.
func histIdx(data []byte, pos, end int) int {
	if end-pos < 4 {
		return int(data[pos]) & histMask
	}
	return (int(data[pos])<<6 ^ int(data[pos+1])<<4 ^ int(data[pos+2])<<2 ^ int(data[pos+3])) & histMask
}

// Decompress decodes a raw PGLZ token stream into exactly rawSize bytes. It
// mirrors pglz_decompress, including the overlapping run-length copy semantics
// used when a match offset is smaller than its length. It errors on a
// truncated or corrupt stream, or when the produced length does not equal
// rawSize.
func Decompress(src []byte, rawSize int) ([]byte, error) {
	if rawSize < 0 {
		return nil, fmt.Errorf("pglz: negative rawSize %d", rawSize)
	}
	dst := make([]byte, 0, rawSize)
	sp := 0
	for sp < len(src) && len(dst) < rawSize {
		ctrl := src[sp]
		sp++
		for bit := 0; bit < 8 && sp < len(src) && len(dst) < rawSize; bit++ {
			if ctrl&1 != 0 {
				// Match tag (2 bytes, plus an optional extension byte).
				if sp+2 > len(src) {
					return nil, fmt.Errorf("pglz: truncated match tag")
				}
				b0 := src[sp]
				b1 := src[sp+1]
				sp += 2
				length := int(b0&0x0f) + minMatchLen
				off := (int(b0&0xf0) << 4) | int(b1)
				if length == 18 {
					if sp >= len(src) {
						return nil, fmt.Errorf("pglz: truncated extension byte")
					}
					length += int(src[sp])
					sp++
				}
				// A zero offset or one reaching before the output start is
				// corrupt (and would otherwise loop forever).
				if off == 0 || off > len(dst) {
					return nil, fmt.Errorf("pglz: invalid offset %d at output pos %d", off, len(dst))
				}
				if remaining := rawSize - len(dst); length > remaining {
					length = remaining
				}
				// review/260831 NB-18: only an overlapping match (off <
				// length) needs the byte-at-a-time run-length expansion; the
				// common non-overlapping case is one copy. dst is pre-sized to
				// rawSize, so neither branch reallocates.
				start := len(dst) - off
				if off >= length {
					dst = append(dst, dst[start:start+length]...)
				} else {
					for k := 0; k < length; k++ {
						dst = append(dst, dst[start+k])
					}
				}
			} else {
				dst = append(dst, src[sp])
				sp++
			}
			ctrl >>= 1
		}
	}
	if len(dst) != rawSize {
		return nil, fmt.Errorf("pglz: decompressed %d bytes, want %d", len(dst), rawSize)
	}
	return dst, nil
}

// BuildCompressedVarlena wraps a PGLZ-compressed payload in the full
// inline-compressed 4-byte varlena on-disk representation (va_header +
// va_tcinfo + payload) for a value whose original length is rawSize.
func BuildCompressedVarlena(compressed []byte, rawSize int) []byte {
	total := 8 + len(compressed)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2|0x02) // VARATT_IS_4B_C
	binary.LittleEndian.PutUint32(buf[4:8], uint32(rawSize)&extSizeMask|
		uint32(CompressionMethodPGLZ)<<30) // va_tcinfo: rawsize | method<<30
	copy(buf[8:], compressed)
	return buf
}

// DecodeInlineCompressed parses an inline-compressed 4-byte varlena
// (VARATT_IS_4B_C) beginning at data[0] and returns the decompressed payload
// and the number of bytes the whole varlena occupied on disk. Only PGLZ
// (method 0) is supported; an LZ4 or unknown method is reported as an error.
func DecodeInlineCompressed(data []byte) ([]byte, int, error) {
	if len(data) < 8 {
		return nil, 0, fmt.Errorf("pglz: truncated compressed varlena header")
	}
	total := int(binary.LittleEndian.Uint32(data[:4]) >> 2)
	if total < 8 || total > len(data) {
		return nil, 0, fmt.Errorf("pglz: truncated compressed varlena (total=%d, have=%d)", total, len(data))
	}
	tcinfo := binary.LittleEndian.Uint32(data[4:8])
	rawSize := int(tcinfo & extSizeMask)
	method := tcinfo >> 30
	if method != CompressionMethodPGLZ {
		return nil, 0, fmt.Errorf("pglz: unsupported varlena compression method %d", method)
	}
	payload, err := Decompress(data[8:total], rawSize)
	if err != nil {
		return nil, 0, err
	}
	return payload, total, nil
}
