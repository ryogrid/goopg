package server

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

func constSetting(v string) func(name string) (string, bool) {
	return func(name string) (string, bool) {
		if name == "datestyle" {
			return v, true
		}
		return "", false
	}
}
