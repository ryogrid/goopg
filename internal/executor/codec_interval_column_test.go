package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The interval-column storage slice of M0119-0006. Until it landed, an
// `interval` column fell through encodeValuePG's varlena default arm and was
// stored as the literal text the user typed, so every runtime interval
// operation was lexicographic: ORDER BY put '2 hours' after '10 days',
// `i > interval '10 days'` kept '2 hours' and dropped '1 mon', `i = interval
// '30 days'` missed the '1 mon' PG calls equal, and the column echoed back
// '2 hours' where PG prints '02:00:00'. Each expectation below was captured
// from the PostgreSQL 18.3 reference cluster on port 65432.

func intervalType() catalog.Type { return catalog.Type{Name: "interval"} }

// TestIntervalColumnUsesPGNativeStructLayout pins the on-disk bytes to PG's
// Interval struct (postgres/src/include/datatype/timestamp.h):
//
//	typedef struct { TimeOffset time; int32 day; int32 month; } Interval;
//
// time at offset 0, day at 8, month at 12 — the layout a PG standby reading
// goopg's heap has to find. Swapping the day and month words still round-trips
// through goopg's own decoder, so the offsets are asserted directly rather than
// only via the round trip below.
func TestIntervalColumnUsesPGNativeStructLayout(t *testing.T) {
	// 1 year 2 mons 3 days 04:05:06.5 → months=14, days=3, micros=14706500000.
	d := NewIntervalDatumFull(14, 3, 14706500000)
	enc, err := encodeValuePG(intervalType(), d)
	if err != nil {
		t.Fatalf("encodeValuePG(interval): %v", err)
	}
	if len(enc) != 16 {
		t.Fatalf("encoded %d bytes, want 16 (pg_type OID 1186 typlen)", len(enc))
	}
	if got := int64(binary.LittleEndian.Uint64(enc[:8])); got != 14706500000 {
		t.Errorf("time field at offset 0 = %d, want 14706500000", got)
	}
	if got := int32(binary.LittleEndian.Uint32(enc[8:12])); got != 3 {
		t.Errorf("day field at offset 8 = %d, want 3", got)
	}
	if got := int32(binary.LittleEndian.Uint32(enc[12:16])); got != 14 {
		t.Errorf("month field at offset 12 = %d, want 14", got)
	}
}

// TestIntervalColumnIsFixedWidthNotVarlena guards the two predicates that
// decide how the tuple builder treats the column. A wrong answer here does not
// corrupt the interval field itself — it shifts every *following* column, and
// pgPhysicalTypeIsVarlena additionally drives the HEAP_HASVARWIDTH infomask bit
// that PG18's nocachegetattr fast path asserts on.
func TestIntervalColumnIsFixedWidthNotVarlena(t *testing.T) {
	if got := physicalPGTypeAlign(intervalType()); got != 8 {
		t.Errorf("physicalPGTypeAlign(interval) = %d, want 8 (typalign 'd')", got)
	}
	if pgPhysicalTypeIsVarlena(intervalType()) {
		t.Error("pgPhysicalTypeIsVarlena(interval) = true, want false (typlen 16)")
	}
}

// TestIntervalColumnRoundTripsEveryFieldShape round-trips the shapes that broke
// under text storage, including the ±infinity sentinels (which need no special
// case precisely because storage is field-wise: INTERVAL_NOEND / NOBEGIN *are*
// all fields at their signed extreme).
func TestIntervalColumnRoundTripsEveryFieldShape(t *testing.T) {
	cases := []struct {
		name         string
		months, days int32
		micros       int64
		wantText     string
	}{
		{"month only", 1, 0, 0, "1 mon"},
		{"days equal to that month", 0, 30, 0, "30 days"},
		{"sub-day only", 0, 0, 2 * 3600 * 1_000_000, "02:00:00"},
		{"negative mixed", 0, -1, 2 * 3600 * 1_000_000, "-1 days +02:00:00"},
		{"one microsecond", 0, 0, 1, "00:00:00.000001"},
		{"zero", 0, 0, 0, "00:00:00"},
		{"max days", 0, math.MaxInt32, 0, "2147483647 days"},
		{"infinity", math.MaxInt32, math.MaxInt32, math.MaxInt64, "infinity"},
		{"-infinity", math.MinInt32, math.MinInt32, math.MinInt64, "-infinity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := NewIntervalDatumFull(tc.months, tc.days, tc.micros)
			enc, err := encodeValuePG(intervalType(), in)
			if err != nil {
				t.Fatalf("encodeValuePG: %v", err)
			}
			got, n, err := decodePhysicalPGValueMctx(intervalType(), enc, nil)
			if err != nil {
				t.Fatalf("decodePhysicalPGValueMctx: %v", err)
			}
			if n != 16 {
				t.Fatalf("decode consumed %d bytes, want 16", n)
			}
			if got.Kind != KindInterval {
				t.Fatalf("decoded kind=%v, want KindInterval (text storage returned KindString)", got.Kind)
			}
			if got.IntervalMonthsValue() != tc.months || got.IntervalDaysValue() != tc.days ||
				got.IntervalMicrosValue() != tc.micros {
				t.Fatalf("round trip = (%d,%d,%d), want (%d,%d,%d)",
					got.IntervalMonthsValue(), got.IntervalDaysValue(), got.IntervalMicrosValue(),
					tc.months, tc.days, tc.micros)
			}
			if txt := got.Format(); txt != tc.wantText {
				t.Errorf("Format() = %q, want %q (PG 18.3)", txt, tc.wantText)
			}
		})
	}
}

// TestIntervalColumnAcceptsUnknownLiteral covers the INSERT shape that actually
// reaches storage: `INSERT INTO t(i) VALUES ('1 mon')` is an `unknown` literal
// upstream and lands via interval_in, so the KindString arm must produce bytes
// identical to the already-typed KindInterval datum. Sibling-agreement between
// the two entry points is the whole point (they share parser.ParseIntervalBody).
func TestIntervalColumnAcceptsUnknownLiteral(t *testing.T) {
	fromText, err := encodeValuePG(intervalType(), NewStringDatum("1 year 2 mons 3 days 04:05:06.5"))
	if err != nil {
		t.Fatalf("encodeValuePG(string): %v", err)
	}
	fromDatum, err := encodeValuePG(intervalType(), NewIntervalDatumFull(14, 3, 14706500000))
	if err != nil {
		t.Fatalf("encodeValuePG(interval): %v", err)
	}
	if string(fromText) != string(fromDatum) {
		t.Fatalf("unknown-literal bytes %x != typed-datum bytes %x", fromText, fromDatum)
	}
}

// TestIntervalColumnRejectsUnparseableText: text storage accepted literally
// anything, so a typo silently became a row. PG raises 22007 from interval_in.
func TestIntervalColumnRejectsUnparseableText(t *testing.T) {
	_, err := encodeValuePG(intervalType(), NewStringDatum("not an interval"))
	if err == nil {
		t.Fatal("expected an error for unparseable interval text")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22007" {
		t.Fatalf("err = %v, want ExecError 22007", err)
	}
}

// TestIntervalColumnComparesAsIntervalNotText is the defect this slice exists
// for: the stored value must reach compareDatum's interval_cmp_value port. Each
// expectation is the PG 18.3 answer, and each was WRONG under text storage.
func TestIntervalColumnComparesAsIntervalNotText(t *testing.T) {
	store := func(text string) Datum {
		t.Helper()
		enc, err := encodeValuePG(intervalType(), NewStringDatum(text))
		if err != nil {
			t.Fatalf("encodeValuePG(%q): %v", text, err)
		}
		d, _, err := decodePhysicalPGValueMctx(intervalType(), enc, nil)
		if err != nil {
			t.Fatalf("decode(%q): %v", text, err)
		}
		return d
	}
	cases := []struct {
		a, b string
		want int
	}{
		// PG calls a month and 30 days equal (interval_cmp_value widens months
		// at a flat 30/month); text storage said "1 mon" < "30 days".
		{"1 mon", "30 days", 0},
		// Text storage said "2 hours" > "10 days" ('2' > '1').
		{"2 hours", "10 days", -1},
		{"400 days", "1 mon", 1},
		{"-infinity", "1 mon", -1},
		{"infinity", "2147483647 days", 1},
	}
	for _, tc := range cases {
		got, err := compareDatum(store(tc.a), store(tc.b), 0)
		if err != nil {
			t.Fatalf("compareDatum(%q,%q): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("compareDatum(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestIntervalColumnComparesAgainstUnknownLiteral covers `i > '10 days'`, where
// only one side is stored. PG coerces the unknown literal to interval in
// transformExpr; goopg has no such pass, so promoteCrossKind/tryParseStringAs
// has to do it or the pair falls back to Format()-vs-Format() — text comparison
// again, one level down.
func TestIntervalColumnComparesAgainstUnknownLiteral(t *testing.T) {
	enc, err := encodeValuePG(intervalType(), NewStringDatum("1 mon"))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	stored, _, err := decodePhysicalPGValueMctx(intervalType(), enc, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// '1 mon' > '10 days' in PG; the Format()-vs-Format() fallback says the
	// opposite because "1 mon" sorts before "10 days" bytewise.
	got, err := compareDatum(stored, NewStringDatum("10 days"), 0)
	if err != nil {
		t.Fatalf("compareDatum: %v", err)
	}
	if got != 1 {
		t.Errorf("compareDatum('1 mon' stored, '10 days' literal) = %d, want 1", got)
	}
	// Equal-by-PG pair through the same asymmetric path.
	got, err = compareDatum(stored, NewStringDatum("30 days"), 0)
	if err != nil {
		t.Fatalf("compareDatum: %v", err)
	}
	if got != 0 {
		t.Errorf("compareDatum('1 mon' stored, '30 days' literal) = %d, want 0", got)
	}
}

// TestIntervalColumnRowRoundTripKeepsNeighbourColumns walks a whole tuple with
// an interval between neighbours of every other width class, mirroring the
// mixed-column table used for the PG-oracle diff. It catches a width mistake
// (16 vs anything else) because the walker's cursor then lands mid-field for
// every following column.
//
// It deliberately does NOT catch a wrong *alignment* or a wrong varlena
// verdict: EncodeRowPG and the decode walker consult the same two predicates,
// so a matched pair of wrong answers round-trips through goopg perfectly and
// only diverges from what a PG reader computes from the TupleDesc. Those two
// values are pinned against pg_type directly in
// TestIntervalColumnIsFixedWidthNotVarlena, which is where a mutation of either
// one shows up.
func TestIntervalColumnRowRoundTripKeepsNeighbourColumns(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "i", Type: intervalType()},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "int8"}},
		{Name: "j", Type: intervalType()},
	}
	row := Row{
		NewIntDatum(7),
		NewIntervalDatumFull(14, 3, 14706500000),
		NewStringDatum("neighbour"),
		NewIntDatum(1234567890123),
		NewIntervalDatumFull(0, -1, 2*3600*1_000_000),
	}
	enc, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	out := make(Row, len(cols))
	if err := decodePhysicalPGRowIntoMctx(out, cols, enc, nil); err != nil {
		t.Fatalf("decodePhysicalPGRowIntoMctx: %v", err)
	}
	if out[0].Int != 7 {
		t.Errorf("col a = %d, want 7", out[0].Int)
	}
	if got := out[1].Format(); got != "1 year 2 mons 3 days 04:05:06.5" {
		t.Errorf("col i = %q", got)
	}
	if got := out[2].StringValue(); got != "neighbour" {
		t.Errorf("col b = %q, want %q (interval width/align shifted it)", got, "neighbour")
	}
	if out[3].Int != 1234567890123 {
		t.Errorf("col c = %d, want 1234567890123", out[3].Int)
	}
	if got := out[4].Format(); got != "-1 days +02:00:00" {
		t.Errorf("col j = %q", got)
	}
}
