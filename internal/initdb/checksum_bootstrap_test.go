package initdb

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// makeBootstrapPage returns a BlockSize page that is non-new (so it carries a
// checksum) with a recognisable fill so per-block transposition is detectable.
func makeBootstrapPage(fill byte) []byte {
	p := make([]byte, storage.BlockSize)
	for i := range p {
		p[i] = fill
	}
	// Clear the pd_checksum field (header bytes 8-9) so the input has no
	// pre-existing checksum; the helper must compute it.
	p[8], p[9] = 0, 0
	return p
}

func TestChecksumRelationDataDisabledIsIdentity(t *testing.T) {
	raw := append(makeBootstrapPage(0x11), makeBootstrapPage(0x22)...)
	got := checksumRelationData(raw, false)
	// Disabled must return the exact same backing slice (no copy, no alloc).
	if &got[0] != &raw[0] {
		t.Fatalf("disabled path allocated a copy; want the input slice verbatim")
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("disabled path mutated bytes")
	}
}

func TestChecksumRelationDataEnabledDoesNotMutateInput(t *testing.T) {
	raw := append(makeBootstrapPage(0x11), makeBootstrapPage(0x22)...)
	orig := append([]byte(nil), raw...)
	_ = checksumRelationData(raw, true)
	if !bytes.Equal(raw, orig) {
		t.Fatalf("enabled path mutated the caller's input slice")
	}
}

func TestChecksumRelationDataEnabledPerBlockChecksum(t *testing.T) {
	const nblocks = 4
	var raw []byte
	for i := range nblocks {
		raw = append(raw, makeBootstrapPage(byte(0x10+i))...)
	}
	got := checksumRelationData(raw, true)
	if len(got) != nblocks*storage.BlockSize {
		t.Fatalf("length changed: got %d want %d", len(got), nblocks*storage.BlockSize)
	}
	for i := range nblocks {
		block := got[i*storage.BlockSize : (i+1)*storage.BlockSize]
		if !storage.VerifyPage(block, storage.BlockNumber(i)) {
			t.Errorf("block %d does not verify at its own block number", i)
		}
		// Folding the block number in means a page checksummed for block i must
		// NOT verify at a different block number — this is the property that
		// catches a transposed-page bug in the offset→blkno derivation.
		if i > 0 && storage.VerifyPage(block, storage.BlockNumber(i-1)) {
			t.Errorf("block %d unexpectedly verifies at block %d (block number not folded in)", i, i-1)
		}
	}
}

func TestChecksumRelationDataEnabledSkipsNewPages(t *testing.T) {
	// An all-zero (new/uninitialised) page carries no checksum, exactly as
	// upstream PageIsNew skips it; the helper must leave it all-zero.
	raw := make([]byte, storage.BlockSize)
	got := checksumRelationData(raw, true)
	if !bytes.Equal(got, raw) {
		t.Fatalf("new (all-zero) page must be left untouched")
	}
	if !storage.VerifyPage(got, 0) {
		t.Fatalf("new page must verify")
	}
}

func TestChecksumRelationDataPartialTailVerbatim(t *testing.T) {
	// A trailing partial block (a bootstrap bug, but the helper must not
	// corrupt it) is copied through verbatim while full blocks are checksummed.
	raw := append(makeBootstrapPage(0x33), 0xAB, 0xCD)
	got := checksumRelationData(raw, true)
	if len(got) != len(raw) {
		t.Fatalf("length changed: got %d want %d", len(got), len(raw))
	}
	if !storage.VerifyPage(got[:storage.BlockSize], 0) {
		t.Errorf("leading full block must be checksummed")
	}
	tail := got[storage.BlockSize:]
	if !bytes.Equal(tail, []byte{0xAB, 0xCD}) {
		t.Errorf("partial tail must be copied verbatim, got %v", tail)
	}
}
