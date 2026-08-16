package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0119-0006, 40th slice — the `::text` cast of a `timestamp with time zone`.
//
// The 39th slice made the two output paths that KNOW the declared column type
// (dispatch.go's appendTypedCellText, copy_text.go's datumToCopyText) run
// timestamptz_out: convert the stored instant into the session TimeZone and
// print the offset. It could not fix the type-agnostic renderer behind
// CAST-to-text, which sees a bare Datum, and left a ledger row saying so — so
// `('2020-01-01 10:00:00+05:30'::timestamptz)::text` printed no zone and never
// left UTC, disagreeing with goopg's OWN SELECT output of the same value. Under
// a non-UTC TimeZone that is a silent relabel of the instant.
//
// This file pins the fix from both ends: the datum now carries a subtype
// (TimeSubTimestampTZ, set by NewTimestampTZDatum at every producer that knows
// the SQL type), and the renderer dispatches on it.

// tzCtx builds a Context whose GetSetting answers the two GUCs the timestamptz
// output path reads, exactly as a real session's would.
func tzCtx(dateStyle, zone string) *Context {
	return &Context{GetSetting: func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return dateStyle, true
		case "timezone":
			return zone, true
		}
		return "", false
	}}
}

// TestTimestampTZCastToTextMatchesPG18Oracle pins cells captured from a real
// PG 18.3 (initdb'd fresh, `psql -c "SELECT (…::timestamptz)::text"`). These are
// the values a guess gets wrong: the zone spelling is per-DateStyle (ISO jams a
// NUMERIC offset onto the seconds field, the other three print the zone
// ABBREVIATION after a space) and the offset width varies with the zone.
func TestTimestampTZCastToTextMatchesPG18Oracle(t *testing.T) {
	// 2020-01-01 04:30:00 UTC — the instant '2020-01-01 10:00:00+05:30' denotes.
	inst := time.Date(2020, 1, 1, 4, 30, 0, 0, time.UTC)

	cases := []struct {
		name      string
		dateStyle string
		zone      string
		want      string
	}{
		{"ISO UTC", "ISO, MDY", "UTC", "2020-01-01 04:30:00+00"},
		{"ISO half-hour zone", "ISO, MDY", "Asia/Kolkata", "2020-01-01 10:00:00+05:30"},
		{"ISO negative zone", "ISO, MDY", "America/Los_Angeles", "2019-12-31 20:30:00-08"},
		{"Postgres DMY", "Postgres, DMY", "Asia/Kolkata", "Wed 01 Jan 10:00:00 2020 IST"},
		{"German DMY", "German, DMY", "Asia/Kolkata", "01.01.2020 10:00:00 IST"},
		{"SQL DMY", "SQL, DMY", "Asia/Kolkata", "01/01/2020 10:00:00 IST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalCast(NewTimestampTZDatum(inst), "text", 0, tzCtx(c.dateStyle, c.zone))
			if err != nil {
				t.Fatalf("evalCast(text): %v", err)
			}
			if got.StringValue() != c.want {
				t.Errorf("timestamptz::text under DateStyle=%q TimeZone=%q = %q, want %q (PG 18.3)",
					c.dateStyle, c.zone, got.StringValue(), c.want)
			}
		})
	}
}

// TestTimestampTZCastToTextAgreesWithOutputPath is the sibling guard
// (pattern_sibling_paths_must_agree). The CAST-to-text renderer and the
// SELECT/COPY renderer must produce the same bytes for the same timestamptz
// value, so widening one alone fails the build — the exact drift this slice
// removed. config.FormatTimestampTZ is what both dispatch.go's "timestamptz"
// arm and copy_text.go's call, so agreeing with it is agreeing with them.
func TestTimestampTZCastToTextAgreesWithOutputPath(t *testing.T) {
	inst := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	for _, style := range []string{"ISO, MDY", "Postgres, DMY", "German, DMY", "SQL, DMY"} {
		for _, zone := range []string{"UTC", "Asia/Kolkata", "America/Los_Angeles", "Australia/Lord_Howe"} {
			got, err := evalCast(NewTimestampTZDatum(inst), "text", 0, tzCtx(style, zone))
			if err != nil {
				t.Fatalf("evalCast(text): %v", err)
			}
			style, order := misc.ParseDateStyleValue(style)
			want := misc.FormatTimestampTZ(inst, style, order, zone)
			if got.StringValue() != want {
				t.Errorf("cast-to-text %q != output path %q (DateStyle=%q/%q TimeZone=%q)",
					got.StringValue(), want, style, order, zone)
			}
		}
	}
}

// TestPlainTimestampCastToTextStaysZoneless is the negative half: the fix must
// not leak a zone onto `timestamp without time zone`, which is the failure mode
// that made the 39th slice refuse to guess a suffix in the first place. A plain
// timestamp renders the same bytes under every TimeZone.
func TestPlainTimestampCastToTextStaysZoneless(t *testing.T) {
	inst := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	const want = "2020-01-01 10:00:00"
	for _, zone := range []string{"UTC", "Asia/Kolkata", "America/Los_Angeles"} {
		got, err := evalCast(NewTimeDatum(inst), "text", 0, tzCtx("ISO, MDY", zone))
		if err != nil {
			t.Fatalf("evalCast(text): %v", err)
		}
		if got.StringValue() != want {
			t.Errorf("timestamp::text under TimeZone=%q = %q, want %q (no zone, no shift)",
				zone, got.StringValue(), want)
		}
	}
	// A DATE must not move either — TimeSubDate takes its own arm.
	gotD, err := evalCast(NewDateDatum(inst), "text", 0, tzCtx("ISO, MDY", "America/Los_Angeles"))
	if err != nil {
		t.Fatalf("evalCast(text) date: %v", err)
	}
	if gotD.StringValue() != "2020-01-01" {
		t.Errorf("date::text = %q, want %q", gotD.StringValue(), "2020-01-01")
	}
}

// TestTimestampTZProducersTagTheSubtype pins the OTHER half of the fix. The
// renderer above can only work if the datum arrives tagged, so every site that
// knows the value's SQL type is timestamptz must produce it via
// NewTimestampTZDatum. A producer that regresses to NewTimeDatum would render
// zone-less again — silently, since the value is otherwise identical.
func TestTimestampTZProducersTagTheSubtype(t *testing.T) {
	// (1) The typed string literal: TIMESTAMPTZ '…' / '…'::timestamptz.
	lit := &optimizer.TypedStringLit{Type: "timestamptz", Value: "2020-01-01 10:00:00+05:30"}
	d, err := evalTypedStringLit(lit, nil)
	if err != nil {
		t.Fatalf("typed literal: %v", err)
	}
	if !d.IsTimestampTZ() {
		t.Errorf("timestamptz literal produced TimeSub=%d, want TimeSubTimestampTZ", d.TimeSub)
	}
	// Its plain-timestamp sibling must NOT be tagged.
	litTS := &optimizer.TypedStringLit{Type: "timestamp", Value: "2020-01-01 10:00:00+05:30"}
	dTS, err := evalTypedStringLit(litTS, nil)
	if err != nil {
		t.Fatalf("typed literal (timestamp): %v", err)
	}
	if dTS.IsTimestampTZ() {
		t.Error("plain timestamp literal was tagged TimeSubTimestampTZ")
	}

	// (2) The cast from text.
	dc, err := evalCast(NewStringDatum("2020-01-01 10:00:00+05:30"), "timestamptz", 0, tzCtx("ISO, MDY", "UTC"))
	if err != nil {
		t.Fatalf("cast from text: %v", err)
	}
	if !dc.IsTimestampTZ() {
		t.Errorf("text::timestamptz produced TimeSub=%d, want TimeSubTimestampTZ", dc.TimeSub)
	}
}

// TestTimestampCrossCastAppliesSessionZone pins the conversion this slice found
// underneath the rendering bug. goopg returned the datum UNTOUCHED for
// `ts::timestamptz` and `tstz::timestamp`, which is the identity only while
// TimeZone is UTC. Upstream timestamp2timestamptz reads the zone-less wall clock
// as a LOCAL time (so the stored instant moves by the offset) and
// timestamptz2timestamp renders the instant into the session zone and keeps that
// wall clock. Both were silently off by the offset — no error, wrong instant.
//
// Oracle (PG 18.3, TimeZone='Asia/Kolkata'):
//
//	'2020-01-01 10:00:00'::timestamp::timestamptz  -> 2020-01-01 10:00:00+05:30
//	'2020-01-01 04:30:00+00'::timestamptz::timestamp -> 2020-01-01 10:00:00
func TestTimestampCrossCastAppliesSessionZone(t *testing.T) {
	ctx := tzCtx("ISO, MDY", "Asia/Kolkata")

	// timestamp -> timestamptz: the wall clock is 10:00 IST, i.e. 04:30 UTC.
	wall := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	up, err := evalCast(NewTimeDatum(wall), "timestamptz", 0, ctx)
	if err != nil {
		t.Fatalf("timestamp::timestamptz: %v", err)
	}
	if !up.IsTimestampTZ() {
		t.Errorf("timestamp::timestamptz left TimeSub=%d, want TimeSubTimestampTZ", up.TimeSub)
	}
	if want := time.Date(2020, 1, 1, 4, 30, 0, 0, time.UTC); !up.TimeValue().Equal(want) {
		t.Errorf("timestamp::timestamptz stored %v, want %v (wall clock read as local)",
			up.TimeValue().UTC(), want)
	}
	if got, want := renderCastText(t, up, ctx), "2020-01-01 10:00:00+05:30"; got != want {
		t.Errorf("timestamp::timestamptz::text = %q, want %q (PG 18.3)", got, want)
	}

	// timestamptz -> timestamp: the instant rendered in IST, zone discarded.
	inst := time.Date(2020, 1, 1, 4, 30, 0, 0, time.UTC)
	down, err := evalCast(NewTimestampTZDatum(inst), "timestamp", 0, ctx)
	if err != nil {
		t.Fatalf("timestamptz::timestamp: %v", err)
	}
	if down.IsTimestampTZ() {
		t.Error("timestamptz::timestamp kept TimeSubTimestampTZ; target type has no zone")
	}
	if got, want := renderCastText(t, down, ctx), "2020-01-01 10:00:00"; got != want {
		t.Errorf("timestamptz::timestamp::text = %q, want %q (PG 18.3)", got, want)
	}

	// Under UTC the cross-cast is the identity, which is why the defect hid.
	utc := tzCtx("ISO, MDY", "UTC")
	same, err := evalCast(NewTimeDatum(wall), "timestamptz", 0, utc)
	if err != nil {
		t.Fatalf("timestamp::timestamptz (UTC): %v", err)
	}
	if !same.TimeValue().Equal(wall) {
		t.Errorf("timestamp::timestamptz under UTC moved the instant: %v != %v",
			same.TimeValue().UTC(), wall)
	}
}

// TestTimestampCrossCastLeavesInfinityAlone guards the ±infinity sentinels: they
// render from Datum.Int alone and have no real wall clock, so a zone shift would
// corrupt them into an ordinary (wrong) instant.
func TestTimestampCrossCastLeavesInfinityAlone(t *testing.T) {
	ctx := tzCtx("ISO, MDY", "Asia/Kolkata")
	for _, tc := range []struct {
		name string
		d    Datum
		want string
	}{
		{"+infinity", NewTimestampInfinity(true), "infinity"},
		{"-infinity", NewTimestampInfinity(false), "-infinity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalCast(tc.d, "timestamptz", 0, ctx)
			if err != nil {
				t.Fatalf("cast: %v", err)
			}
			if got.Int != tc.d.Int {
				t.Fatalf("sentinel carrier changed: %d != %d", got.Int, tc.d.Int)
			}
			if s := renderCastText(t, got, ctx); s != tc.want {
				t.Errorf("rendered %q, want %q", s, tc.want)
			}
		})
	}
}

func renderCastText(t *testing.T, d Datum, ctx *Context) string {
	t.Helper()
	s, err := evalCast(d, "text", 0, ctx)
	if err != nil {
		t.Fatalf("evalCast(text): %v", err)
	}
	return s.StringValue()
}
