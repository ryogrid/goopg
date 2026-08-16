package executor

import (
	"bytes"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestCopyBinaryRoundTrip is the DoD test for M0049-0004.
//
// Round-trip every supported COPY BINARY type: int4, int8, bool,
// text, varchar, timestamp, date, numeric.
func TestCopyBinaryRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "cnt", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
		{Name: "flag", Type: catalog.Type{Name: "bool"}, Ordinal: 2},
		{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 3},
		{Name: "ts", Type: catalog.Type{Name: "timestamp"}, Ordinal: 4},
		{Name: "dt", Type: catalog.Type{Name: "date"}, Ordinal: 5},
		{Name: "price", Type: catalog.Type{Name: "numeric"}, Ordinal: 6},
	}

	ts := time.Date(2024, 6, 15, 12, 34, 56, 0, time.UTC)
	dt := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	priceM, priceS, _ := parseNumeric("99.99")
	price, _ := newNumeric(priceM, int(priceS)), error(nil)
	_ = price

	rows := []Row{
		{
			Datum{Kind: KindInt, Int: 1},
			Datum{Kind: KindInt, Int: 1000000},
			NewBoolDatum(true),
			NewStringDatum("hello world"),
			NewTimeDatum(ts),
			NewTimeDatum(dt),
			newNumeric(priceM, int(priceS)),
		},
		{
			Datum{Kind: KindInt, Int: 2},
			Datum{Kind: KindInt, Int: -42},
			NewBoolDatum(false),
			NewStringDatum(""),
			NewTimeDatum(pgEpoch),
			NewTimeDatum(pgEpoch),
			newNumeric(priceM, int(priceS)),
		},
		{
			NullDatum, NullDatum, NullDatum, NullDatum, NullDatum, NullDatum, NullDatum,
		},
	}

	// Encode.
	var buf []byte
	buf = append(buf, CopyBinaryHeader()...)
	for _, row := range rows {
		var err error
		buf, err = AppendCopyBinaryRow(buf, row, cols)
		if err != nil {
			t.Fatalf("AppendCopyBinaryRow: %v", err)
		}
	}
	buf = AppendCopyBinaryTrailer(buf)

	// Parse header.
	n, err := ParseCopyBinaryHeader(buf)
	if err != nil {
		t.Fatalf("ParseCopyBinaryHeader: %v", err)
	}

	// Decode rows.
	decoded, trailerFound, _, parseErr := ParseCopyBinaryRows(buf[n:], cols)
	if parseErr != nil {
		t.Fatalf("ParseCopyBinaryRows: %v", parseErr)
	}
	if !trailerFound {
		t.Error("trailer not found after decoding rows")
	}
	if len(decoded) != len(rows) {
		t.Fatalf("decoded %d rows, want %d", len(decoded), len(rows))
	}

	// Verify each row.
	for ri, wantRow := range rows {
		gotRow := decoded[ri]
		for ci, want := range wantRow {
			got := gotRow[ci]
			if want.IsNull() {
				if !got.IsNull() {
					t.Errorf("row %d col %d: want NULL, got %v", ri, ci, got)
				}
				continue
			}
			if got.IsNull() {
				t.Errorf("row %d col %d: want %v, got NULL", ri, ci, want)
				continue
			}
			if !datumEqual(want, got, cols[ci]) {
				t.Errorf("row %d col %s: want %v, got %v", ri, cols[ci].Name, want.Format(), got.Format())
			}
		}
	}
}

// TestCopyBinaryHeaderSignature verifies the 19-byte header.
func TestCopyBinaryHeaderSignature(t *testing.T) {
	hdr := CopyBinaryHeader()
	if len(hdr) != 19 {
		t.Fatalf("header length = %d, want 19", len(hdr))
	}
	want := []byte("PGCOPY\n\377\r\n\000")
	if !bytes.Equal(hdr[:11], want) {
		t.Errorf("signature = %q, want %q", hdr[:11], want)
	}
	// flags and ext-area-len must be zero
	for i := 11; i < 19; i++ {
		if hdr[i] != 0 {
			t.Errorf("hdr[%d] = %02x, want 0", i, hdr[i])
		}
	}
}

// TestCopyBinaryRoundTripViaExecutor exercises the full RunCopyTo →
// ParseCopyBinaryRows path end-to-end against a real storage-backed table.
func TestCopyBinaryRoundTripViaExecutor(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// M0129-S8.3: advance the command counter so RunCopyTo sees the seed rows.
	advanceStmtCounter(ctx)

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyTo,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdout,
		Options:     []parser.CopyOption{{Name: "binary", Bool: true}},
	}

	var outBuf []byte
	_, isBin, err := RunCopyTo(ctx, plan, func(b []byte) error {
		outBuf = append(outBuf, b...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isBin {
		t.Error("RunCopyTo did not detect binary format")
	}

	// Verify header.
	n, err := ParseCopyBinaryHeader(outBuf)
	if err != nil {
		t.Fatalf("ParseCopyBinaryHeader: %v", err)
	}

	cols := tbl.Columns
	decoded, trailer, _, err := ParseCopyBinaryRows(outBuf[n:], cols)
	if err != nil {
		t.Fatalf("ParseCopyBinaryRows: %v", err)
	}
	if !trailer {
		t.Error("binary trailer not found")
	}
	if len(decoded) != 3 {
		t.Errorf("decoded %d rows, want 3", len(decoded))
	}
	// First row should have id=1, label=alpha.
	if len(decoded) > 0 {
		if decoded[0][0].Int != 1 {
			t.Errorf("row[0].id = %d, want 1", decoded[0][0].Int)
		}
		if decoded[0][1].StringValue() != "alpha" {
			t.Errorf("row[0].label = %q, want alpha", decoded[0][1].StringValue())
		}
	}
}

// datumEqual compares two Datums for equality at the type level.
func datumEqual(a, b Datum, col catalog.Column) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case KindInt:
		return a.Int == b.Int
	case KindBool:
		return a.BoolValue() == b.BoolValue()
	case KindString:
		return a.StringValue() == b.StringValue()
	case KindBytes:
		return bytes.Equal(a.BytesValue(), b.BytesValue())
	case KindTime:
		// Compare at microsecond precision (binary timestamp precision).
		return a.TimeValue().Unix() == b.TimeValue().Unix() &&
			a.TimeValue().Nanosecond()/1000 == b.TimeValue().Nanosecond()/1000
	case KindNumeric:
		return a.Format() == b.Format()
	}
	return false
}
