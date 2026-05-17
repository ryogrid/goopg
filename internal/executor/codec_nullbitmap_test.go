package executor

import (
	"bytes"
	"testing"
)

// TestNullBitmapPGUsesPGConvention pins NullBitmapPG against PG's
// heap_fill_tuple convention: bit i is set when column i is NOT NULL,
// cleared when column i is NULL. Before M0106-0010 step 3i, the
// codec used the inverted convention and prepended the bitmap inside
// the column data area, breaking every catalog tuple PG decoded.
func TestNullBitmapPGUsesPGConvention(t *testing.T) {
	row := Row{
		NewIntDatum(1),
		NullDatum,
		NewIntDatum(3),
		NullDatum,
		NewIntDatum(5),
	}
	got := NullBitmapPG(row)
	want := []byte{0x15} // bits 0,2,4 set (cols 1,3,5 NOT NULL)
	if !bytes.Equal(got, want) {
		t.Fatalf("NullBitmapPG: got 0x%02x, want 0x%02x", got, want)
	}
}

// TestNullBitmapPGNilWhenNoNulls confirms the function returns nil
// (not an empty slice) for rows without any NULLs, so callers can
// gate HEAP_HASNULL on a nil check.
func TestNullBitmapPGNilWhenNoNulls(t *testing.T) {
	row := Row{NewIntDatum(1), NewIntDatum(2), NewIntDatum(3)}
	if got := NullBitmapPG(row); got != nil {
		t.Fatalf("NullBitmapPG: got %v, want nil", got)
	}
}

// TestNullBitmapPGSpansTwoBytes pins multi-byte bitmap layout: bit
// numbering is little-endian within each byte (bit 0 = first attr in
// the byte). A 21-column row with cols 20-21 NULL must produce
// {0xFF, 0xFF, 0x07} — exactly the bitmap that pg_index seeds emit
// for indexprs/indpred NULL columns.
func TestNullBitmapPGSpansTwoBytes(t *testing.T) {
	row := make(Row, 21)
	for i := 0; i < 19; i++ {
		row[i] = NewIntDatum(int64(i + 1))
	}
	row[19] = NullDatum // col 20
	row[20] = NullDatum // col 21
	got := NullBitmapPG(row)
	want := []byte{0xFF, 0xFF, 0x07}
	if !bytes.Equal(got, want) {
		t.Fatalf("NullBitmapPG: got %x, want %x", got, want)
	}
}
