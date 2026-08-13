package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (63rd slice). An `interval(N)` / `interval year to month` /
// `interval hour to second(p)` COLUMN's typmod was never applied at input:
// parseColumnType did not parse the range qualifier at all, and the value was
// stored whole. Upstream interval_in/interval_recv finish by calling
// AdjustIntervalForTypmod (timestamp.c:1355), which zeroes the fields to the
// right of the range's low field and rounds the sub-second field to the
// declared precision. These tests pin the adjustment at every input site, in
// the same shape as the time/timetz slice (time_typmod_rounding_test.go).

// packIntervalTypmod packs a range mask + precision into the INTERVAL_TYPMOD a
// column typmod carries: ((range & 0x7FFF) << 16) | (precision & 0xFFFF), with
// -1 precision meaning "full precision" (0xFFFF). The field bit positions are
// datetime.h's MONTH=1, YEAR=2, DAY=3, HOUR=10, MINUTE=11, SECOND=12.
func packIntervalTypmod(rng, prec int) int64 {
	p := 0xFFFF
	if prec >= 0 {
		p = prec
	}
	return (int64(rng) << 16) | int64(p)
}

const (
	imaskYear   = 1 << 2
	imaskMonth  = 1 << 1
	imaskDay    = 1 << 3
	imaskHour   = 1 << 10
	imaskMinute = 1 << 11
	imaskSecond = 1 << 12
)

// intervalTypmodCol returns a catalog.Type for an interval column with the given
// packed typmod; intervalBareCol is the typmod-free spelling.
func intervalTypmodCol(typmod int64) catalog.Type { return catalog.Type{Name: "interval", Args: []int64{typmod}} }
func intervalBareCol() catalog.Type          { return catalog.Type{Name: "interval"} }

func TestRoundIntervalDatumToTypmodYear(t *testing.T) {
	d, err := roundIntervalDatumToTypmod(
		NewIntervalDatumFull(14, 3, 14400000000), // 1 year 2 mons 3 days 04:00:00
		packIntervalTypmod(imaskYear, -1))
	if err != nil {
		t.Fatalf("roundIntervalDatumToTypmod: %v", err)
	}
	if m, d2, u := d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(); m != 12 || d2 != 0 || u != 0 {
		t.Fatalf("year typmod = (%d,%d,%d), want (12,0,0)", m, d2, u)
	}
}

func TestRoundIntervalDatumToTypmodSecondPrecision(t *testing.T) {
	d, err := roundIntervalDatumToTypmod(
		NewIntervalDatumFull(0, 0, 1234567), // 00:00:01.234567
		packIntervalTypmod(imaskSecond, 2))
	if err != nil {
		t.Fatalf("roundIntervalDatumToTypmod: %v", err)
	}
	if u := d.IntervalMicrosValue(); u != 1230000 {
		t.Fatalf("second(2) typmod = %d, want 1230000", u)
	}
}

func TestRoundIntervalDatumToTypmodStringEntry(t *testing.T) {
	// A bare quoted literal reaches the column through ParseIntervalBody (the
	// same tokenizer encodeValuePG uses); the year typmod zeroes days+time and
	// rounds months to the year.
	d, err := roundIntervalDatumToTypmod(
		NewStringDatum("1 year 2 mons 3 days 04:00:00"),
		packIntervalTypmod(imaskYear, -1))
	if err != nil {
		t.Fatalf("roundIntervalDatumToTypmod: %v", err)
	}
	if m, d2, u := d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(); m != 12 || d2 != 0 || u != 0 {
		t.Fatalf("year typmod = (%d,%d,%d), want (12,0,0)", m, d2, u)
	}
}

func TestRoundIntervalDatumToTypmodNoOp(t *testing.T) {
	// A bare interval column (typmod -1) and the ±infinity sentinel are no-ops.
	if d, err := roundIntervalDatumToTypmod(NewIntervalDatumFull(14, 3, 14400000000), -1); err != nil || d.IntervalMonthsValue() != 14 {
		t.Fatalf("typmod -1 changed the value: %v", err)
	}
	inf := NewIntervalDatumFull(math.MaxInt32, math.MaxInt32, math.MaxInt64)
	if d, err := roundIntervalDatumToTypmod(inf, packIntervalTypmod(imaskYear, -1)); err != nil || !d.IsIntervalNoEnd() {
		t.Fatalf("infinity was not preserved: %v", err)
	}
}

// COPY TEXT: the interval arm must round the same way INSERT does (Hard-won
// Rule #2 — the two readers of one type agree).
func TestCopyTextToDatumAppliesIntervalTypmod(t *testing.T) {
	d, err := copyTextToDatum(intervalTypmodCol(packIntervalTypmod(imaskYear, -1)), []byte("1 year 2 mons 3 days"), "")
	if err != nil {
		t.Fatalf("copyTextToDatum: %v", err)
	}
	if m, d2, u := d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(); m != 12 || d2 != 0 || u != 0 {
		t.Fatalf("COPY text interval year = (%d,%d,%d), want (12,0,0)", m, d2, u)
	}
}

// COPY BINARY: the recv twin must round identically.
func TestCopyBinaryToDatumAppliesIntervalTypmod(t *testing.T) {
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload[0:8], 14400000000) // 04:00:00
	binary.BigEndian.PutUint32(payload[8:12], 3)           // 3 days
	binary.BigEndian.PutUint32(payload[12:16], 14)         // 14 months
	d, err := copyBinaryToDatum(intervalTypmodCol(packIntervalTypmod(imaskYear, -1)), payload)
	if err != nil {
		t.Fatalf("copyBinaryToDatum: %v", err)
	}
	if m, d2, u := d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(); m != 12 || d2 != 0 || u != 0 {
		t.Fatalf("COPY binary interval year = (%d,%d,%d), want (12,0,0)", m, d2, u)
	}
}

// INSERT: coerceRowForConstraintChecks is the single coercion point every new
// row passes through; an interval column's value must be range/round-adjusted
// there.
func TestCoerceRowForConstraintChecksAppliesIntervalTypmod(t *testing.T) {
	cols := []catalog.Column{{Name: "i", Type: intervalTypmodCol(packIntervalTypmod(imaskYear, -1))}}
	row := Row{NewStringDatum("1 year 2 mons 3 days")}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, nil, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks: %v", err)
	}
	if row[0].Kind != KindInterval {
		t.Fatalf("row[0] kind = %d, want KindInterval (coerced)", row[0].Kind)
	}
	if m, d2, u := row[0].IntervalMonthsValue(), row[0].IntervalDaysValue(), row[0].IntervalMicrosValue(); m != 12 || d2 != 0 || u != 0 {
		t.Fatalf("INSERT interval year = (%d,%d,%d), want (12,0,0)", m, d2, u)
	}
}

// The storage choke point applies the typmod too, so a value that bypasses
// input coercion — a DEFAULT or generated column — is still adjusted before it
// reaches the heap.
func TestEncodeValuePGAppliesIntervalTypmod(t *testing.T) {
	b, err := encodeValuePG(intervalTypmodCol(packIntervalTypmod(imaskYear, -1)), NewStringDatum("1 year 2 mons 3 days 04:00:00"))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	if m := int32(binary.LittleEndian.Uint32(b[12:16])); m != 12 {
		t.Fatalf("encodeValuePG interval year months = %d, want 12", m)
	}
	if d2 := int32(binary.LittleEndian.Uint32(b[8:12])); d2 != 0 {
		t.Fatalf("encodeValuePG interval year days = %d, want 0", d2)
	}
	if u := int64(binary.LittleEndian.Uint64(b[0:8])); u != 0 {
		t.Fatalf("encodeValuePG interval year micros = %d, want 0", u)
	}
}

// A bare interval column (no typmod) stores the value whole, unchanged from
// before this slice.
func TestEncodeValuePGBareIntervalUnchanged(t *testing.T) {
	b, err := encodeValuePG(intervalBareCol(), NewStringDatum("1 year 2 mons 3 days"))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	if m := int32(binary.LittleEndian.Uint32(b[12:16])); m != 14 {
		t.Fatalf("bare interval months = %d, want 14", m)
	}
}

// format_type(interval, typmod) must render the range/precision qualifier the
// way PG's intervaltypmodout does, so a `interval year to month` column's
// atttypmod round-trips the declared spelling through pg_dump.
func TestFormatTypeOIDIntervalTypmod(t *testing.T) {
	cases := []struct {
		typmod int64
		want   string
	}{
		{-1, "interval"},
		{packIntervalTypmod(imaskYear|imaskMonth, -1), "interval year to month"},
		{packIntervalTypmod(imaskSecond, 2), "interval second(2)"},
		{packIntervalTypmod(0x7FFF, 2), "interval(2)"},
		{packIntervalTypmod(imaskDay|imaskHour|imaskMinute|imaskSecond, -1), "interval day to second"},
	}
	for _, c := range cases {
		if got := formatTypeOID(1186, c.typmod); got != c.want {
			t.Fatalf("formatTypeOID(1186, %d) = %q, want %q", c.typmod, got, c.want)
		}
	}
}
