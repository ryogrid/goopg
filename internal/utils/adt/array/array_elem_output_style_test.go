package array

import "testing"

// Array elements are NOT rendered by array_out itself: upstream
// (postgres/src/backend/utils/adt/arrayfuncs.c, array_out) looks up the ELEMENT
// type's output function and calls it per element, so `timestamptz_out` inside
// an array honours the session TimeZone and DateStyle exactly as it does for a
// scalar column, and `date_out`/`timestamp_out` honour DateStyle.
//
// goopg used to render every date-time array element in ISO/UTC unconditionally
// (pgdatetime.FormatTimestampTZUTC and friends), which was the 2026-08-12
// deferral-ledger row: a session in any other zone read a correct instant back
// under the wrong offset, silently. These cells are captured live from
// PostgreSQL 18.3 — the integer images come from
// `(extract(epoch from …)*1000000)::bigint - 946684800000000` and
// `<date> - '2000-01-01'::date`, i.e. PG's own stored forms — so a divergence
// here is a divergence from the oracle, not from a re-derivation of it.
// M0119-0006.

const (
	// 2020-06-15 10:00:00+00 — northern summer, so the US zones are on DST.
	ttzSummer = int64(645530400000000)
	// 2020-01-15 10:00:00+00 — the same wall clock in winter (standard time).
	ttzWinter = int64(632397600000000)
	// 0001-02-28 10:00:00+00 BC — before every zone's first tzdata transition,
	// so the offset is Local Mean Time and has a SECONDS component.
	ttzBC = int64(-63108856800000000)

	tsPlain = int64(645530400000000)  // 2020-06-15 10:00:00
	tsBC    = int64(-63108856800000000) // 0001-02-28 10:00:00 BC
	tsFrac  = int64(636381296100000)  // 2020-03-01 12:34:56.1

	dPlain = int32(7471)    // 2020-06-15
	dBC    = int32(-730427) // 0001-02-28 BC
)

func TestFormatTimestampTZElemAgainstPG18Oracle(t *testing.T) {
	cases := []struct {
		name              string
		st                OutputStyle
		micros            int64
		want              string
	}{
		// The regression this slice exists for: same instant, three zones.
		{"iso utc summer", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, ttzSummer, "2020-06-15 10:00:00+00"},
		{"iso kolkata summer", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"}, ttzSummer, "2020-06-15 15:30:00+05:30"},
		{"iso kathmandu summer", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kathmandu"}, ttzSummer, "2020-06-15 15:45:00+05:45"},
		// DST moves both the clock and the offset width.
		{"iso kolkata winter", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"}, ttzWinter, "2020-01-15 15:30:00+05:30"},
		// An HH:MM:SS offset — Local Mean Time, reachable only pre-1906.
		{"iso kolkata BC", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"}, ttzBC, "0001-02-28 15:53:28+05:53:28 BC"},
		{"iso kathmandu BC", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kathmandu"}, ttzBC, "0001-02-28 15:41:16+05:41:16 BC"},
		// The abbreviation styles print the tzdata abbreviation, not the offset.
		{"german utc", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, ttzSummer, "15.06.2020 10:00:00 UTC"},
		{"sql dmy LA summer", OutputStyle{Style: "SQL", Order: "DMY", Zone: "America/Los_Angeles"}, ttzSummer, "15/06/2020 03:00:00 PDT"},
		{"sql dmy LA winter", OutputStyle{Style: "SQL", Order: "DMY", Zone: "America/Los_Angeles"}, ttzWinter, "15/01/2020 02:00:00 PST"},
		{"postgres mdy LA summer", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "America/Los_Angeles"}, ttzSummer, "Mon Jun 15 03:00:00 2020 PDT"},
		{"postgres mdy LA winter", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "America/Los_Angeles"}, ttzWinter, "Wed Jan 15 02:00:00 2020 PST"},
		// The era marker trails the ZONE, not the seconds.
		{"postgres mdy LA BC", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "America/Los_Angeles"}, ttzBC, "Mon Feb 28 02:07:02 0001 LMT BC"},
		// Sentinels are style- and zone-independent (EncodeSpecialTimestamp).
		{"infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "Asia/Kolkata"}, maxInt64, "infinity"},
		{"-infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "Asia/Kolkata"}, minInt64, "-infinity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatTimestampTZElem(c.micros, c.st); got != c.want {
				t.Errorf("FormatTimestampTZElem = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatTimestampElemAgainstPG18Oracle(t *testing.T) {
	// A zone-less timestamp must NOT move when TimeZone changes — the twin
	// assertion to the one above. Landing the zone conversion on this type
	// would relabel every stored value.
	cases := []struct {
		name   string
		st     OutputStyle
		micros int64
		want   string
	}{
		{"iso", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, tsPlain, "2020-06-15 10:00:00"},
		{"iso under a non-UTC session zone", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"}, tsPlain, "2020-06-15 10:00:00"},
		{"german", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, tsPlain, "15.06.2020 10:00:00"},
		{"postgres mdy", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "America/Los_Angeles"}, tsPlain, "Mon Jun 15 10:00:00 2020"},
		{"iso BC", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, tsBC, "0001-02-28 10:00:00 BC"},
		{"german BC", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, tsBC, "28.02.0001 10:00:00 BC"},
		{"postgres BC keeps the true weekday", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "UTC"}, tsBC, "Mon Feb 28 10:00:00 0001 BC"},
		// Fractional seconds carry no trailing zeros (AppendSeconds).
		{"fractional", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, tsFrac, "2020-03-01 12:34:56.1"},
		{"fractional postgres", OutputStyle{Style: "Postgres", Order: "MDY", Zone: "UTC"}, tsFrac, "Sun Mar 01 12:34:56.1 2020"},
		{"infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, maxInt64, "infinity"},
		{"-infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, minInt64, "-infinity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatTimestampElem(c.micros, c.st); got != c.want {
				t.Errorf("FormatTimestampElem = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatDateElemAgainstPG18Oracle(t *testing.T) {
	cases := []struct {
		name string
		st   OutputStyle
		days int32
		want string
	}{
		{"iso", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, dPlain, "2020-06-15"},
		{"iso ignores the session zone", OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"}, dPlain, "2020-06-15"},
		{"german", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, dPlain, "15.06.2020"},
		{"sql dmy", OutputStyle{Style: "SQL", Order: "DMY", Zone: "UTC"}, dPlain, "15/06/2020"},
		{"iso BC", OutputStyle{Style: "ISO", Order: "MDY", Zone: "UTC"}, dBC, "0001-02-28 BC"},
		{"german BC", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, dBC, "28.02.0001 BC"},
		{"infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, maxInt32, "infinity"},
		{"-infinity", OutputStyle{Style: "German", Order: "DMY", Zone: "UTC"}, minInt32, "-infinity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatDateElem(c.days, c.st); got != c.want {
				t.Errorf("FormatDateElem = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDefaultOutputStyleIsThePreSliceBehaviour pins the compatibility contract
// the ~70 session-less decode sites rely on: passing DefaultOutputStyle must
// reproduce exactly what this package rendered before OutputStyle existed
// (ISO/MDY, UTC). If this drifts, every catalog reload and VACUUM rescan
// silently changes what it reads.
func TestDefaultOutputStyleIsThePreSliceBehaviour(t *testing.T) {
	st := DefaultOutputStyle()
	if got, want := FormatTimestampTZElem(ttzSummer, st), "2020-06-15 10:00:00+00"; got != want {
		t.Errorf("timestamptz default = %q, want %q", got, want)
	}
	if got, want := FormatTimestampElem(tsPlain, st), "2020-06-15 10:00:00"; got != want {
		t.Errorf("timestamp default = %q, want %q", got, want)
	}
	if got, want := FormatDateElem(dPlain, st), "2020-06-15"; got != want {
		t.Errorf("date default = %q, want %q", got, want)
	}
}

const (
	maxInt64 = int64(^uint64(0) >> 1)
	minInt64 = -maxInt64 - 1
	maxInt32 = int32(^uint32(0) >> 1)
	minInt32 = -maxInt32 - 1
)
