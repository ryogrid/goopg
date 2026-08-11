package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// The ISO 8601 spellings of a timestamp — a `T` between the date and the time,
// and a `Z` for the UTC zone — are ordinary PostgreSQL input. PG never matches
// a layout: ParseDateTime() (postgres/src/backend/utils/adt/datetime.c) splits
// the string into fields, treating the `T` as an ordinary field break in either
// case, and DecodeDateTime() resolves the zone token against datetbl, where `Z`
// is a DTZ entry for UTC found case-insensitively and whether or not whitespace
// precedes it. So all of `2020-01-01T10:00:00`, `2020-01-01t10:00:00`,
// `2020-01-01 10:00:00Z`, `…z` and `… Z` are the same instant.
//
// goopg rejected every one of them with 22007. Its two timestamp layout tables
// — parseCopyTimestamp (COPY TEXT, the codec's value encoder, the array element
// encoder, the cross-kind comparison coercion) and the typed-literal list in
// evalTypedStringLit — between them covered only the space separator with an
// optional `-07` offset, plus Go's RFC3339 constants, which demand BOTH the `T`
// and a zone. `2020-01-01T10:00:00`, plain ISO 8601 with no zone, matched
// neither: not the space layouts (wrong separator) and not RFC3339 (no zone).
//
// This is the third time the two tables have been found disagreeing (unpadded
// fields, M0125-0007; the seconds-less `HH:MM` form, the 28th M0119-0006
// slice), so the fix merges them into one shared pgTimestampLayouts and folds
// the case/spacing variants upstream in pgdatetime.NormalizeInput. Every `want`
// below is what PG 18.3 answers for the same input (local 18.3 cluster, socket
// /tmp port 5599, `set timezone='UTC'`).
func TestParseCopyTimestampISO8601SeparatorAndZulu(t *testing.T) {
	base := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"2020-01-01T10:00:00", base},   // plain ISO 8601, no zone — the repro
		{"2020-01-01t10:00:00", base},   // PG's field splitter is case-blind
		{"2020-01-01T10:00", base},      // + seconds-less (28th slice's rule)
		{"2020-01-01T10:00:00.5", base.Add(500 * time.Millisecond)},
		{"2020-01-01 10:00:00Z", base},  // space separator, Zulu zone
		{"2020-01-01T10:00:00z", base},  // lowercase Zulu
		{"2020-01-01T10:00:00 Z", base}, // zone as a separate field
		{"2020-01-01T10:00Z", base},
		{"2020-01-01T10:00:00.25Z", base.Add(250 * time.Millisecond)},
		// Numeric offsets, in each of the three widths PG's DecodeTimezone
		// accepts. The `T` forms were rejected outright before this slice; the
		// two wider offsets were rejected on BOTH separators, since the only
		// offset layout was `-07`.
		{"2020-01-01T10:00:00+05", base.Add(-5 * time.Hour)},
		{"2020-01-01T10:00:00+0530", base.Add(-5*time.Hour - 30*time.Minute)},
		{"2020-01-01T10:00:00+05:30", base.Add(-5*time.Hour - 30*time.Minute)},
		{"2020-01-01 10:00:00-0430", base.Add(4*time.Hour + 30*time.Minute)},
		{"2020-01-01 10:00:00-04:30", base.Add(4*time.Hour + 30*time.Minute)},
		// Regressions: the spellings that already worked must keep working.
		{"2020-01-01 10:00:00", base},
		{"2020-01-01 10:00", base},
		{"2020-01-01 10:00:00.123456", base.Add(123456 * time.Microsecond)},
		{"2020-01-01 10:00:00+05", base.Add(-5 * time.Hour)},
		{"2020-01-01T10:00:00.25+05:30", base.Add(-5*time.Hour - 30*time.Minute + 250*time.Millisecond)},
	} {
		got, err := parseCopyTimestamp(c.in)
		if err != nil {
			t.Errorf("parseCopyTimestamp(%q): %v — PG reads this as %v", c.in, err, c.want)
			continue
		}
		if !got.UTC().Equal(c.want) {
			t.Errorf("parseCopyTimestamp(%q) = %v, want %v", c.in, got.UTC(), c.want)
		}
	}
}

// TestTimestampLiteralAndCopyPathsAgree is the sibling-path guard that the
// shared layout table exists to make cheap (Hard-won Rule #2). A typed literal
// and the same text arriving through COPY / the value encoder must decode to
// the same instant, or an INSERT can raise 22007 on text a SELECT accepts —
// exactly the failure mode of the previous two slices. It asserts AGREEMENT
// rather than a fixed value on purpose: it keeps holding if a later slice
// widens acceptance, and fails the moment only one table is widened.
func TestTimestampLiteralAndCopyPathsAgree(t *testing.T) {
	forms := []string{
		"2020-01-01T10:00:00", "2020-01-01t10:00:00", "2020-01-01T10:00",
		"2020-01-01 10:00:00Z", "2020-01-01T10:00:00z", "2020-01-01T10:00:00 Z",
		"2020-01-01T10:00:00+05", "2020-01-01T10:00:00+05:30",
		"2020-01-01 10:00:00-0430", "2020-01-01 10:00", "2020-01-01 10:00:00",
		"2020-01-01", "2020-1-1 3:4:5",
		// Still rejected by both (each a ledger row, none of them invented):
		// a lone date carrying a zone, a zone ABBREVIATION, a dangling
		// separator. Agreement is the assertion, so these belong here too.
		"2020-01-01Z", "2020-01-01 10:00:00 NZ", "2020-01-01T",
	}
	for _, typ := range []string{"timestamp", "timestamptz"} {
		for _, in := range forms {
			// Both paths are read under the SAME target type: since M0119-0006
			// a decoded zone is kept by timestamptz and discarded by timestamp,
			// so comparing the two paths under different rules would assert a
			// difference the types are supposed to have, not a table drift.
			copyTS, copyErr := parseCopyTimestampZone(in, tsZoneModeForType(typ))
			litD, litErr := evalTypedStringLit(&planner.TypedStringLit{Type: typ, Value: in})
			if (copyErr == nil) != (litErr == nil) {
				t.Errorf("%s %q: COPY path err=%v but literal path err=%v — the two layout tables have drifted apart again",
					typ, in, copyErr, litErr)
				continue
			}
			if copyErr != nil {
				continue
			}
			if got := litD.TimeValue().UTC(); !got.Equal(copyTS.UTC()) {
				t.Errorf("%s %q: literal = %v, COPY = %v", typ, in, got, copyTS.UTC())
			}
		}
	}
}

// TestArrayElementISO8601Timestamp is the array half: encodeArrayValuePG runs
// each element through the same scalar input function, so a `T`/`Z` element was
// rejected outright. The round-trip also pins the SPELLING it reads back as —
// PG stores an instant, not the text, and prints it in the space-separated
// canonical form.
func TestArrayElementISO8601Timestamp(t *testing.T) {
	typ := catalog.Type{Name: "timestamp", IsArray: true}
	for _, c := range []struct{ lit, want string }{
		{"{2020-01-01T10:00:00}", `{"2020-01-01 10:00:00"}`},
		{"{2020-01-01T10:00}", `{"2020-01-01 10:00:00"}`},
	} {
		blob, err := encodeArrayValuePG(typ, NewStringDatum(c.lit))
		if err != nil {
			t.Errorf("timestamp[] encode %q: %v", c.lit, err)
			continue
		}
		back, _, err := decodeArrayValuePG(typ, blob)
		if err != nil {
			t.Errorf("timestamp[] decode %q: %v", c.lit, err)
			continue
		}
		if got := back.StringValue(); got != c.want {
			t.Errorf("timestamp[] %q round-tripped as %q, want %q", c.lit, got, c.want)
		}
	}
}

// TestISO8601NormalisationStopsAtTheUnhandledForms is the mutation guard. The
// separator/zone folding must widen acceptance to exactly the forms above and
// no further; everything here is either still-unimplemented PG input (ledger
// rows) or genuinely invalid, and must keep raising rather than decode to some
// invented instant.
//
//	'2020-01-01Z'            PG: 2020-01-01 00:00:00 — a zone on a bare DATE
//	'2020-01-01 10:00:00 NZ' PG: accepted — zone ABBREVIATIONS (datetbl lookup)
//	'2020-01-01T'            PG: 22007 — a dangling separator, invalid forever
//	'2020-01-01T10:00:00+05:3'  PG: 22007 — a truncated offset
func TestISO8601NormalisationStopsAtTheUnhandledForms(t *testing.T) {
	for _, in := range []string{
		"2020-01-01Z", "2020-01-01 10:00:00 NZ", "2020-01-01T",
		"2020-01-01T10:00:00+05:3", "2020-01-01Tgarbage",
		// '2020-01-01T10:00.5' left this list when the time-field decoder
		// landed: PG reads it as 2020-01-01 00:10:00.5 (MINUTE TO SECOND), and
		// TestParsePGTimestampTextPGFieldRoles now pins that reading.
	} {
		if got, err := parseCopyTimestamp(in); err == nil {
			t.Errorf("parseCopyTimestamp(%q) = %v, want an error — the separator/zone folding must not guess", in, got)
		}
	}
}
