package executor

import (
	"testing"
	"time"
)

// TestEvalCastTimeToTextHonorsDateStyle pins the M-NIGHTLY DateStyle
// follow-up: `x::text` (and CAST(x AS text)) on a DATE/TIMESTAMP value must
// honor the session's `datestyle` GUC exactly like the SELECT/COPY output
// paths (dispatch.go's appendTypedCellText, copy_text.go's datumToCopyText)
// already do — before this fix evalCast's KindTime "text" branch always
// called Datum.Format(), which hardcodes ISO for TIMESTAMP and
// Postgres-style MDY for DATE regardless of `SET datestyle`.
func TestEvalCastTimeToTextHonorsDateStyle(t *testing.T) {
	when := time.Date(2026, 7, 14, 9, 5, 3, 0, time.UTC)

	cases := []struct {
		name      string
		d         Datum
		dateStyle string
		want      string
	}{
		{"date ISO", NewDateDatum(when), "ISO, MDY", "2026-07-14"},
		{"date SQL MDY", NewDateDatum(when), "SQL, MDY", "07/14/2026"},
		{"date Postgres DMY", NewDateDatum(when), "Postgres, DMY", "14-07-2026"},
		{"date German", NewDateDatum(when), "German, DMY", "14.07.2026"},
		{"timestamp ISO", NewTimeDatum(when), "ISO, MDY", "2026-07-14 09:05:03"},
		{"timestamp SQL DMY", NewTimeDatum(when), "SQL, DMY", "14/07/2026 09:05:03"},
		{"timestamp Postgres MDY", NewTimeDatum(when), "Postgres, MDY", "Tue Jul 14 09:05:03 2026"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &Context{GetSetting: func(name string) (string, bool) {
				if name == "datestyle" {
					return c.dateStyle, true
				}
				return "", false
			}}
			got, err := evalCast(c.d, "text", 0, ctx)
			if err != nil {
				t.Fatalf("evalCast(text) unexpected error: %v", err)
			}
			if got.Kind != KindString || got.StringValue() != c.want {
				t.Errorf("evalCast(text) with datestyle=%q = %q, want %q", c.dateStyle, got.StringValue(), c.want)
			}
		})
	}
}

// TestEvalCastTimeToTextNilCtxDefaultsISO confirms a nil ctx (the 4
// non-session call sites: unnest coercion, ALTER-COLUMN-TYPE row rewrite
// without a live GetSetting, etc.) falls back to ISO/MDY — the same default
// dispatch.go/copy_text.go use when getSetting is nil, so behavior for those
// call sites is unchanged by this fix.
func TestEvalCastTimeToTextNilCtxDefaultsISO(t *testing.T) {
	when := time.Date(2026, 7, 14, 9, 5, 3, 0, time.UTC)
	got, err := evalCast(NewTimeDatum(when), "text", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(text) unexpected error: %v", err)
	}
	if want := "2026-07-14 09:05:03"; got.StringValue() != want {
		t.Errorf("evalCast(text) with nil ctx = %q, want %q", got.StringValue(), want)
	}
}

// TestEvalCastToDateSetsFlagDate pins a sibling bug uncovered while wiring
// insertOp's literal coercion (2026-07-15): evalCast's "date" case built its
// result via NewTimeDatum, which leaves TimeSub at TimeSubTimestamp, instead of
// NewDateDatum ("Use this at every date-producing site", datum.go). Every
// other date-producing site (storage decode, date literals) sets TimeSubDate so
// type-agnostic renderers (Datum.Format(), fkValsForDetail's
// formatTimeDatumDateStyle) can tell a DATE apart from a TIMESTAMP sharing
// the same KindTime carrier. Without the flag, a freshly-cast date datum
// rendered with a spurious "00:00:00" time-of-day suffix wherever a
// downstream consumer branches on d.TimeSub == TimeSubDate instead of separately
// tracked column-type context.
func TestEvalCastToDateSetsFlagDate(t *testing.T) {
	// String source (e.g. an INSERT literal coerced via evalCast(..., "date", ...)).
	got, err := evalCast(NewStringDatum("2026-07-15"), "date", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(date) from string: unexpected error: %v", err)
	}
	if !got.IsDate() {
		t.Errorf("evalCast(date) from string did not set TimeSubDate; Format()=%q", got.Format())
	}

	// KindTime (timestamp) source, e.g. `some_timestamp_col::date`.
	when := time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC)
	got2, err := evalCast(NewTimeDatum(when), "date", 0, nil)
	if err != nil {
		t.Fatalf("evalCast(date) from timestamp: unexpected error: %v", err)
	}
	if !got2.IsDate() {
		t.Errorf("evalCast(date) from timestamp did not set TimeSubDate; Format()=%q", got2.Format())
	}
}
