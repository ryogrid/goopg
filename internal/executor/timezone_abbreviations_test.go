package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestTimezoneAbbreviationsSetting pins M0149-0001h1a82: date/time input reads
// a zone abbreviation from the set timezone_abbreviations names, as
// load_tzoffsets does — IST is Israel (+02) in Default and India (+05:30) in
// the India set, EST is -05 in Default and +10 in Australia.
func TestTimezoneAbbreviationsSetting(t *testing.T) {
	read := func(set, v string) string {
		ctx := &Context{GetSetting: func(name string) (string, bool) {
			switch name {
			case "timezone":
				return "UTC", true
			case "datestyle":
				return "ISO, MDY", true
			case "timezone_abbreviations":
				return set, set != ""
			}
			return "", false
		}}
		d, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "timestamptz", Value: v}, ctx)
		if err != nil {
			t.Fatalf("%s %q: %v", set, v, err)
		}
		return formatDatumDateStyle(d, ctx)
	}
	for _, c := range []struct{ set, in, want string }{
		{"", "2020-01-01 00:00 IST", "2019-12-31 22:00:00+00"},
		{"India", "2020-01-01 00:00 IST", "2019-12-31 18:30:00+00"},
		{"Default", "2020-01-01 00:00 EST", "2020-01-01 05:00:00+00"},
		{"Australia", "2020-01-01 00:00 EST", "2019-12-31 14:00:00+00"},
	} {
		if got := read(c.set, c.in); got != c.want {
			t.Errorf("timezone_abbreviations=%q %s = %s, want %s", c.set, c.in, got, c.want)
		}
	}
}
