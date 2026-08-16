package postmaster

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestAppendTypedCellTextDateHonorsDateStyle pins the M-NIGHTLY (run
// 20260714-011651) DateStyle output-rendering follow-up: appendTypedCellText's
// "date" case previously hardcoded ISO output regardless of the session's
// `datestyle` GUC, so `SET datestyle = 'SQL'` (or Postgres/German) had no
// effect on `SELECT` results even though `SHOW datestyle` correctly reflected
// the new value. A nil getSetting (no session) falls back to the ISO/MDY boot
// default, matching every other getSetting-driven case in this switch.
func TestAppendTypedCellTextDateHonorsDateStyle(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	dateType := catalog.Type{Name: "date"}
	day := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	d := executor.NewDateDatum(day)

	tests := []struct {
		name       string
		getSetting func(name string) (string, bool)
		want       string
	}{
		{"nil session falls back to ISO/MDY", nil, "2026-07-14"},
		{"ISO, MDY", constSetting("ISO, MDY"), "2026-07-14"},
		{"SQL, MDY", constSetting("SQL, MDY"), "07/14/2026"},
		{"SQL, DMY", constSetting("SQL, DMY"), "14/07/2026"},
		{"Postgres, MDY", constSetting("Postgres, MDY"), "07-14-2026"},
		{"Postgres, DMY", constSetting("Postgres, DMY"), "14-07-2026"},
		{"German, DMY", constSetting("German, DMY"), "14.07.2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(srv.appendTypedCellText(nil, d, dateType, tt.getSetting))
			if got != tt.want {
				t.Errorf("appendTypedCellText(date, %s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}


// TestAppendTypedCellTextTimestampHonorsDateStyle is the sibling of
// TestAppendTypedCellTextDateHonorsDateStyle: appendTypedCellText had no
// "timestamp"/"timestamptz" case at all (fell through to the default,
// hardcoded-ISO AppendValueText), so `SET datestyle` had zero effect on
// SELECT results for timestamp columns.
//
// M0119-0006 later split the two types apart: `timestamptz` now prints its
// zone (config.FormatTimestampTZ), which is where the per-type suffix below
// comes from — ISO takes the numeric offset with no space, the other three
// styles take the abbreviation after a space. This session has no "timezone"
// setting (constSetting answers only "datestyle"), so the zone is the boot
// default, UTC.
func TestAppendTypedCellTextTimestampHonorsDateStyle(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	when := time.Date(2026, time.July, 14, 9, 5, 3, 0, time.UTC)
	d := executor.NewTimeDatum(when)

	for _, typeName := range []string{"timestamp", "timestamptz"} {
		tsType := catalog.Type{Name: typeName}
		tests := []struct {
			name       string
			getSetting func(name string) (string, bool)
			want       string
			tzSuffix   string // appended only for timestamptz
		}{
			{"nil session falls back to ISO/MDY", nil, "2026-07-14 09:05:03", "+00"},
			{"ISO, MDY", constSetting("ISO, MDY"), "2026-07-14 09:05:03", "+00"},
			{"SQL, MDY", constSetting("SQL, MDY"), "07/14/2026 09:05:03", " UTC"},
			{"SQL, DMY", constSetting("SQL, DMY"), "14/07/2026 09:05:03", " UTC"},
			{"Postgres, MDY", constSetting("Postgres, MDY"), "Tue Jul 14 09:05:03 2026", " UTC"},
			{"Postgres, DMY", constSetting("Postgres, DMY"), "Tue 14 Jul 09:05:03 2026", " UTC"},
			{"German, DMY", constSetting("German, DMY"), "14.07.2026 09:05:03", " UTC"},
		}
		for _, tt := range tests {
			want := tt.want
			if typeName == "timestamptz" {
				want += tt.tzSuffix
			}
			t.Run(typeName+"/"+tt.name, func(t *testing.T) {
				got := string(srv.appendTypedCellText(nil, d, tsType, tt.getSetting))
				if got != want {
					t.Errorf("appendTypedCellText(%s, %s) = %q, want %q", typeName, tt.name, got, want)
				}
			})
		}
	}
}

// TestAppendTypedCellTextTimestampTZHonorsTimeZone pins the half of
// timestamptz_out that the DateStyle table above cannot see: the session's
// TimeZone GUC moves the printed wall clock, and only for `timestamptz`. Before
// M0119-0006 both types ignored TimeZone and printed no zone at all, so a
// client under `SET TimeZone='Asia/Kolkata'` read the returned text as a
// different instant than the one stored. Values pinned against PG 18.3.
func TestAppendTypedCellTextTimestampTZHonorsTimeZone(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	d := executor.NewTimeDatum(time.Date(2020, time.June, 15, 10, 0, 0, 0, time.UTC))
	getSetting := func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return "ISO, MDY", true
		case "timezone":
			return "Asia/Kolkata", true
		}
		return "", false
	}
	if got := string(srv.appendTypedCellText(nil, d, catalog.Type{Name: "timestamptz"}, getSetting)); got != "2020-06-15 15:30:00+05:30" {
		t.Errorf("timestamptz under TimeZone=Asia/Kolkata = %q, want %q", got, "2020-06-15 15:30:00+05:30")
	}
	// `timestamp without time zone` is deliberately untouched by TimeZone.
	if got := string(srv.appendTypedCellText(nil, d, catalog.Type{Name: "timestamp"}, getSetting)); got != "2020-06-15 10:00:00" {
		t.Errorf("timestamp under TimeZone=Asia/Kolkata = %q, want %q", got, "2020-06-15 10:00:00")
	}
}

func constSetting(v string) func(name string) (string, bool) {
	return func(name string) (string, bool) {
		if name == "datestyle" {
			return v, true
		}
		return "", false
	}
}
