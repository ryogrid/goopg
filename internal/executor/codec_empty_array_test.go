package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEmptyArrayTypeBytesShape verifies that emptyArrayTypeBytes
// produces the 16-byte PG-native serialisation that construct_empty_array
// would emit: a 4-byte uncompressed varlena header carrying total size
// 16, then ndim=0, dataoffset=0, elemtype=<elemType>. The byte layout
// must match exactly because PG's deconstruct_array casts the raw
// datum as ArrayType* and asserts ARR_ELEMTYPE == elmtype.
//
// Regression pin for M0106-0010.
func TestEmptyArrayTypeBytesShape(t *testing.T) {
	cases := []struct {
		name     string
		elemType uint32
	}{
		{"aclitem", 1033},
		{"text", 25},
		{"oid", 26},
		{"int2", 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := emptyArrayTypeBytes(tc.elemType)
			if len(buf) != 16 {
				t.Fatalf("expected 16-byte ArrayType, got %d bytes", len(buf))
			}
			// 4-byte uncompressed varlena header (LE): total << 2.
			gotHdr := binary.LittleEndian.Uint32(buf[0:4])
			wantHdr := uint32(16) << 2
			if gotHdr != wantHdr {
				t.Fatalf("vl_len_: got %#x, want %#x", gotHdr, wantHdr)
			}
			// Low 2 bits of byte 0 must be 00 (uncompressed 4-byte form).
			if buf[0]&0x03 != 0 {
				t.Fatalf("low 2 bits of byte 0 = %#x, want 0 (uncompressed varlena)", buf[0]&0x03)
			}
			if got := binary.LittleEndian.Uint32(buf[4:8]); got != 0 {
				t.Fatalf("ndim: got %d, want 0", got)
			}
			if got := binary.LittleEndian.Uint32(buf[8:12]); got != 0 {
				t.Fatalf("dataoffset: got %d, want 0", got)
			}
			if got := binary.LittleEndian.Uint32(buf[12:16]); got != tc.elemType {
				t.Fatalf("elemtype: got %d, want %d", got, tc.elemType)
			}
		})
	}
}

// TestEncodeValuePGAclItemArrayEmitsEmptyArrayType pins the encoder
// dispatch for "aclitem[]" / "text[]" against the binary ArrayType
// produced by emptyArrayTypeBytes, regardless of the Datum payload
// (so a stale string "{}" Datum still produces a valid empty array).
//
// Regression pin for M0106-0010: pg_class relacl/reloptions can no
// longer be written as a text varlena.
func TestEncodeValuePGAclItemArrayEmitsEmptyArrayType(t *testing.T) {
	cases := []struct {
		typeName string
		elemType uint32
	}{
		{"aclitem[]", 1033},
		{"_aclitem", 1033},
		{"text[]", 25},
		{"_text", 25},
	}
	for _, tc := range cases {
		t.Run(tc.typeName, func(t *testing.T) {
			// Any Datum the caller might supply; the encoder must
			// ignore the payload for these types.
			got, err := encodeValuePG(catalog.Type{Name: tc.typeName}, NewStringDatum("{}"))
			if err != nil {
				t.Fatalf("encodeValuePG: %v", err)
			}
			want := emptyArrayTypeBytes(tc.elemType)
			if len(got) != len(want) {
				t.Fatalf("len: got %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("byte %d: got %#x, want %#x", i, got[i], want[i])
				}
			}
		})
	}
}

// TestPhysicalPGTypeAlignArrayTypes pins the alignment used by
// encodeRowPG for varlena ArrayType / pg_node_tree columns. PG aligns
// ArrayType at 4 bytes ('i') because ArrayType's leading members are
// int32; a wrong alignment shifts every subsequent column and breaks
// nocachegetattr's physical-offset computation.
func TestPhysicalPGTypeAlignArrayTypes(t *testing.T) {
	for _, name := range []string{"aclitem[]", "_aclitem", "text[]", "_text", "pg_node_tree", "anyarray"} {
		if got := physicalPGTypeAlign(catalog.Type{Name: name}); got != 4 {
			t.Errorf("physicalPGTypeAlign(%q) = %d, want 4", name, got)
		}
	}
}
