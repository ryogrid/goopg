package executor

import (
	"testing"
	"time"
)

// M0119-0006 — hour 24 and the leap second are ORDINARY timestamp input, and the
// day they carry belongs to the timestamp but NOT to the date.
//
// PostgreSQL's DecodeDateTime() deliberately admits tm_hour == 24 and
// tm_sec == 60 and leaves them in the struct; time_overflows()
// (postgres/src/backend/utils/adt/date.c) then range-checks the fields
// individually and separately requires the TOTAL not to exceed 24:00:00. The
// fold into the next day is not part of decoding at all — it falls out of
// tm2timestamp(), which composes date2j(y,m,d) * USECS_PER_DAY with the
// time-of-day microseconds. So:
//
//	'2020-01-01 24:00:00'  -> 2020-01-02 00:00:00
//	'2020-01-01 23:59:60'  -> 2020-01-02 00:00:00
//	'2020-01-01 10:00:60'  -> 2020-01-01 10:01:00   (below the boundary: rolls
//	                                                 the minute, no day carry)
//	'2020-01-01 24:00:00.5'-> error 22008           (total past the whole day)
//
// goopg's time-only path had all of this; its timestamp path had none of it,
// because CanonicalizeTimeToken declined any token the canonical HH:MM:SS
// spelling cannot hold — so every spelling above was rejected outright as a
// syntax error. The two now share pgdatetime.TimeOfDay.Normalize.
//
// date_in() is the one input function that must NOT see the carry: it never
// calls tm2timestamp, it hands tm_year/mon/mday straight to date2j. Applying the
// carry in the shared parse would have made '2020-01-01 24:00:00'::date answer
// the 2nd, a whole-day error of exactly the kind the preceding zone slice fixed.
//
// Every `want` below was captured from a PostgreSQL 18.3 oracle cluster.
func TestTimestampInputHour24AndLeapSecond(t *testing.T) {
	t.Parallel()

	wall := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}

	t.Run("timestamp composes the day carry", func(t *testing.T) {
		cases := []struct {
			in   string
			want time.Time
		}{
			{"2020-01-01 24:00:00", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01 23:59:60", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01 23:59:60.0", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01 24:00:00.000000", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01T24:00:00", wall(2020, 1, 2, 0, 0, 0)},
			// The seconds-less and run-together spellings decode to the same
			// whole day (pgdatetime.ParseTimeOfDay).
			{"2020-01-01 24:00", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01 240000", wall(2020, 1, 2, 0, 0, 0)},
			{"2020-01-01 235960", wall(2020, 1, 2, 0, 0, 0)},
			// The carry crosses the month and the year, so it must be a real
			// calendar add, not a +1 on the day field.
			{"2020-02-28 23:59:60", wall(2020, 2, 29, 0, 0, 0)},
			{"2020-12-31 24:00:00", wall(2021, 1, 1, 0, 0, 0)},
			// Below the day boundary a second 60 only rolls the minute.
			{"2020-01-01 10:00:60", wall(2020, 1, 1, 10, 1, 0)},
			{"2020-01-01 10:59:60", wall(2020, 1, 1, 11, 0, 0)},
			// The meridiem runs first, so 12 AM is hour 0 and the leap second
			// then rolls that minute.
			{"2020-01-01 12:00:60 AM", wall(2020, 1, 1, 0, 1, 0)},
		}
		for _, c := range cases {
			got, err := parsePGTimestampTextZone(c.in, tsDiscardZone)
			if err != nil {
				t.Errorf("parsePGTimestampTextZone(%q): %v", c.in, err)
				continue
			}
			if !got.Equal(c.want) {
				t.Errorf("parsePGTimestampTextZone(%q) = %s; want %s",
					c.in, got.Format(time.RFC3339Nano), c.want.Format(time.RFC3339Nano))
			}
		}
	})

	t.Run("date drops the day carry", func(t *testing.T) {
		// date_in never composes date with time, so the same text that is
		// 2020-01-02 as a timestamp is 2020-01-01 as a date.
		for _, in := range []string{
			"2020-01-01 24:00:00",
			"2020-01-01 23:59:60",
			"2020-12-31 24:00:00",
		} {
			got, err := parseDateInputText(in)
			if err != nil {
				t.Errorf("parseDateInputText(%q): %v", in, err)
				continue
			}
			wantDay := in[:10]
			if got.Format("2006-01-02") != wantDay {
				t.Errorf("parseDateInputText(%q) = %s; want day %s",
					in, got.Format(time.RFC3339Nano), wantDay)
			}
		}
	})

	t.Run("past the whole day is 22008 not 22007", func(t *testing.T) {
		// time_overflows()'s total check. These decode field-by-field and only
		// then fail, so upstream answers "date/time field value out of range"
		// (22008); reporting a syntax error would point at the spelling, which
		// is not what is wrong with them.
		for _, in := range []string{
			"2020-01-01 24:00:00.5",
			"2020-01-01 23:59:60.5",
			"2020-01-01 24:00:01",
			"2020-01-01 25:00:00",
		} {
			_, err := parsePGTimestampTextZone(in, tsDiscardZone)
			if err == nil {
				t.Errorf("parsePGTimestampTextZone(%q) accepted; want out-of-range", in)
				continue
			}
			execErr := dateTimeInputError(err, "timestamp", in, 0)
			if execErr.Code != "22008" {
				t.Errorf("parsePGTimestampTextZone(%q) reported %s (%v); want 22008",
					in, execErr.Code, err)
			}
		}
	})

	t.Run("time-only path agrees", func(t *testing.T) {
		// Sibling-path guard: the time and timestamp readings of the same token
		// now share pgdatetime.TimeOfDay.Normalize, and only the time path used
		// to have the rule at all.
		cases := []struct {
			in   string
			want time.Time
		}{
			{"24:00:00", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)},
			{"23:59:60", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)},
			{"10:00:60", time.Date(1970, 1, 1, 10, 1, 0, 0, time.UTC)},
		}
		for _, c := range cases {
			got, err := parseTimeString(c.in)
			if err != nil {
				t.Errorf("parseTimeString(%q): %v", c.in, err)
				continue
			}
			if !got.Equal(c.want) {
				t.Errorf("parseTimeString(%q) = %s; want %s",
					c.in, got.Format(time.RFC3339Nano), c.want.Format(time.RFC3339Nano))
			}
		}
		for _, in := range []string{"24:00:00.5", "23:59:60.5", "24:00:01", "25:00:00"} {
			if _, err := parseTimeString(in); err == nil {
				t.Errorf("parseTimeString(%q) accepted; want out-of-range", in)
			}
		}
	})
}
