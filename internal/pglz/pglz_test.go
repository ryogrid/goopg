package pglz

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// TestRoundTrip exercises Compress -> Decompress over a range of inputs,
// including highly-repetitive data (which drives back-references, the
// extension byte for long matches, and overlapping run-length copies).
func TestRoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"empty":           {},
		"single":          {0x41},
		"short-literal":   []byte("hi"),
		"no-match":        []byte("abcdefghijklmnopqrstuvwxyz0123456789"),
		"repeat-short":    bytes.Repeat([]byte("ab"), 100), // RLE, off<len
		"repeat-run":      bytes.Repeat([]byte{0x5a}, 500), // single-byte RLE
		"long-match":      []byte(strings.Repeat("The quick brown fox. ", 40)),
		"pg_node_treeish": []byte(strings.Repeat("{QUERY :commandType 1 :querySource 0 :canSetTag true ", 60)),
		"mixed":           append(bytes.Repeat([]byte("xyz"), 50), []byte("unique-tail-1234567890")...),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			comp := Compress(in)
			got, err := Decompress(comp, len(in))
			if err != nil {
				t.Fatalf("Decompress: %v", err)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(in))
			}
		})
	}
}

// TestCompressActuallyShrinks confirms the compressor emits back-references
// (not just literals) for repetitive input — a stream of pure literals would
// never trigger the decode path we care about.
func TestCompressActuallyShrinks(t *testing.T) {
	in := bytes.Repeat([]byte("abcd"), 1000) // 4000 bytes, very compressible
	comp := Compress(in)
	if len(comp) >= len(in) {
		t.Fatalf("compressed %d bytes >= raw %d; compressor emitted no matches", len(comp), len(in))
	}
}

// TestDecompressPGSpecTokens decodes a hand-authored token stream built
// strictly to PostgreSQL's pg_lzcompress.c wire spec — independent of this
// package's own Compress — so a bug shared between our encoder and decoder
// cannot hide. It covers: literal bytes, a control byte processed LSB-first, a
// 2-byte match tag (low nibble = len-3, high nibble = offset high bits), the
// extension byte for len==18, and an overlapping (off<len) run-length copy.
func TestDecompressPGSpecTokens(t *testing.T) {
	// Build: literal 'A','B','C', then a match (len=3, off=3) copying "ABC".
	// control byte bits (LSB first): lit,lit,lit,match -> 0b1000 = 0x08.
	// match tag: b0 = (len-3)&0x0f | (off>>8)<<4 = 0 | 0 = 0x00, b1 = off&0xff = 3.
	stream := []byte{0x08, 'A', 'B', 'C', 0x00, 0x03}
	got, err := Decompress(stream, 6)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if want := []byte("ABCABC"); !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Overlapping RLE: literal 'X', then match len=5 off=1 -> "XXXXXX".
	// control: lit,match -> 0b10 = 0x02. tag: b0 = (5-3)=0x02, b1 = 1.
	rle := []byte{0x02, 'X', 0x02, 0x01}
	got, err = Decompress(rle, 6)
	if err != nil {
		t.Fatalf("Decompress rle: %v", err)
	}
	if want := []byte("XXXXXX"); !bytes.Equal(got, want) {
		t.Fatalf("rle got %q, want %q", got, want)
	}

	// Extension byte: literal 'Y', then a long match len=20 off=1.
	// len nibble saturates at 0x0f (base 18) + extension byte 2 -> 20.
	// control: lit,match -> 0x02. tag: b0 = 0x0f, b1 = 1, ext = 20-18 = 2.
	ext := []byte{0x02, 'Y', 0x0f, 0x01, 0x02}
	got, err = Decompress(ext, 21)
	if err != nil {
		t.Fatalf("Decompress ext: %v", err)
	}
	if want := bytes.Repeat([]byte("Y"), 21); !bytes.Equal(got, want) {
		t.Fatalf("ext got %q, want %q", got, want)
	}
}

// TestDecompressHighOffset checks the 12-bit offset reconstruction across the
// high-nibble/low-byte split (off = ((b0&0xf0)<<4) | b1).
func TestDecompressHighOffset(t *testing.T) {
	// 260 distinct-ish bytes then a match referencing offset 260.
	in := make([]byte, 0, 260)
	for i := 0; i < 260; i++ {
		in = append(in, byte(i))
	}
	full := append(append([]byte{}, in...), in[:5]...) // repeat first 5 bytes
	comp := Compress(full)
	got, err := Decompress(comp, len(full))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("high-offset round-trip mismatch")
	}
}

// TestDecompressCorrupt verifies corrupt/truncated streams are rejected rather
// than panicking or looping forever.
func TestDecompressCorrupt(t *testing.T) {
	cases := map[string][]byte{
		"truncated-match-tag": {0x01, 0x00},       // match flagged, only 1 tag byte
		"zero-offset":         {0x01, 0x00, 0x00}, // off=0 is invalid
		"offset-past-start":   {0x01, 0x00, 0x05}, // off=5 with nothing emitted
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decompress(s, 16); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
	// rawSize larger than the stream can produce.
	if _, err := Decompress([]byte{0x01, 'a'}, 100); err == nil {
		t.Fatalf("expected error for short output")
	}
}

// TestVarlenaFraming round-trips through the full inline-compressed varlena
// framing (BuildCompressedVarlena -> DecodeInlineCompressed) and checks the
// PG18 va_header / va_tcinfo bit layout.
func TestVarlenaFraming(t *testing.T) {
	raw := []byte(strings.Repeat("compress me please ", 200))
	comp := Compress(raw)
	blob := BuildCompressedVarlena(comp, len(raw))

	// va_header low 2 bits = 0b10 (VARATT_IS_4B_C); high bits = total size.
	vaHeader := binary.LittleEndian.Uint32(blob[0:4])
	if vaHeader&0x03 != 0x02 {
		t.Fatalf("va_header low bits = %#x, want 0x02", vaHeader&0x03)
	}
	if total := int(vaHeader >> 2); total != len(blob) {
		t.Fatalf("va_header total = %d, want %d", total, len(blob))
	}
	// va_tcinfo low 30 bits = rawsize, top 2 bits = method (0 = PGLZ).
	tcinfo := binary.LittleEndian.Uint32(blob[4:8])
	if rawSize := int(tcinfo & ((1 << 30) - 1)); rawSize != len(raw) {
		t.Fatalf("va_tcinfo rawsize = %d, want %d", rawSize, len(raw))
	}
	if method := tcinfo >> 30; method != CompressionMethodPGLZ {
		t.Fatalf("va_tcinfo method = %d, want %d (PGLZ)", method, CompressionMethodPGLZ)
	}

	payload, consumed, err := DecodeInlineCompressed(blob)
	if err != nil {
		t.Fatalf("DecodeInlineCompressed: %v", err)
	}
	if consumed != len(blob) {
		t.Fatalf("consumed = %d, want %d", consumed, len(blob))
	}
	if !bytes.Equal(payload, raw) {
		t.Fatalf("payload mismatch after framing round-trip")
	}
}

// TestDecodeInlineCompressedRejectsLZ4 confirms an unsupported compression
// method surfaces a clear error instead of mis-decoding.
func TestDecodeInlineCompressedRejectsLZ4(t *testing.T) {
	blob := BuildCompressedVarlena([]byte{0x00, 0x41}, 1)
	// Rewrite the method bits to 1 (LZ4).
	tcinfo := binary.LittleEndian.Uint32(blob[4:8])
	tcinfo = (tcinfo & ((1 << 30) - 1)) | (uint32(1) << 30)
	binary.LittleEndian.PutUint32(blob[4:8], tcinfo)
	if _, _, err := DecodeInlineCompressed(blob); err == nil {
		t.Fatalf("expected error for LZ4 method")
	}
}
