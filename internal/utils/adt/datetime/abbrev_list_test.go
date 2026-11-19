package datetime

import (
	"testing"
	"time"
)

func TestZoneAndDefaultAbbrevRows(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tz database")
	}
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	got := map[string]AbbrevRow{}
	for _, r := range ZoneAbbrevRows(la, now) {
		got[r.Abbrev] = r
	}
	// sysviews.sql: the local mean time America/Los_Angeles used before 1883.
	if r, ok := got["LMT"]; !ok || r.Offset != -28378 || r.IsDST {
		t.Errorf("LMT = %+v, %v", r, ok)
	}
	if r, ok := got["PDT"]; !ok || !r.IsDST || r.Offset != -25200 {
		t.Errorf("PDT = %+v, %v", r, ok)
	}
	load := func(n string) (*time.Location, bool) { l, err := time.LoadLocation(n); return l, err == nil }
	rows := DefaultAbbrevRows(now, load)
	if len(rows) != len(defaultZoneAbbrevs) {
		t.Errorf("default rows %d, want %d: a dynamic abbreviation the zone data no longer uses keeps the zone's offset",
			len(rows), len(defaultZoneAbbrevs))
	}
	for _, r := range rows {
		if r.Abbrev == "ART" && r.Offset != -10800 {
			t.Errorf("ART = %+v", r)
		}
	}
}
