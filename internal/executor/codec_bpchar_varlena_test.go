package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEncodeValuePGCharNoArgs verifies that "char" without a length modifier
// (PG internal single-byte "char" type, OID 18) encodes as a single byte.
func TestEncodeValuePGCharNoArgs(t *testing.T) {
	typ := catalog.Type{Name: "char"} // no Args → internal "char", typlen=1
	d := NewStringDatum("A")
	out, err := encodeValuePG(typ, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != 'A' {
		t.Fatalf("expected single byte 'A', got %v", out)
	}
}

// TestEncodeValuePGCharWithArgs verifies that char(N) (bpchar, character(N))
// with a length modifier encodes as a PG varlena (header + data),
// NOT as a bare single byte. The pgbench filler column is character(84),
// and previously this returned a single byte which caused
// "DecodePhysicalPGRow: filler: truncated 4-byte varlena header" on read-back.
func TestEncodeValuePGCharWithArgs(t *testing.T) {
	// character(84) — bpchar with length modifier
	typ := catalog.Type{Name: "char", Args: []int64{84}}
	text := "hello"
	d := NewStringDatum(text)
	out, err := encodeValuePG(typ, d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must be a varlena: at minimum 2 bytes (1-byte header + 1 byte data for short).
	// A single bare byte was the old (wrong) encoding; any varlena is > 1 byte.
	if len(out) < 2 {
		t.Fatalf("expected varlena (≥2 bytes), got %d bytes: %v — this is the bare single-byte bug", len(out), out)
	}
	// Verify the payload bytes match by round-tripping through varlenaTextBytes.
	expected := varlenaTextBytes(text)
	if !bytes.Equal(out, expected) {
		t.Fatalf("encoded bytes = %v, want %v", out, expected)
	}
}

func TestEncodeValuePGCharWithArgsIsVarlena(t *testing.T) {
	bare := catalog.Type{Name: "char"}
	if pgPhysicalTypeIsVarlena(bare) {
		t.Error("char (no args) should NOT be varlena")
	}
	withN := catalog.Type{Name: "char", Args: []int64{84}}
	if !pgPhysicalTypeIsVarlena(withN) {
		t.Error("char(N) should be varlena")
	}
}

// TestEncodeValuePGCharAlignmentMatches checks physicalPGTypeAlign
// for the two char variants.
func TestEncodeValuePGCharAlignmentMatches(t *testing.T) {
	bare := catalog.Type{Name: "char"}
	if physicalPGTypeAlign(bare) != 1 {
		t.Errorf("char (no args) alignment: got %d, want 1", physicalPGTypeAlign(bare))
	}
	withN := catalog.Type{Name: "char", Args: []int64{84}}
	if physicalPGTypeAlign(withN) != 4 {
		t.Errorf("char(N) alignment: got %d, want 4", physicalPGTypeAlign(withN))
	}
}


