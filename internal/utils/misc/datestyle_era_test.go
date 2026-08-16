package misc

import (
	"testing"
	"time"
)

// goopg stores dates the way PG's date2j/j2date count them — 1 BC is year 0,
// 2 BC is year -1 — so a BC value must not reach Go's layout formatter as-is:
// `t.Format("2006-01-02")` renders year -2019 as "-2019-01-01", where every
// PostgreSQL DateStyle prints "2020-01-01 BC". EncodeDateOnly/EncodeDateTime
// (postgres/src/backend/utils/adt/datetime.c) print `-(tm_year - 1)` digits for
// tm_year <= 0 and append " BC" after everything else, including the time of
// day.
//
// Every `want` is what PG 18.3 answers under that DateStyle (local 18.3
// cluster, socket /tmp port 5599). M0119-0006.
func TestFormatDateEraBC(t *testing.T) {
	// Year 0 == 1 BC, year -2019 == 2020 BC.
	firstBC := time.Date(0, 6, 15, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		style, order, want string
	}{
		{"ISO", "MDY", "0001-06-15 BC"},
		{"SQL", "MDY", "06/15/0001 BC"},
		{"SQL", "DMY", "15/06/0001 BC"},
		{"Postgres", "MDY", "06-15-0001 BC"},
		{"German", "DMY", "15.06.0001 BC"},
	} {
		if got := FormatDate(firstBC, c.style, c.order); got != c.want {
			t.Errorf("FormatDate(1 BC, %s/%s) = %q, want %q", c.style, c.order, got, c.want)
		}
	}
	if got := FormatDate(time.Date(-2019, 1, 1, 0, 0, 0, 0, time.UTC), "ISO", "MDY"); got != "2020-01-01 BC" {
		t.Errorf("FormatDate(2020 BC, ISO) = %q, want %q", got, "2020-01-01 BC")
	}
}

func TestFormatTimestampEraBC(t *testing.T) {
	ts := time.Date(0, 6, 15, 7, 8, 9, 500000000, time.UTC) // 0001-06-15 07:08:09.5 BC
	for _, c := range []struct {
		style, order, want string
	}{
		{"ISO", "MDY", "0001-06-15 07:08:09.5 BC"},
		{"SQL", "MDY", "06/15/0001 07:08:09.5 BC"},
		{"German", "DMY", "15.06.0001 07:08:09.5 BC"},
		// The Postgres style also prints a WEEKDAY, which belongs to the stored
		// instant and not to the era-adjusted year: 15 June 1 BC is a Thursday
		// (PG: `extract(dow from '0001-06-15 BC'::date)` = 4), while 15 June
		// AD 1 is not. Formatting the whole value off a year-substituted copy
		// would print the wrong day name here and nowhere else.
		{"Postgres", "MDY", "Thu Jun 15 07:08:09.5 0001 BC"},
	} {
		if got := FormatTimestamp(ts, c.style, c.order); got != c.want {
			t.Errorf("FormatTimestamp(1 BC, %s/%s) = %q, want %q", c.style, c.order, got, c.want)
		}
	}
	if got := FormatTimestamp(time.Date(-2019, 1, 1, 0, 0, 0, 0, time.UTC), "ISO", "MDY"); got != "2020-01-01 00:00:00 BC" {
		t.Errorf("FormatTimestamp(2020 BC, ISO) = %q, want %q", got, "2020-01-01 00:00:00 BC")
	}
}

// The era suffix must appear for BC values and ONLY for them: an AD value has
// no marker at all, including year 1, which is the boundary the `y > 0` test
// sits on.
func TestFormatDateADCarriesNoEraMarker(t *testing.T) {
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "0001-01-01"},
		{time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "2020-01-01"},
	} {
		if got := FormatDate(c.t, "ISO", "MDY"); got != c.want {
			t.Errorf("FormatDate(%v, ISO) = %q, want %q", c.t, got, c.want)
		}
	}
}
