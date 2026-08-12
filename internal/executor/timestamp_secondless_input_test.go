package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// A time-of-day with NO seconds field is ordinary PostgreSQL input: DecodeTime
// (postgres/src/backend/utils/adt/datetime.c) requires hour and minute and only
// reads a seconds field `if (*cp == ':')`, leaving tm_sec = 0 otherwise. So
// `10:00` is `10:00:00` and `2020-01-01 10:00` is a plain timestamp.
//
// goopg disagreed with itself about that: the typed-literal path in expr.go
// lists a "2006-01-02 15:04" layout, but parseCopyTimestamp — which is what the
// COPY TEXT reader, the codec's value encoder and the array-element encoder all
// funnel through — did not, so `INSERT INTO t(ts) VALUES ('2020-01-01 10:00')`
// raised 22007 while `timestamp '2020-01-01 10:00'` parsed. The array-element
// slice (M0119-0006, deferral ledger 2026-08-12) made arrays inherit the same
// gap when it stopped storing element text verbatim.
//
// The fix supplies the absent field once, in pgdatetime.NormalizeInput, so every
// call site agrees. These tests pin the executor-visible half of that contract;
// TestNormalizeInputPGAcceptedForms pins the rewrite itself. Every `want` below
// is what PG 18.3 returns for the same input (local 18.3 cluster, socket /tmp
// port 5599).
func TestParseCopyTimestampSecondlessTime(t *testing.T) {
	want := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, lit := range []string{
		"2020-01-01 10:00",    // the repro
		"2020-1-1 10:00",      // + unpadded date fields
		"2020-01-01 10:00:",   // empty trailing seconds field (PG accepts)
		"2020-01-01 10:00:00", // seconds present: unchanged behaviour
		" 2020-01-01 10:00 ",  // PG trims before decoding
		// NOT here, and pre-existing (they fail with seconds too, so this
		// slice neither fixed nor broke them): the 'T' separator without a
		// zone ("2020-01-01T10:00:00") and a zone-suffixed space-separated
		// timestamp ("2020-01-01 10:00:00Z"), both accepted by PG. Ledger row.
	} {
		got, err := parseCopyTimestamp(lit)
		if err != nil {
			t.Errorf("parseCopyTimestamp(%q): %v — PG reads this as %v", lit, err, want)
			continue
		}
		if !got.UTC().Equal(want) {
			t.Errorf("parseCopyTimestamp(%q) = %v, want %v", lit, got.UTC(), want)
		}
	}
}

// TestParseTimeStringSecondlessTime is the bare-time-of-day half: `time '10:00'`
// and `timetz '10:00+05'` are both 10:00:00 to PG.
func TestParseTimeStringSecondlessTime(t *testing.T) {
	for _, c := range []struct {
		in   string
		h, m int
	}{
		{"10:00", 10, 0},
		{"1:2", 1, 2},
		{"10:00:", 10, 0},
		{"10:00 PM", 22, 0},
		{"22:00", 22, 0},
	} {
		got, err := parseTimeString(c.in)
		if err != nil {
			t.Errorf("parseTimeString(%q): %v", c.in, err)
			continue
		}
		if got.Hour() != c.h || got.Minute() != c.m || got.Second() != 0 {
			t.Errorf("parseTimeString(%q) = %02d:%02d:%02d, want %02d:%02d:00",
				c.in, got.Hour(), got.Minute(), got.Second(), c.h, c.m)
		}
	}
	tz, off, err := parseTimeTZString("10:00+05", "")
	if err != nil {
		t.Fatalf("parseTimeTZString(%q): %v", "10:00+05", err)
	}
	if tz.Hour() != 10 || tz.Minute() != 0 || tz.Second() != 0 || off != 5*3600 {
		t.Errorf("parseTimeTZString(\"10:00+05\") = %02d:%02d:%02d offset %ds, want 10:00:00 offset 18000s",
			tz.Hour(), tz.Minute(), tz.Second(), off)
	}
}

// TestArrayElementSecondlessTimestamp is the ledger row's own repro: the array
// element encoder delegates to the same scalar input function, so a secondless
// element used to be rejected outright. The round-trip also pins the SPELLING
// the element reads back as — PG prints the stored value with seconds.
func TestArrayElementSecondlessTimestamp(t *testing.T) {
	for _, c := range []struct{ typ, lit, want string }{
		{"timestamp", "{2020-01-01 10:00}", "{\"2020-01-01 10:00:00\"}"},
		{"time", "{10:00}", "{10:00:00}"},
	} {
		typ := catalog.Type{Name: c.typ, IsArray: true}
		blob, err := encodeArrayValuePG(typ, NewStringDatum(c.lit))
		if err != nil {
			t.Errorf("%s[] encode %q: %v", c.typ, c.lit, err)
			continue
		}
		back, _, err := decodeArrayValuePG(typ, blob)
		if err != nil {
			t.Errorf("%s[] decode: %v", c.typ, err)
			continue
		}
		if got := back.StringValue(); got != c.want {
			t.Errorf("%s[] %q round-tripped as %q, want %q", c.typ, c.lit, got, c.want)
		}
	}
}

// TestSecondlessNormalisationStopsAtTheAmbiguousForms is the mutation guard: the
// rewrite must supply an ABSENT seconds field and nothing else. Each input here
// is one PG also has an opinion about, and that opinion is NOT "hh:mm plus
// whatever followed" — so goopg must keep raising rather than invent a time.
//
//	'2020-01-01 10'    PG: 22007 — a lone hour is not a time-of-day
//
// It must stay rejected forever. The two forms this guard used to also list —
// '10:00.5' (PG: 00:10:00.5, the fractional field is MM:SS.f) and '10::00' (PG:
// 10:00:00 via an empty MINUTE field) — were the ledger rows asking for a real
// DecodeTime field walk, and a later M0119-0006 slice landed exactly that
// (pgdatetime.ParseTimeOfDay). They are pinned to PG's readings in
// TestParseTimeStringPGFieldRoles now, not to a rejection.
func TestSecondlessNormalisationStopsAtTheAmbiguousForms(t *testing.T) {
	for _, lit := range []string{"2020-01-01 10"} {
		if got, err := parseCopyTimestamp(lit); err == nil {
			t.Errorf("parseCopyTimestamp(%q) = %v, want an error — the seconds default must not guess", lit, got)
		}
	}
	for _, lit := range []string{"10:00:ab"} {
		if got, err := parseTimeString(lit); err == nil {
			t.Errorf("parseTimeString(%q) = %v, want an error — the seconds default must not guess", lit, got)
		}
	}
}
