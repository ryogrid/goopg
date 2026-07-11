package executor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/pglz"
)

// TestDecodePhysicalPGVarlenaCompressed verifies that decodePhysicalPGVarlena
// transparently decompresses an inline PGLZ-compressed varlena
// (VARATT_IS_4B_C) instead of the pre-fix "compressed varlena not supported"
// error. This is the executor twin of wal.pgoDecodePhysicalVarlena — both must
// agree (pattern_sibling_paths_must_agree).
func TestDecodePhysicalPGVarlenaCompressed(t *testing.T) {
	raw := []byte(strings.Repeat("{QUERY :commandType 1 :canSetTag true} ", 80))
	blob := pglz.BuildCompressedVarlena(pglz.Compress(raw), len(raw))

	payload, n, err := decodePhysicalPGVarlena(blob)
	if err != nil {
		t.Fatalf("decodePhysicalPGVarlena(compressed): %v", err)
	}
	if n != len(blob) {
		t.Fatalf("consumed = %d, want %d", n, len(blob))
	}
	if !bytes.Equal(payload, raw) {
		t.Fatalf("decompressed payload mismatch")
	}

	// An uncompressed 4-byte varlena still decodes unchanged.
	un := varlenaTextBytes("plain value")
	got, _, err := decodePhysicalPGVarlena(un)
	if err != nil {
		t.Fatalf("decodePhysicalPGVarlena(uncompressed): %v", err)
	}
	if string(got) != "plain value" {
		t.Fatalf("uncompressed decode = %q, want %q", got, "plain value")
	}
}
