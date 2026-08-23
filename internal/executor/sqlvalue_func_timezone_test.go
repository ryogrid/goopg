package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCurrentDateIsTaggedDate pins M0134-0084 (expressions.sql): current_date
// shares the KindTime carrier with timestamp, and the site used to build it
// with NewTimeDatum (no TimeSub tag), so `current_date::text` rendered the
// full "YYYY-MM-DD 00:00:00" timestamp shape instead of a bare date and
// `date(now())::text = current_date::text` always compared unequal
// regardless of the actual day. NewDateDatum is the fix.
func TestCurrentDateIsTaggedDate(t *testing.T) {
	ctx := &Context{Now: time.Date(2026, 8, 23, 14, 46, 8, 107_000_000, time.UTC)}
	got, err := evalFuncCall(&optimizer.FuncCall{Name: "current_date"}, nil, ctx)
	if err != nil {
		t.Fatalf("current_date: %v", err)
	}
	if !got.IsDate() {
		t.Fatalf("current_date did not return a TimeSubDate-tagged Datum: %+v", got)
	}
	if got.Format() != "08-23-2026" {
		t.Errorf("current_date.Format() = %q, want a bare date (08-23-2026)", got.Format())
	}
}

// TestLocalTimestampMatchesTimestamptzCastToTimestamp pins M0134-0084
// (expressions.sql): PG derives both `localtimestamp` and `now()::timestamp`
// from the same transaction-start instant converted into the session
// TimeZone (timestamptz_timestamp, date.c) — they must always agree.
// localtimestamp previously skipped the TimeZone conversion entirely and
// returned the raw UTC instant relabelled as local wall clock.
func TestLocalTimestampMatchesTimestamptzCastToTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 46, 8, 107_000_000, time.UTC)
	ctx := tzCtx("ISO, MDY", "America/Los_Angeles")
	ctx.Now = now

	viaLocaltimestamp, err := evalFuncCall(&optimizer.FuncCall{Name: "localtimestamp"}, nil, ctx)
	if err != nil {
		t.Fatalf("localtimestamp: %v", err)
	}
	viaNowCast, err := evalCast(NewTimestampTZDatum(now), "timestamp", 0, ctx)
	if err != nil {
		t.Fatalf("now()::timestamp: %v", err)
	}
	if viaLocaltimestamp.TimeValue() != viaNowCast.TimeValue() {
		t.Errorf("localtimestamp = %v, now()::timestamp = %v, want equal (session zone America/Los_Angeles)",
			viaLocaltimestamp.TimeValue(), viaNowCast.TimeValue())
	}
	// Sanity: the LA offset (-7 in August, DST) must actually have shifted
	// the wall clock away from the naive UTC hour, or this test would pass
	// vacuously even with the old UTC-only bug.
	if viaLocaltimestamp.TimeValue().Hour() == now.Hour() {
		t.Fatalf("test setup did not exercise a real zone shift: got hour %d same as UTC", viaLocaltimestamp.TimeValue().Hour())
	}
}

// TestCurrentTimeAndLocaltimeRoundLikeTimeCast pins M0134-0084
// (expressions.sql): current_time(N)/localtime(N) previously floored the
// fractional seconds via integer division, while the `::time(N)` CastExpr
// Typmod path rounds (AdjustTimeForTypmod, date.c:1710) — the mismatch made
// `now()::time(N)::text = localtime(N)::text` flaky (they only agreed when
// the discarded digits happened to floor and round to the same value).
func TestCurrentTimeAndLocaltimeRoundLikeTimeCast(t *testing.T) {
	// .9995 seconds: floor(3) truncates to .999, PG's rounding carries to
	// the next second — the exact divergence class this pins.
	now := time.Date(2026, 8, 23, 14, 46, 8, 999_500_000, time.UTC)
	ctx := &Context{Now: now}
	prec := &optimizer.IntegerConst{Value: 3}

	viaCurrentTime, err := evalFuncCall(&optimizer.FuncCall{Name: "current_time", Args: []optimizer.Expr{prec}}, nil, ctx)
	if err != nil {
		t.Fatalf("current_time(3): %v", err)
	}
	viaLocaltime, err := evalFuncCall(&optimizer.FuncCall{Name: "localtime", Args: []optimizer.Expr{prec}}, nil, ctx)
	if err != nil {
		t.Fatalf("localtime(3): %v", err)
	}
	viaCast, err := evalCast(NewTimeDatum(now), "time", 0, ctx)
	if err != nil {
		t.Fatalf("now()::time: %v", err)
	}
	viaCast = roundTimeDatumToPrecision(viaCast, 3)

	if viaCurrentTime.TimeValue() != viaCast.TimeValue() {
		t.Errorf("current_time(3) = %v, now()::time(3) = %v, want equal", viaCurrentTime.TimeValue(), viaCast.TimeValue())
	}
	if viaLocaltime.TimeValue() != viaCast.TimeValue() {
		t.Errorf("localtime(3) = %v, now()::time(3) = %v, want equal", viaLocaltime.TimeValue(), viaCast.TimeValue())
	}
}
