package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDecodeRowFallsThroughToPGFormatOnTrailingBytes verifies that
// decodeGoopgRowIntoMctx correctly signals failure (via "trailing bytes"
// error) when the data it receives is in PG physical little-endian format
// rather than goopg's flag+big-endian format.  This allows decodeRowIntoMctx
// to fall through to decodePhysicalPGRowIntoMctx and decode correctly.
//
// Regression test for M0107: clusters with PageHeaders=true use EncodeRowPG
// (PG physical format) for heap writes.  int4=1 is [0x01,0x00,0x00,0x00] in
// little-endian; the goopg decoder would read 0x01 as the NULL flag, return
// KindNull with no error, and prevent the fallback from running.
func TestDecodeRowFallsThroughToPGFormatOnTrailingBytes(t *testing.T) {
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}
	dst := make(Row, 1)

	// PG physical format for int4=1: little-endian [0x01, 0x00, 0x00, 0x00]
	pgData := []byte{0x01, 0x00, 0x00, 0x00}

	// decodeGoopgRowIntoMctx must return an error (trailing bytes) so the
	// caller can fall through to decodePhysicalPGRowIntoMctx.
	err := decodeGoopgRowIntoMctx(dst, cols, pgData, nil)
	if err == nil {
		t.Fatalf("decodeGoopgRowIntoMctx: expected trailing-bytes error for PG-format data, got nil (row=%v)", dst)
	}

	// The full DecodeRow path must successfully decode it as KindInt{1}.
	dst2 := make(Row, 1)
	if err2 := DecodeRowInto(dst2, cols, pgData); err2 != nil {
		t.Fatalf("DecodeRowInto: unexpected error for PG-format data: %v", err2)
	}
	if dst2[0].Kind != KindInt || dst2[0].Int != 1 {
		t.Fatalf("DecodeRowInto: got %v, want KindInt{1}", dst2[0])
	}
}

// TestDecodeRowGoopgFormatStillWorks ensures the trailing-bytes guard does not
// break normal goopg-format rows (flag byte + big-endian value).
func TestDecodeRowGoopgFormatStillWorks(t *testing.T) {
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}

	// goopg format for int4=1: [flag=0x00, 0x00, 0x00, 0x00, 0x01]
	goopgData := []byte{0x00, 0x00, 0x00, 0x00, 0x01}

	dst := make(Row, 1)
	if err := decodeGoopgRowIntoMctx(dst, cols, goopgData, nil); err != nil {
		t.Fatalf("decodeGoopgRowIntoMctx: unexpected error for goopg-format data: %v", err)
	}
	if dst[0].Kind != KindInt || dst[0].Int != 1 {
		t.Fatalf("decodeGoopgRowIntoMctx: got %v, want KindInt{1}", dst[0])
	}
}

// TestDecodeRowGoopgNullStillWorks ensures the trailing-bytes guard does not
// fire when a column is legitimately NULL in goopg format (only the flag byte).
func TestDecodeRowGoopgNullStillWorks(t *testing.T) {
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}

	// goopg NULL: just the null flag byte [0x01].
	nullData := []byte{0x01}

	dst := make(Row, 1)
	if err := decodeGoopgRowIntoMctx(dst, cols, nullData, nil); err != nil {
		t.Fatalf("decodeGoopgRowIntoMctx: unexpected error for NULL: %v", err)
	}
	if !dst[0].IsNull() {
		t.Fatalf("decodeGoopgRowIntoMctx: got %v, want NULL", dst[0])
	}
}
