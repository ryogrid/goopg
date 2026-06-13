package storage

import (
	"encoding/binary"
	"testing"
)

// makeChecksumTestPage returns a BlockSize page initialised like a real
// heap page (so PageChecksum's no-new-page precondition holds) with a few
// bytes of deterministic payload.
func makeChecksumTestPage() []byte {
	p := make([]byte, BlockSize)
	_ = InitPage(p)
	// Scatter some non-zero payload into the data area.
	for i := 0; i < 256; i++ {
		p[SizeOfPageHeaderData+i] = byte(i * 7)
	}
	return p
}

func TestPageChecksumNonZeroAndDeterministic(t *testing.T) {
	p := makeChecksumTestPage()
	c1 := PageChecksum(p, 0)
	if c1 == 0 {
		t.Fatal("PageChecksum returned 0; checksums must never be zero")
	}
	c2 := PageChecksum(p, 0)
	if c1 != c2 {
		t.Fatalf("PageChecksum not deterministic: %x vs %x", c1, c2)
	}
}

// TestPageChecksumIgnoresStoredChecksum verifies the pd_checksum field
// (bytes 8..10) is excluded from the computation: pre-setting it to any
// value must not change the result. This is the property that makes
// verify-after-store work.
func TestPageChecksumIgnoresStoredChecksum(t *testing.T) {
	p := makeChecksumTestPage()
	want := PageChecksum(p, 42)

	for _, stored := range []uint16{0x0000, 0x1234, 0xFFFF, want} {
		binary.LittleEndian.PutUint16(p[8:10], stored)
		got := PageChecksum(p, 42)
		if got != want {
			t.Fatalf("stored pd_checksum=%#04x changed result: got %#04x want %#04x", stored, got, want)
		}
	}
}

// TestPageChecksumDependsOnBlockNumber verifies the block number is mixed
// in, so an otherwise-identical page at a different block fails to match.
func TestPageChecksumDependsOnBlockNumber(t *testing.T) {
	p := makeChecksumTestPage()
	if PageChecksum(p, 0) == PageChecksum(p, 1) {
		t.Fatal("PageChecksum did not vary with block number")
	}
}

// TestPageChecksumDependsOnData verifies a single-byte change anywhere in
// the data area changes the checksum.
func TestPageChecksumDependsOnData(t *testing.T) {
	p := makeChecksumTestPage()
	base := PageChecksum(p, 7)
	p[BlockSize-1] ^= 0x01
	if PageChecksum(p, 7) == base {
		t.Fatal("PageChecksum did not change after flipping a data byte")
	}
}

// TestPageChecksumDependsOnFlags verifies the pd_flags field (bytes
// 10..12, the high half of the pd_checksum word) is included — only the
// low 16 bits at offset 8 are masked out, not the whole word.
func TestPageChecksumDependsOnFlags(t *testing.T) {
	p := makeChecksumTestPage()
	base := PageChecksum(p, 0)
	binary.LittleEndian.PutUint16(p[10:12], 0x4) // set a pd_flags bit
	if PageChecksum(p, 0) == base {
		t.Fatal("PageChecksum did not change after setting pd_flags")
	}
}
