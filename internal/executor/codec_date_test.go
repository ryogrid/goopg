package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDateDecodeCarriesDateFlag pins the M0003 / 0003-0013 fix: a date value
// round-tripped through the on-disk physical codec must come back tagged as a
// DATE (TimeSubDate), not a bare timestamp. Date and timestamp share the KindTime
// carrier; only the flag distinguishes them in type-agnostic rendering
// (Datum.Format(), used by text casts, string concat, and array/composite
// element rendering). Before the fix, decodePhysicalPGValueMctx returned a
// flagless NewTimeDatum for a date column, so a storage-decoded date rendered
// as "2001-02-16 00:00:00.000000" instead of the date shape a literal produces.
func TestDateDecodeCarriesDateFlag(t *testing.T) {
	dateType := catalog.Type{Name: "date"}

	// A date literal (via the parser/expr path) carries TimeSubDate; a decoded
	// date must be byte-identical so downstream Format() paths can't tell them
	// apart.
	lit := NewDateDatum(time.Date(2001, 2, 16, 0, 0, 0, 0, time.UTC))

	enc, err := encodeValuePG(dateType, lit)
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	dec, n, err := decodePhysicalPGValueMctx(dateType, enc, nil)
	if err != nil {
		t.Fatalf("decodePhysicalPGValueMctx: %v", err)
	}
	if n != 4 {
		t.Errorf("date decode consumed %d bytes, want 4", n)
	}
	if !dec.IsDate() {
		t.Fatalf("decoded date lost TimeSubDate; Format()=%q, want date shape", dec.Format())
	}
	if got, want := dec.Format(), lit.Format(); got != want {
		t.Errorf("decoded date Format()=%q, want %q (identical to literal)", got, want)
	}
	// Sanity: a decoded timestamp must NOT be tagged as a date.
	tsType := catalog.Type{Name: "timestamp"}
	ts := NewTimeDatum(time.Date(2001, 2, 16, 12, 0, 0, 0, time.UTC))
	tsEnc, err := encodeValuePG(tsType, ts)
	if err != nil {
		t.Fatalf("encodeValuePG timestamp: %v", err)
	}
	tsDec, _, err := decodePhysicalPGValueMctx(tsType, tsEnc, nil)
	if err != nil {
		t.Fatalf("decodePhysicalPGValueMctx timestamp: %v", err)
	}
	if tsDec.IsDate() {
		t.Errorf("decoded timestamp wrongly tagged TimeSubDate; Format()=%q", tsDec.Format())
	}
}
