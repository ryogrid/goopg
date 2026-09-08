package executor

// D-09 (MD-1x) gate: conditional alignment, both directions.
// Placement goldens (encode), peek table, backward read (old nominal
// bytes), forward read (PG-authored unaligned shorts), per-column
// STORAGE override, round-trips.

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

func alignCols() []catalog.Column {
	return []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
}

func TestAlignShortSkipsPad(t *testing.T) {
	// int2 (2 B) + short text: PG writes NO pad (fill_val short arm);
	// the old nominal encoder wrote 2 pad bytes here.
	body, err := EncodeRowPG(alignCols(), Row{NewIntDatum(7), NewStringDatum("hi")})
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// int2 LE 7, then short header (3<<1)|1 = 0x05, payload — 5 bytes.
	want := []byte{0x07, 0x00, 0x07, 'h', 'i'}
	if !bytes.Equal(body, want) {
		t.Fatalf("short text placement:\n got %x\nwant %x", body, want)
	}
}

func TestAlignLongKeepsPad(t *testing.T) {
	// 4-byte-header text still aligns to 4 after int2.
	payload := ""
	for i := 0; i < 300; i++ {
		payload += "x"
	}
	body, err := EncodeRowPG(alignCols(), Row{NewIntDatum(7), NewStringDatum(payload)})
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// int2 + 2 pad + 4-byte header (304<<2 LE) + payload.
	if len(body) != 2+2+4+300 {
		t.Fatalf("long text body len = %d, want %d", len(body), 2+2+4+300)
	}
	if body[2] != 0 || body[3] != 0 {
		t.Fatalf("long text must keep pad bytes, got %x", body[:6])
	}
	// total 304 << 2 = 1216 = 0x4C0 LE.
	if body[4] != 0xC0 || body[5] != 0x04 || body[6] != 0x00 || body[7] != 0x00 {
		t.Fatalf("long header wrong: %x", body[4:8])
	}
}

func TestAlignPlainStorageKeepsPad(t *testing.T) {
	// Per-column STORAGE override wins over the type default: a
	// PLAIN-overridden text column places ALIGNED even when short
	// (placement-only divergence — the encoder still writes the short
	// header; PG would write long+aligned, out of D-09 scope).
	cols := alignCols()
	cols[1].Storage = "plain"
	body, err := EncodeRowPG(cols, Row{NewIntDatum(7), NewStringDatum("hi")})
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	want := []byte{0x07, 0x00, 0x00, 0x00, 0x07, 'h', 'i'}
	if !bytes.Equal(body, want) {
		t.Fatalf("plain-storage placement:\n got %x\nwant %x", body, want)
	}
}

func TestIsShortVarlenaHeader(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"empty text 0x03", []byte{0x03}, true},
		{"max short 0xFF", []byte{0xFF}, true},
		{"toast marker 0x01 is not short", bytes.Repeat([]byte{0x01}, 13), false},
		{"4B zero-low-byte is not short", []byte{0x00, 0x01, 0x00, 0x00}, false},
		{"4B nonzero even is not short", []byte{0x40, 0x00, 0x00, 0x00}, false},
		{"empty buf", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShortVarlenaHeader(tc.buf); got != tc.want {
				t.Errorf("isShortVarlenaHeader(%x) = %v, want %v", tc.buf, got, tc.want)
			}
		})
	}
}

func TestAlignBackwardOldBytes(t *testing.T) {
	// Old nominal encoder output: int2 + 2 zero pad + short text.
	old := []byte{0x07, 0x00, 0x00, 0x00, 0x07, 'h', 'i'}
	dst := make(Row, 2)
	if err := decodePhysicalPGRowIntoMctx(dst, alignCols(), old, nil); err != nil {
		t.Fatalf("decode old bytes: %v", err)
	}
	if dst[0].Int != 7 || dst[1].StringValue() != "hi" {
		t.Fatalf("backward read wrong: %v", dst)
	}
}

func TestAlignForwardPGBytes(t *testing.T) {
	// PG-authored bytes: int2 + UNALIGNED short text (no pad) — the old
	// nominal decoder skipped onto the payload here.
	pg := []byte{0x07, 0x00, 0x07, 'h', 'i'}
	dst := make(Row, 2)
	if err := decodePhysicalPGRowIntoMctx(dst, alignCols(), pg, nil); err != nil {
		t.Fatalf("decode PG bytes: %v", err)
	}
	if dst[0].Int != 7 || dst[1].StringValue() != "hi" {
		t.Fatalf("forward read wrong: %v", dst)
	}
}

func TestAlignRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "int2"}},
		{Name: "d", Type: catalog.Type{Name: "text"}},
	}
	long := ""
	for i := 0; i < 500; i++ {
		long += "z"
	}
	rows := []Row{
		{NewIntDatum(1), NewStringDatum("a"), NewIntDatum(2), NewStringDatum("b")},
		{NewIntDatum(-3), NewStringDatum(long), NewIntDatum(4), NewStringDatum("")},
		{NewIntDatum(5), NullDatum, NewIntDatum(6), NewStringDatum("tail")},
	}
	for ri, row := range rows {
		body, err := EncodeRowPG(cols, row)
		if err != nil {
			t.Fatalf("row %d encode: %v", ri, err)
		}
		dst := make(Row, len(cols))
		// Header-less bodies carry no nulls positionally; rows with
		// NULLs round-trip through the bitmap path instead.
		if NullBitmapPG(row) != nil {
			bm := NullBitmapPG(row)
			if _, err := decodeRowRangeInfo(dst, cols, nil, body, bm, len(cols), nil, array.OutputStyle{}, 0, len(cols), 0); err != nil {
				t.Fatalf("row %d decode: %v", ri, err)
			}
		} else if err := decodePhysicalPGRowIntoMctx(dst, cols, body, nil); err != nil {
			t.Fatalf("row %d decode: %v", ri, err)
		}
		for i := range cols {
			if row[i].IsNull() != dst[i].IsNull() {
				t.Fatalf("row %d col %d null mismatch", ri, i)
			}
			if row[i].IsNull() {
				continue
			}
			if row[i].Int != dst[i].Int || row[i].StringValue() != dst[i].StringValue() {
				t.Fatalf("row %d col %d mismatch: %v vs %v", ri, i, row[i], dst[i])
			}
		}
	}
}

// TestAlignLivePGGolden pins D-09 byte-identity against LIVE PostgreSQL
// 18.3 (captured 2026-09-06, bench/tpch reference cluster :65432,
/// database d09gold):
//
//	CREATE TABLE d09(a int2, b text, c int4, d text, e int8);
//	INSERT INTO d09 VALUES (7, 'hi', 123456, repeat('x',300), 9);
//	CHECKPOINT; -- then tuple bytes read from base/<db>/17175 page 0.
//
// goopg's EncodeRowPG output was byte-identical to PG's 328 tuple bytes
// (verified in-session before pinning). The embedded prefix below is the
// first 12 live bytes (int2 + unaligned short + pad + int4); the long
// header + tail are asserted structurally (length + header + suffix)
// to keep the 300-byte payload out of the source.
func TestAlignLivePGGolden(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}},
		{Name: "d", Type: catalog.Type{Name: "text"}},
		{Name: "e", Type: catalog.Type{Name: "int8"}},
	}
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	row := Row{NewIntDatum(7), NewStringDatum("hi"), NewIntDatum(123456), NewStringDatum(long), NewIntDatum(9)}
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	if len(body) != 328 {
		t.Fatalf("golden len = %d, want 328 (live PG)", len(body))
	}
	// Live PG prefix: int2 7, short 'hi' at offset 2 with NO pad,
	// 3 pad bytes, int4 123456 LE.
	wantPrefix := []byte{0x07, 0x00, 0x07, 'h', 'i', 0x00, 0x00, 0x00, 0x40, 0xE2, 0x01, 0x00}
	if !bytes.Equal(body[:12], wantPrefix) {
		t.Fatalf("golden prefix:\n got %x\nwant %x", body[:12], wantPrefix)
	}
	// Live PG long header at 12 (304<<2 = 0x4C0 LE) and int8 tail
	// (4 pad + 9) — 'd'-align varlena (polygon/tsrange) excluded: the
	// physical align TABLE defaults them to 4 vs PG's 8 (ledgered as
	// take3-D-09-noted, separate mechanism from the conditional rule).
	if !bytes.Equal(body[12:16], []byte{0xC0, 0x04, 0x00, 0x00}) {
		t.Fatalf("golden long header: %x", body[12:16])
	}
	if !bytes.Equal(body[316:], append([]byte{0x00, 0x00, 0x00, 0x00}, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)) {
		t.Fatalf("golden tail: %x", body[316:])
	}
	// Forward read of PG-shaped bytes: the decoder must round-trip the
	// live layout (placement proof, not just self-consistency).
	dst := make(Row, len(cols))
	if err := decodePhysicalPGRowIntoMctx(dst, cols, body, nil); err != nil {
		t.Fatalf("forward decode: %v", err)
	}
	if dst[0].Int != 7 || dst[1].StringValue() != "hi" || dst[2].Int != 123456 ||
		len(dst[3].StringValue()) != 300 || dst[4].Int != 9 {
		t.Fatalf("forward values wrong: %v", dst)
	}
}

// TestAlignToastAmidShorts pins the TOAST-pointer interplay: a pointer
// blob (0x01 first byte — never a short header) still aligns to 4 even
// when surrounded by packed shorts, and round-trips as a pointer.
func TestAlignToastAmidShorts(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "json"}},
		{Name: "d", Type: catalog.Type{Name: "text"}},
	}
	ptr := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC}
	row := Row{NewIntDatum(7), NewStringDatum("hi"), NewToastPointerDatum(ptr), NewStringDatum("yo")}
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// int2[0:2], short 'hi' packed[2:5], pad[5:8], 13B pointer[8:21],
	// short 'yo' packed[21:24].
	if len(body) != 24 {
		t.Fatalf("body len = %d, want 24:\n%x", len(body), body)
	}
	if body[2] != 0x07 || body[8] != 0x01 {
		t.Fatalf("placement wrong:\n%x", body)
	}
	dst := make(Row, len(cols))
	if err := decodePhysicalPGRowIntoMctx(dst, cols, body, nil); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst[0].Int != 7 || dst[1].StringValue() != "hi" || dst[3].StringValue() != "yo" {
		t.Fatalf("values wrong: %v", dst)
	}
	if dst[2].Kind != KindToastPointer || !bytes.Equal(dst[2].BytesValue(), ptr) {
		t.Fatalf("pointer wrong: %v", dst[2])
	}
}

// TestAlignBackwardViaBitmap covers the old-nominal-bytes path the
// header-less backward test cannot: padded short + NULLs through the
// bitmap decoder (decodeRowRangeInfo).
func TestAlignBackwardViaBitmap(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}},
	}
	// Old encoder: int2, 2 pad, short 'hi', int4 NULL (no bytes).
	old := []byte{0x07, 0x00, 0x00, 0x00, 0x07, 'h', 'i'}
	bm := []byte{0x03} // a,b NOT NULL; c NULL
	dst := make(Row, len(cols))
	if _, err := decodeRowRangeInfo(dst, cols, nil, old, bm, len(cols), nil, array.OutputStyle{}, 0, len(cols), 0); err != nil {
		t.Fatalf("decode old bytes via bitmap: %v", err)
	}
	if dst[0].Int != 7 || dst[1].StringValue() != "hi" || !dst[2].IsNull() {
		t.Fatalf("backward bitmap read wrong: %v", dst)
	}
}

func TestEffectiveAttStorage(t *testing.T) {
	text := catalog.Column{Name: "b", Type: catalog.Type{Name: "text"}}
	if got := effectiveAttStorage(text); got != 'x' {
		t.Errorf("text default storage = %c, want x", got)
	}
	for col, want := range map[string]byte{
		"plain": 'p', "main": 'm', "external": 'e', "extended": 'x',
		"PLAIN": 'p',
	} {
		text.Storage = col
		if got := effectiveAttStorage(text); got != want {
			t.Errorf("override %q = %c, want %c", col, got, want)
		}
	}
	text.Storage = ""
	arr := catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	if got := effectiveAttStorage(arr); got != 'x' {
		t.Errorf("array storage = %c, want x", got)
	}
}
