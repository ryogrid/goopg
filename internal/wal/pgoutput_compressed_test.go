package wal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/pglz"
)

// TestPgoDecodePhysicalVarlenaCompressed verifies pgoDecodePhysicalVarlena
// decompresses an inline PGLZ-compressed varlena so logical-replication decode
// of a compressed (TOASTed) column value succeeds rather than failing with
// "compressed varlena not supported". Twin of
// executor.decodePhysicalPGVarlena (pattern_sibling_paths_must_agree).
func TestPgoDecodePhysicalVarlenaCompressed(t *testing.T) {
	raw := []byte(strings.Repeat("logical-replication payload; ", 90))
	blob := pglz.BuildCompressedVarlena(pglz.Compress(raw), len(raw))

	payload, n, err := pgoDecodePhysicalVarlena(blob)
	if err != nil {
		t.Fatalf("pgoDecodePhysicalVarlena(compressed): %v", err)
	}
	if n != len(blob) {
		t.Fatalf("consumed = %d, want %d", n, len(blob))
	}
	if !bytes.Equal(payload, raw) {
		t.Fatalf("decompressed payload mismatch")
	}
}
