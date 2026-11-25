package executor

import "testing"

// TestCanonicalDateTimeLiteral pins literal texts measured with EXPLAIN on
// PG 18.3: the value is read under the given zone and DateStyle and printed
// by the output function.
func TestCanonicalDateTimeLiteral(t *testing.T) {
	cases := []struct {
		typ, value, zone, style, want string
	}{
		{"timestamptz", "2020-01-01", "America/New_York", "ISO, MDY", "2020-01-01 00:00:00-05"},
		{"timestamptz", "2020-06-01 12:00", "America/New_York", "ISO, MDY", "2020-06-01 12:00:00-04"},
		{"timestamp", "2020-01-01 10:00", "America/New_York", "ISO, MDY", "2020-01-01 10:00:00"},
		{"date", "2020-1-1", "UTC", "ISO, MDY", "2020-01-01"},
		{"date", "01/02/2020", "UTC", "Postgres, MDY", "01-02-2020"},
		{"date", "02/01/2020", "UTC", "SQL, DMY", "02/01/2020"},
		{"timetz", "10:00", "America/New_York", "ISO, MDY", "10:00:00-05"},
		{"timestamptz", "infinity", "UTC", "ISO, MDY", "infinity"},
		{"date", "0044-03-15 BC", "UTC", "ISO, MDY", "0044-03-15 BC"},
	}
	for _, c := range cases {
		got, ok := canonicalDateTimeLiteral(c.typ, c.value, c.zone, c.style, "")
		if !ok || got != c.want {
			t.Errorf("%s %q under %s/%s = %q, %v; want %q", c.typ, c.value, c.zone, c.style, got, ok, c.want)
		}
	}
	if _, ok := canonicalDateTimeLiteral("date", "not a date", "UTC", "ISO, MDY", ""); ok {
		t.Error("an invalid literal is left for run time to report")
	}
}
