package datetime

import "testing"

// TestAbbrevSetsLoadLikePG pins M0149-0001h1a82: the embedded Default file
// parses to the generated Default table, India and Australia override the
// entries their files name, and an unknown or dotted name is rejected, as
// load_tzoffsets does.
func TestAbbrevSetsLoadLikePG(t *testing.T) {
	m := map[string]zoneAbbrev{}
	if err := parseTzFile("Default", 0, m); err != nil {
		t.Fatal(err)
	}
	if len(m) != len(defaultZoneAbbrevs) {
		t.Fatalf("Default parses to %d entries, generated table has %d", len(m), len(defaultZoneAbbrevs))
	}
	for k, v := range defaultZoneAbbrevs {
		if m[k] != v {
			t.Errorf("%s: parsed %+v, generated %+v", k, m[k], v)
		}
	}
	india, err := abbrevSet("India")
	if err != nil {
		t.Fatal(err)
	}
	if got := india["ist"]; got.kind != tokTZ || got.offset != 19800 {
		t.Errorf("India IST = %+v, want +05:30", got)
	}
	aus, err := abbrevSet("Australia")
	if err != nil {
		t.Fatal(err)
	}
	if got := aus["est"]; got.offset != 36000 {
		t.Errorf("Australia EST = %+v, want +10:00", got)
	}
	if got := defaultZoneAbbrevs["est"]; got.offset != -18000 {
		t.Errorf("Default EST changed: %+v", got)
	}
	for _, bad := range []string{"Nope", "Africa.txt", "../Default"} {
		if ValidAbbrevSet(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
