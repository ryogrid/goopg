package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/parser"
)

// TestConcatHonorsDateStyle pins the next M-NIGHTLY DateStyle follow-up
// slice: the `||` operator's text-concatenation branch (evalBinary's
// parser.OpConcat case) always called Datum.Format() on its operands, which
// hardcodes ISO for TIMESTAMP and Postgres-style MDY for DATE regardless of
// `SET datestyle` — diverging from the already-fixed SELECT/COPY/CAST-to-text
// output paths (formatDatumDateStyle/dateStyleFromCtx). `array_to_string`'s
// array-element rendering (operators_join_agg.go) shares the same underlying
// gap but is out of scope for this slice.
func TestConcatHonorsDateStyle(t *testing.T) {
	when := time.Date(2026, 7, 14, 9, 5, 3, 0, time.UTC)

	cases := []struct {
		name      string
		left      Datum
		right     Datum
		dateStyle string
		want      string
	}{
		{"prefix || date ISO", NewStringDatum("d="), NewDateDatum(when), "ISO, MDY", "d=2026-07-14"},
		{"prefix || date SQL MDY", NewStringDatum("d="), NewDateDatum(when), "SQL, MDY", "d=07/14/2026"},
		{"prefix || date Postgres DMY", NewStringDatum("d="), NewDateDatum(when), "Postgres, DMY", "d=14-07-2026"},
		{"prefix || date German", NewStringDatum("d="), NewDateDatum(when), "German, DMY", "d=14.07.2026"},
		{"timestamp || suffix ISO", NewTimeDatum(when), NewStringDatum(" end"), "ISO, MDY", "2026-07-14 09:05:03 end"},
		{"timestamp || suffix SQL DMY", NewTimeDatum(when), NewStringDatum(" end"), "SQL, DMY", "14/07/2026 09:05:03 end"},
		{"timestamp || suffix Postgres MDY", NewTimeDatum(when), NewStringDatum(" end"), "Postgres, MDY", "Tue Jul 14 09:05:03 2026 end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &Context{GetSetting: func(name string) (string, bool) {
				if name == "datestyle" {
					return c.dateStyle, true
				}
				return "", false
			}}
			got, err := evalBinary(parser.OpConcat, c.left, c.right, 0, ctx)
			if err != nil {
				t.Fatalf("evalBinary(||) unexpected error: %v", err)
			}
			if got.Kind != KindString || got.StringValue() != c.want {
				t.Errorf("|| with datestyle=%q = %q, want %q", c.dateStyle, got.StringValue(), c.want)
			}
		})
	}
}

// TestConcatNilCtxDefaultsISO confirms a nil ctx (batched/vectorised and
// window-frame evalBinary callers that never carry a session) falls back to
// ISO/MDY, matching Format()'s pre-existing hardcoded default so those
// call sites are behavior-unchanged by this fix.
func TestConcatNilCtxDefaultsISO(t *testing.T) {
	when := time.Date(2026, 7, 14, 9, 5, 3, 0, time.UTC)
	got, err := evalBinary(parser.OpConcat, NewStringDatum("d="), NewDateDatum(when), 0, nil)
	if err != nil {
		t.Fatalf("evalBinary(||) unexpected error: %v", err)
	}
	if want := "d=2026-07-14"; got.StringValue() != want {
		t.Errorf("|| with nil ctx = %q, want %q", got.StringValue(), want)
	}
}
