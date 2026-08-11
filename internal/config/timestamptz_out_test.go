package config

import (
	"testing"
	"time"
)

// TestFormatTimestampTZAgainstPG18Oracle pins FormatTimestampTZ against cells
// captured live from a throwaway PostgreSQL 18.3 (`<value>::timestamptz::text`
// under an explicit `SET TimeZone` / `SET DateStyle`), because this function's
// whole job is to reproduce timestamptz_out and the interesting parts of it are
// exactly the parts one would guess wrong:
//
//   - ISO prints a NUMERIC offset, never an abbreviation; the other three
//     styles print the ABBREVIATION and never a number (when one exists).
//   - the offset width varies with the zone: "+00", "-07", "+05:30", "+05:45",
//     and — for pre-standardisation local mean time — "+05:53:28".
//   - " BC" trails the ZONE, not the seconds field.
//   - the Postgres style's weekday is the weekday of the CONVERTED local time.
//
// The stored instants below are all UTC, matching how goopg carries a KindTime.
func TestFormatTimestampTZAgainstPG18Oracle(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
	}
	cases := []struct {
		name         string
		t            time.Time
		style, order string
		zone         string
		want         string
	}{
		// SET TimeZone='UTC'; SET DateStyle='ISO, MDY'
		{"iso-utc", utc(2020, 1, 1, 4, 30, 0, 0), "ISO", "MDY", "UTC", "2020-01-01 04:30:00+00"},
		{"iso-utc-frac", utc(2020, 1, 1, 10, 0, 0, 500000000), "ISO", "MDY", "UTC", "2020-01-01 10:00:00.5+00"},
		// The empty zone is the boot default, UTC — the same cell.
		{"iso-empty-zone", utc(2020, 1, 1, 4, 30, 0, 0), "ISO", "MDY", "", "2020-01-01 04:30:00+00"},
		// A zone Go's tzdata does not know falls back to UTC rather than
		// erroring; see the deferral ledger (POSIX zone spellings).
		{"iso-unknown-zone", utc(2020, 1, 1, 4, 30, 0, 0), "ISO", "MDY", "Mars/Olympus", "2020-01-01 04:30:00+00"},

		// SET TimeZone='Asia/Kolkata' — a half-hour offset.
		{"iso-kolkata", utc(2020, 1, 1, 10, 0, 0, 0), "ISO", "MDY", "Asia/Kolkata", "2020-01-01 15:30:00+05:30"},
		// SET TimeZone='Asia/Kathmandu' — a quarter-hour offset.
		{"iso-kathmandu", utc(2020, 6, 15, 10, 0, 0, 0), "ISO", "MDY", "Asia/Kathmandu", "2020-06-15 15:45:00+05:45"},
		// SET TimeZone='America/Los_Angeles' — DST moves both the clock and
		// the offset, so the same wall time renders differently by season.
		{"iso-la-winter", utc(2020, 1, 1, 10, 0, 0, 0), "ISO", "MDY", "America/Los_Angeles", "2020-01-01 02:00:00-08"},
		{"iso-la-summer", utc(2020, 6, 15, 10, 0, 0, 0), "ISO", "MDY", "America/Los_Angeles", "2020-06-15 03:00:00-07"},
		// SET TimeZone='Africa/Monrovia' — LMT with a seconds component, the
		// only arm of EncodeTimezone that prints HH:MM:SS.
		{"iso-monrovia-lmt", utc(1970, 1, 1, 10, 0, 0, 0), "ISO", "MDY", "Africa/Monrovia", "1970-01-01 09:15:30-00:44:30"},

		// " BC" trails the zone. 1 BC is astronomical year 0.
		{"iso-bc-utc", utc(0, 2, 28, 10, 0, 0, 0), "ISO", "MDY", "UTC", "0001-02-28 10:00:00+00 BC"},
		// The oracle cell was `'0001-02-28 10:00:00 BC'::timestamptz` under
		// TimeZone='Asia/Kolkata', which PG READS as 10:00 local and prints
		// back as 10:00 local; the stored UTC instant is therefore 04:06:32,
		// and the same instant is what goopg holds. Feeding the 10:00 UTC
		// instant instead moves the local clock to 15:53:28 — the offset,
		// which is the part being pinned here, is unchanged.
		{"iso-bc-kolkata", utc(0, 2, 28, 10, 0, 0, 0), "ISO", "MDY", "Asia/Kolkata", "0001-02-28 15:53:28+05:53:28 BC"},
		{"iso-bc-kolkata-oracle-instant", utc(0, 2, 28, 4, 6, 32, 0), "ISO", "MDY", "Asia/Kolkata", "0001-02-28 10:00:00+05:53:28 BC"},

		// The abbreviation styles.
		{"sql-mdy-la", utc(2020, 6, 15, 10, 0, 0, 0), "SQL", "MDY", "America/Los_Angeles", "06/15/2020 03:00:00 PDT"},
		{"sql-dmy-kolkata", utc(2020, 6, 15, 10, 0, 0, 0), "SQL", "DMY", "Asia/Kolkata", "15/06/2020 15:30:00 IST"},
		{"postgres-mdy-la", utc(2020, 6, 15, 10, 0, 0, 0), "Postgres", "MDY", "America/Los_Angeles", "Mon Jun 15 03:00:00 2020 PDT"},
		{"postgres-dmy-kolkata", utc(2020, 6, 15, 10, 0, 0, 0), "Postgres", "DMY", "Asia/Kolkata", "Mon 15 Jun 15:30:00 2020 IST"},
		{"german-la", utc(2020, 6, 15, 10, 0, 0, 0), "German", "DMY", "America/Los_Angeles", "15.06.2020 03:00:00 PDT"},
		{"german-kolkata", utc(2020, 6, 15, 10, 0, 0, 0), "German", "DMY", "Asia/Kolkata", "15.06.2020 15:30:00 IST"},
		// A zone whose tzdata abbreviation IS numeric prints that string
		// verbatim in the abbreviation styles — not EncodeTimezone's "+05:45".
		{"postgres-kathmandu-numeric-abbrev", utc(2020, 6, 15, 10, 0, 0, 0), "Postgres", "MDY", "Asia/Kathmandu", "Mon Jun 15 15:45:00 2020 +0545"},
		{"postgres-monrovia-lmt-abbrev", utc(1970, 1, 1, 10, 0, 0, 0), "Postgres", "MDY", "Africa/Monrovia", "Thu Jan 01 09:15:30 1970 MMT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatTimestampTZ(tc.t, tc.style, tc.order, tc.zone); got != tc.want {
				t.Errorf("FormatTimestampTZ(%s, %s, %s, %q) = %q, want %q",
					tc.t.Format(time.RFC3339Nano), tc.style, tc.order, tc.zone, got, tc.want)
			}
		})
	}
}

// TestFormatTimestampStillDropsTheZone pins the OTHER half of the split: a
// `timestamp without time zone` must keep rendering the stored instant with no
// conversion and no zone, whatever the session TimeZone is. Upstream calls the
// same EncodeDateTime with print_tz=false (timestamp_out), so the two output
// functions differ only in that flag — and a refactor that accidentally routed
// plain timestamps through the tz path would relabel every one of them.
func TestFormatTimestampStillDropsTheZone(t *testing.T) {
	when := time.Date(2020, 6, 15, 10, 0, 0, 0, time.UTC)
	if got := FormatTimestamp(when, "ISO", "MDY"); got != "2020-06-15 10:00:00" {
		t.Errorf("FormatTimestamp = %q, want %q", got, "2020-06-15 10:00:00")
	}
	if got := FormatTimestamp(time.Date(0, 2, 28, 10, 0, 0, 0, time.UTC), "ISO", "MDY"); got != "0001-02-28 10:00:00 BC" {
		t.Errorf("FormatTimestamp(BC) = %q, want %q", got, "0001-02-28 10:00:00 BC")
	}
}

// TestEncodeTimezoneWidths exercises EncodeTimezone's three width arms and its
// sign rule directly, including the zero offset whose sign is '+' (upstream:
// `tz <= 0 ? '+' : '-'`, and tz is the NEGATION of the east-of-UTC offset, so
// zero takes the '+' branch).
func TestEncodeTimezoneWidths(t *testing.T) {
	cases := []struct {
		offsetEast int
		want       string
	}{
		{0, "+00"},
		{3600, "+01"},
		{-28800, "-08"},
		{19800, "+05:30"},
		{20700, "+05:45"},
		{-2670, "-00:44:30"},
		{-1, "-00:00:01"},
	}
	for _, tc := range cases {
		if got := encodeTimezone(tc.offsetEast); got != tc.want {
			t.Errorf("encodeTimezone(%d) = %q, want %q", tc.offsetEast, got, tc.want)
		}
	}
}
