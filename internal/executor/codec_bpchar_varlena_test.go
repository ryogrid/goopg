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
	//
	// M0143-0007b slice 1: a width-carrying bpchar is stored BLANK-PADDED to
	// its declared width, as upstream's bpchar_input does, so the varlena
	// carries 84 characters here rather than the 5 that were written. That
	// growth is not incidental — it IS the fix: storing trimmed was the whole
	// of the K41 `relpages` gap M0143-0007 measured on `customer`/`item`.
	// What this test still pins is the ENCODING (a varlena, not a bare byte),
	// which is why the expectation is derived from the same padding helper the
	// production path uses instead of a hand-written byte list.
	expected := varlenaTextBytes(catalog.PadBpchar(typ, text))
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

func TestEncodeValuePGVarcharCoercesIntAndTrimsOnlyExcessSpaces(t *testing.T) {
	typ := catalog.Type{Name: "varchar", Args: []int64{1}}
	out, err := encodeValuePG(typ, NewIntDatum(2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := varlenaTextBytes("2"); !bytes.Equal(out, want) {
		t.Fatalf("encoded bytes = %v, want %v", out, want)
	}

	out, err = encodeValuePG(typ, NewStringDatum("c"))
	if err != nil {
		t.Fatalf("unexpected in-range error: %v", err)
	}
	if want := varlenaTextBytes("c"); !bytes.Equal(out, want) {
		t.Fatalf("in-range bytes = %v, want %v", out, want)
	}

	typ = catalog.Type{Name: "varchar", Args: []int64{3}}
	out, err = encodeValuePG(typ, NewStringDatum("x "))
	if err != nil {
		t.Fatalf("unexpected in-range trailing-space error: %v", err)
	}
	if want := varlenaTextBytes("x "); !bytes.Equal(out, want) {
		t.Fatalf("in-range trailing-space bytes = %v, want %v", out, want)
	}

	out, err = encodeValuePG(typ, NewStringDatum("abc   "))
	if err != nil {
		t.Fatalf("unexpected excess-space error: %v", err)
	}
	if want := varlenaTextBytes("abc"); !bytes.Equal(out, want) {
		t.Fatalf("excess-space bytes = %v, want %v", out, want)
	}
}

func TestEncodeValuePGVarcharRejectsTooLongValue(t *testing.T) {
	typ := catalog.Type{Name: "varchar", Args: []int64{1}}
	if _, err := encodeValuePG(typ, NewStringDatum("cd")); err == nil {
		t.Fatal("expected varchar(1) length error")
	}
}

func TestEncodeValuePGCharWithArgsCoercesIntAndRejectsTooLongValue(t *testing.T) {
	typ := catalog.Type{Name: "char", Args: []int64{1}}
	out, err := encodeValuePG(typ, NewIntDatum(2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := varlenaTextBytes("2"); !bytes.Equal(out, want) {
		t.Fatalf("encoded bytes = %v, want %v", out, want)
	}

	if _, err := encodeValuePG(typ, NewStringDatum("cd")); err == nil {
		t.Fatal("expected char(1) length error")
	}
}
