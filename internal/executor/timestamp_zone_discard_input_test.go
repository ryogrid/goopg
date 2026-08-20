package executor

import (
	"testing"
	"time"
)

// M0119-0006 — `timestamp` and `date` DECODE a time zone field and then THROW IT
// AWAY; only `timestamptz` keeps it.
//
// Upstream runs all three input functions through the same DecodeDateTime()
// (postgres/src/backend/utils/adt/datetime.c), which fills `tzp` whenever the
// text carries an offset. The difference is the call after it: timestamptz_in
// passes `&tz` to tm2timestamp() so the offset moves the wall clock onto the UTC
// line, timestamp_in passes NULL, and date_in (date.c) never consults the zone
// at all.
//
// goopg had a single shared layout table with Go `Z07*` elements and converted
// every match with .UTC(), i.e. the timestamptz rule applied to all three. The
// answers were silently wrong rather than errors:
//
//	'2020-01-01 10:00:00+05:30'::timestamp → 2020-01-01 04:30:00 (PG: 10:00:00)
//	'2020-01-02 02:00:00+05:30'::date      → 2020-01-01           (PG: 2020-01-02)
//
// — the date case being off by a WHOLE DAY whenever the offset crosses midnight.
//
// Every `want` below was captured from a PostgreSQL 18.3 oracle cluster.
func TestTimestampInputDiscardsZoneButTimestamptzKeepsIt(t *testing.T) {
	t.Parallel()

	// A wall clock as written, read as UTC — what tsDiscardZone must produce.
	wall := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}

	cases := []struct {
		in   string
		zone tsZoneMode
		want time.Time
	}{
		// timestamp WITHOUT time zone: the offset is parsed, then ignored.
		{"2020-01-01 10:00:00+05:30", tsDiscardZone, wall(2020, 1, 1, 10, 0, 0)},
		{"2020-01-01 10:00:00-08", tsDiscardZone, wall(2020, 1, 1, 10, 0, 0)},
		{"2020-01-01 10:00:00Z", tsDiscardZone, wall(2020, 1, 1, 10, 0, 0)},
		{"2020-01-01T10:00:00+05:30", tsDiscardZone, wall(2020, 1, 1, 10, 0, 0)},
		{"2020-01-02 02:00:00+05:30", tsDiscardZone, wall(2020, 1, 2, 2, 0, 0)},
		// A zone-less `timestamp` spelling is unaffected by the tsZoneMode rule
		// (there is no zone to keep or discard either way).
		{"2020-01-01 10:00:00", tsDiscardZone, wall(2020, 1, 1, 10, 0, 0)},
		// M0134-0026: a zone-less `timestamptz` spelling is NOT simply "the
		// same as tsDiscardZone" in general — DecodeDateTime reads it as local
		// wall-clock time in the SESSION TimeZone GUC and converts to UTC
		// (datetime.c:1573-1583). This assertion only continues to hold
		// because parsePGTimestampTextZone (unlike the production callers) is
		// the UTC-session convenience wrapper: it always resolves the
		// zone-less branch against sessionZone="" (UTC), where "read as local
		// time and convert to UTC" is the identity. A real, non-UTC session
		// answers a DIFFERENT UTC instant — see
		// TestTimestamptzLiteralAppliesSessionTimeZone
		// (timestamptz_literal_session_zone_test.go), which pins that case
		// against a live PG 18.3 oracle and is the guard for the actual fix.
		{"2020-01-01 10:00:00", tsApplyZone, wall(2020, 1, 1, 10, 0, 0)},
		// timestamptz: the offset shifts the value onto the UTC line, which is
		// what the KindTime carrier stores.
		{"2020-01-01 10:00:00+05:30", tsApplyZone, wall(2020, 1, 1, 4, 30, 0)},
		{"2020-01-01 10:00:00-08", tsApplyZone, wall(2020, 1, 1, 18, 0, 0)},
	}
	for _, c := range cases {
		got, err := parsePGTimestampTextZone(c.in, c.zone)
		if err != nil {
			t.Errorf("parsePGTimestampTextZone(%q, %v): %v", c.in, c.zone, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parsePGTimestampTextZone(%q, %v) = %v, want %v (PG 18.3)",
				c.in, c.zone, got.UTC(), c.want)
		}
	}
}

// tsZoneModeForType is what every call site uses to pick the rule, so the type
// name → rule mapping is worth pinning: only the with-time-zone spellings keep
// the offset.
func TestTSZoneModeForType(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		typeName string
		want     tsZoneMode
	}{
		{"timestamptz", tsApplyZone},
		{"TIMESTAMPTZ", tsApplyZone},
		{"timestamp with time zone", tsApplyZone},
		{"timestamp", tsDiscardZone},
		{"TIMESTAMP", tsDiscardZone},
		{"date", tsDiscardZone},
		{"", tsDiscardZone},
	} {
		if got := tsZoneModeForType(c.typeName); got != c.want {
			t.Errorf("tsZoneModeForType(%q) = %v, want %v", c.typeName, got, c.want)
		}
	}
}
