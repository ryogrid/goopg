package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCurrentTimeLiteralsUseSessionZone pins M0149-0001h1a81 against PG
// 18.3's DecodeDateTime: 'today', 'tomorrow' and 'yesterday' are midnight of
// the session TimeZone's current date, 'now' as a timestamp without time zone
// is the local wall clock, and current_date is the local date. goopg used the
// UTC date, a day off near midnight (Tokyo at 2026-11-22 20:00 UTC is already
// 11-23).
func TestCurrentTimeLiteralsUseSessionZone(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	ctx := tzCtx("ISO, MDY", "Asia/Tokyo")
	ctx.Now = now
	lit := func(typ, v string) string {
		d, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: typ, Value: v}, ctx)
		if err != nil {
			t.Fatalf("%s '%s': %v", typ, v, err)
		}
		return formatDatumDateStyle(d, ctx)
	}
	for _, c := range []struct{ typ, v, want string }{
		{"timestamptz", "today", "2026-11-23 00:00:00+09"},
		{"timestamptz", "tomorrow", "2026-11-24 00:00:00+09"},
		{"timestamp", "today", "2026-11-23 00:00:00"},
		{"timestamp", "now", "2026-11-23 05:00:00"},
		{"date", "today", "2026-11-23"},
		{"date", "yesterday", "2026-11-22"},
	} {
		if got := lit(c.typ, c.v); got != c.want {
			t.Errorf("%s '%s' = %s, want %s", c.typ, c.v, got, c.want)
		}
	}
	d, err := evalFuncCall(&optimizer.FuncCall{Name: "current_date"}, nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := formatDatumDateStyle(d, ctx); got != "2026-11-23" {
		t.Errorf("current_date = %s, want 2026-11-23", got)
	}
}
