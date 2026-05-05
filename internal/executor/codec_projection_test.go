package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDecodeRowProjectionSkipsNonKept (M0054-0005c-followup)
// asserts that DecodeRowProjection materialises only the columns
// flagged in `keep`. Non-kept columns must advance the offset
// (so subsequent kept columns decode correctly) but their payload
// must NOT be allocated — the test checks that varchar/numeric
// payloads do NOT appear in the dst row for non-kept columns.
func TestDecodeRowProjectionSkipsNonKept(t *testing.T) {
	cols := []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}, Ordinal: 1},
		{Name: "v", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
		{Name: "comment", Type: catalog.Type{Name: "varchar", Args: []int64{200}}, Ordinal: 3},
	}
	row := Row{
		{Kind: KindInt, Int: 42},
		{Kind: KindString, String: strings.Repeat("X", 30)},
		{Kind: KindInt, Int: 99},
		{Kind: KindString, String: strings.Repeat("Y", 100)},
	}
	encoded, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatal(err)
	}

	// Keep only k and v (the int4 columns); skip the two varchars.
	keep := []bool{true, false, true, false}
	dst := make(Row, len(cols))
	if err := DecodeRowProjection(dst, cols, encoded, keep); err != nil {
		t.Fatalf("DecodeRowProjection: %v", err)
	}
	// Kept columns must round-trip.
	if dst[0].Kind != KindInt || dst[0].Int != 42 {
		t.Errorf("dst[0] (k): kind=%d int=%d, want KindInt 42", dst[0].Kind, dst[0].Int)
	}
	if dst[2].Kind != KindInt || dst[2].Int != 99 {
		t.Errorf("dst[2] (v): kind=%d int=%d, want KindInt 99", dst[2].Kind, dst[2].Int)
	}
	// Non-kept columns must have empty/null payload (NullDatum is
	// the marker — caller must not read these slots).
	if dst[1].Kind == KindString && dst[1].String != "" {
		t.Errorf("dst[1] (name): unexpectedly materialised payload %q", dst[1].String)
	}
	if dst[3].Kind == KindString && dst[3].String != "" {
		t.Errorf("dst[3] (comment): unexpectedly materialised payload %q", dst[3].String)
	}

	// Sanity: full DecodeRow recovers all columns. Confirms the
	// encoding wasn't corrupted.
	full, err := DecodeRow(cols, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if full[1].String != strings.Repeat("X", 30) {
		t.Errorf("full[1]: got %q, want X*30", full[1].String)
	}
	if full[3].String != strings.Repeat("Y", 100) {
		t.Errorf("full[3]: got %q, want Y*100", full[3].String)
	}
}

// TestDecodeRowProjectionAllKeptMatchesDecodeRow asserts that with
// every column in `keep=true`, the projection variant produces
// the same row as DecodeRow — defensive check against future
// refactors that might diverge the two paths.
func TestDecodeRowProjectionAllKeptMatchesDecodeRow(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "varchar", Args: []int64{20}}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "numeric"}, Ordinal: 2},
	}
	// Use an integer datum for the numeric column — encoding
	// converts it to the varlen text form and decode round-trips
	// to KindNumeric. Avoids depending on a numeric-test helper.
	row := Row{
		{Kind: KindInt, Int: 7},
		{Kind: KindString, String: "hello"},
		{Kind: KindInt, Int: 314},
	}
	encoded, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatal(err)
	}
	keep := []bool{true, true, true}
	dstProj := make(Row, len(cols))
	if err := DecodeRowProjection(dstProj, cols, encoded, keep); err != nil {
		t.Fatal(err)
	}
	dstFull, err := DecodeRow(cols, encoded)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cols {
		if dstProj[i].Kind != dstFull[i].Kind {
			t.Errorf("col %d kind: proj=%d full=%d", i, dstProj[i].Kind, dstFull[i].Kind)
		}
	}
	if dstProj[0].Int != dstFull[0].Int {
		t.Errorf("a int: proj=%d full=%d", dstProj[0].Int, dstFull[0].Int)
	}
	if dstProj[1].String != dstFull[1].String {
		t.Errorf("b string: proj=%q full=%q", dstProj[1].String, dstFull[1].String)
	}
}
