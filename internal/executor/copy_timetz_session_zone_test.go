package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (48th slice). The 47th slice made a zone-less `timetz` LITERAL
// inherit the session TimeZone; COPY FROM kept passing "" and so kept storing
// +00. PG 18.3 on 127.0.0.1:65432, verified for this slice:
//
//	SET TimeZone='Asia/Tokyo';
//	CREATE TABLE zz_ttz(t timetz);
//	COPY zz_ttz FROM STDIN;   -- field: 10:00:00
//	SELECT t FROM zz_ttz;     -- 10:00:00+09
//
// The offset is fixed at input time, so re-reading under another zone still
// shows +09. Upstream mechanism: DecodeTimeOnly's `!(fmask & DTK_M(TZ))` arm
// (datetime.c) calls DetermineTimeZoneOffset with the session zone.
//
// Hard-won Rule #2 (sibling paths): the TEXT reader, the CSV reader and the
// INSERT literal path are three readers of one type — a table loaded by COPY
// must not differ from the same table loaded by INSERT under the same GUC.
func TestCopyFromZonelessTimetzTakesSessionZone(t *testing.T) {
	cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "timetz"}}}

	cases := []struct {
		zone string
		want int
		why  string
	}{
		{"", 0, "no session zone set: the boot default is UTC"},
		{"UTC", 0, "explicit UTC"},
		{"Asia/Tokyo", 9 * 3600, "PG 18.3 answers 10:00:00+09"},
		{"America/New_York", -4 * 3600, "DST-aware: EDT on today's date"},
	}
	for _, tc := range cases {
		// TEXT reader.
		row, err := DecodeCopyTextRow([]byte("10:00:00"), cols, `\N`, tc.zone)
		if err != nil {
			t.Fatalf("DecodeCopyTextRow(zone=%q): %v", tc.zone, err)
		}
		if got := row[0].TimeTZOffsetSecs(); got != tc.want {
			t.Errorf("COPY TEXT '10:00:00' under TimeZone=%q: offset %d, want %d (%s)",
				tc.zone, got, tc.want, tc.why)
		}

		// CSV reader — the sibling. It shares datumsFromCopyFields, so this
		// pins that the sharing survives rather than re-testing the parser.
		csv := copyToFormat{csv: true, delim: ',', quote: '"', escape: '"'}
		crow, err := DecodeCopyCsvRow([]byte("10:00:00"), cols, csv, tc.zone)
		if err != nil {
			t.Fatalf("DecodeCopyCsvRow(zone=%q): %v", tc.zone, err)
		}
		if got := crow[0].TimeTZOffsetSecs(); got != tc.want {
			t.Errorf("COPY CSV '10:00:00' under TimeZone=%q: offset %d, want %d (%s)",
				tc.zone, got, tc.want, tc.why)
		}
	}
}

// A field that carries its own zone is unaffected by the session zone: the
// session zone is the FALLBACK, not an override. Without this the 48th slice
// could have been "written" by unconditionally stamping the session offset,
// which would corrupt every explicitly-zoned COPY field.
func TestCopyFromExplicitZoneBeatsSessionZone(t *testing.T) {
	cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "timetz"}}}
	for _, in := range []string{"10:00:00+05:30", "10:00:00+00", "10:00:00-08"} {
		want, wantOff, err := parseTimeTZString(in, "")
		if err != nil {
			t.Fatalf("parseTimeTZString(%q): %v", in, err)
		}
		row, err := DecodeCopyTextRow([]byte(in), cols, `\N`, "Asia/Tokyo")
		if err != nil {
			t.Fatalf("DecodeCopyTextRow(%q): %v", in, err)
		}
		if got := row[0].TimeTZOffsetSecs(); got != wantOff {
			t.Errorf("COPY %q under TimeZone='Asia/Tokyo': offset %d, want %d "+
				"(the field's own zone wins)", in, got, wantOff)
		}
		if !row[0].TimeValue().Equal(want) {
			t.Errorf("COPY %q: time %v, want %v", in, row[0].TimeValue(), want)
		}
	}
}
