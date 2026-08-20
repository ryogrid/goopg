package executor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0134-0026 / M0134-0026b — a `timestamptz` input string that carries NO zone
// information must be read as LOCAL WALL-CLOCK TIME in the session's
// `TimeZone` GUC and converted to UTC, matching DecodeDateTime's "timezone not
// specified? then use session timezone" rule (postgres/src/backend/utils/adt/
// datetime.c:1573-1583, DetermineTimeZoneOffset at :1591-1740). goopg
// previously anchored the zone-less digits to UTC regardless of the session
// zone, silently storing the wrong instant.
//
// The design doc also asked for an embedded-zone-NAME literal
// ('2006-08-13 12:34:56 Europe/London'::timestamptz, PG 18.3: 2006-08-13
// 11:34:56 UTC) as an "unaffected" case. goopg cannot parse that spelling at
// all — pgTimestampLayouts only has numeric-offset ("Z07*") zone layouts, no
// named-zone one — so it 22007s both before and after this change; see
// TestTimestamptzZoneNameLiteralStillUnsupported below and the M0134-0026b
// report's PG-semantics discoveries (this is a pre-existing, unrelated gap).
//
// Every `want` below is a UTC instant captured VERBATIM from a real PG 18.3
// instance (`postgres/local_install/bin/{initdb,pg_ctl,psql}`, a throwaway
// cluster on port 5544) via
// `SET TimeZone='...'; SELECT '<lit>'::timestamptz AT TIME ZONE 'UTC';` — see
// the M0134-0026b report for the full transcript. None of these were
// hand-computed.
func TestTimestamptzLiteralAppliesSessionTimeZone(t *testing.T) {
	t.Parallel()

	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}

	cases := []struct {
		name string
		zone string
		lit  string
		want time.Time
	}{
		// Zone-less literal in a non-UTC session: the wall clock is read as
		// LOCAL time in America/Los_Angeles (PDT, UTC-7 on this date) and
		// converted to UTC. PG 18.3 oracle:
		//   SET TimeZone='America/Los_Angeles';
		//   SELECT '2006-08-13 12:34:56'::timestamptz AT TIME ZONE 'UTC';
		//    => 2006-08-13 19:34:56
		{"zone-less, LA session", "America/Los_Angeles",
			"2006-08-13 12:34:56", utc(2006, 8, 13, 19, 34, 56)},

		// Explicit numeric offset: UNCHANGED by this fix — the offset wins
		// regardless of session zone. PG 18.3 oracle:
		//   SELECT '2006-08-13 12:34:56-04'::timestamptz AT TIME ZONE 'UTC';
		//    => 2006-08-13 16:34:56
		{"explicit offset, LA session", "America/Los_Angeles",
			"2006-08-13 12:34:56-04", utc(2006, 8, 13, 16, 34, 56)},

		// UTC session: identical to the pre-fix behavior (control).
		{"zone-less, UTC session", "UTC",
			"2006-08-13 12:34:56", utc(2006, 8, 13, 12, 34, 56)},

		// DST spring-forward: 2023-03-12 02:30:00 does not exist in
		// America/Los_Angeles (clocks jump 02:00 -> 03:00 PDT). PG prefers the
		// BEFORE (standard, PST -08) offset. PG 18.3 oracle:
		//   SELECT '2023-03-12 02:30:00'::timestamptz AT TIME ZONE 'UTC';
		//    => 2023-03-12 10:30:00
		{"DST nonexistent (spring-forward)", "America/Los_Angeles",
			"2023-03-12 02:30:00", utc(2023, 3, 12, 10, 30, 0)},

		// DST fall-back: 2023-11-05 01:30:00 occurs twice in
		// America/Los_Angeles. PG prefers the AFTER (standard, PST -08)
		// offset. PG 18.3 oracle:
		//   SELECT '2023-11-05 01:30:00'::timestamptz AT TIME ZONE 'UTC';
		//    => 2023-11-05 09:30:00
		{"DST ambiguous (fall-back)", "America/Los_Angeles",
			"2023-11-05 01:30:00", utc(2023, 11, 5, 9, 30, 0)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := tzCtx("ISO, MDY", c.zone)
			x := &optimizer.TypedStringLit{Type: "timestamptz", Value: c.lit}
			d, err := evalTypedStringLit(x, ctx)
			if err != nil {
				t.Fatalf("evalTypedStringLit(%q) under TimeZone=%s: %v", c.lit, c.zone, err)
			}
			got := d.TimeValue().UTC()
			if !got.Equal(c.want) {
				t.Errorf("evalTypedStringLit(%q) under TimeZone=%s = %v, want %v (PG 18.3 oracle)",
					c.lit, c.zone, got, c.want)
			}
		})
	}
}

// TestTimestampLiteralIgnoresSessionTimeZone is the plain-`timestamp` control:
// a zone-less `timestamp` literal must keep discarding a decoded zone AND
// ignore the session TimeZone entirely — the wall-clock digits are stored
// verbatim regardless of TimeZone. PG 18.3 oracle:
//
//	SET TimeZone='America/Los_Angeles';
//	SELECT '2006-08-13 12:34:56'::timestamp;
//	 => 2006-08-13 12:34:56
func TestTimestampLiteralIgnoresSessionTimeZone(t *testing.T) {
	t.Parallel()
	ctx := tzCtx("ISO, MDY", "America/Los_Angeles")
	x := &optimizer.TypedStringLit{Type: "timestamp", Value: "2006-08-13 12:34:56"}
	d, err := evalTypedStringLit(x, ctx)
	if err != nil {
		t.Fatalf("evalTypedStringLit: %v", err)
	}
	want := time.Date(2006, 8, 13, 12, 34, 56, 0, time.UTC)
	if got := d.TimeValue().UTC(); !got.Equal(want) {
		t.Errorf("evalTypedStringLit(timestamp) under TimeZone=America/Los_Angeles = %v, want %v (PG 18.3 oracle)",
			got, want)
	}
}

// TestTimestamptzZoneNameLiteralStillUnsupported documents (not asserts a
// fix for) a pre-existing, unrelated gap this slice's guard test surfaced
// while capturing the design doc's "literal with a zone name" oracle case:
// goopg's pgTimestampLayouts has no named-zone layout, only numeric-offset
// ("Z07*") ones, so '2006-08-13 12:34:56 Europe/London'::timestamptz 22007s
// where PG 18.3 answers 2006-08-13 11:34:56 UTC. This is unaffected by
// M0134-0026 (still errors identically before and after).
func TestTimestamptzZoneNameLiteralStillUnsupported(t *testing.T) {
	t.Parallel()
	ctx := tzCtx("ISO, MDY", "America/Los_Angeles")
	x := &optimizer.TypedStringLit{Type: "timestamptz", Value: "2006-08-13 12:34:56 Europe/London"}
	if _, err := evalTypedStringLit(x, ctx); err == nil {
		t.Errorf("evalTypedStringLit(zone-name literal) unexpectedly succeeded — " +
			"if named-zone parsing was added, promote this to a real assertion " +
			"(PG 18.3: 2006-08-13 11:34:56 UTC) instead of documenting the gap")
	}
}

// TestTimestamptzInsertLiteralAppliesSessionTimeZoneEndToEnd is the round-2
// extension of M0134-0026b (coordinator-authorised): it drives the fix
// through the REAL `INSERT` path — encodeValuePGCtx (codec.go), reached via
// EncodeRowPGCtx from the storage write operators — rather than exercising
// evalTypedStringLit/evalCast in isolation as the round-1 guard test does.
//
// Before round 2, goopg was internally INCONSISTENT: a bare zone-less
// literal INSERT (this test) reached encodeValuePGCtx's own
// parseCopyTimestampZone call, which was UTC-session, while the identical
// value spelled as an explicit `::timestamptz` cast reached evalCast, fixed
// in round 1 — the same session, the same literal, two different stored
// instants depending on which SQL spelling reached the column.
//
// PG 18.3 oracle (round-2 capture, throwaway cluster, port 5545):
//
//	SET TimeZone='America/Los_Angeles';
//	CREATE TABLE m0134_0026b_e2e (tstz timestamptz);
//	INSERT INTO m0134_0026b_e2e VALUES ('2006-08-13 12:34:56');
//	SELECT tstz, tstz AT TIME ZONE 'UTC' FROM m0134_0026b_e2e;
//	 => 2006-08-13 12:34:56-07 | 2006-08-13 19:34:56
//
// (Matches the round-1 typed-literal oracle capture for the same literal and
// zone exactly, as it must — storing then re-rendering in the SAME session
// zone round-trips the original wall clock.)
func TestTimestamptzInsertLiteralAppliesSessionTimeZoneEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return "ISO, MDY", true
		case "timezone":
			return "America/Los_Angeles", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, `CREATE TABLE m0134_0026b_e2e (tstz timestamptz)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO m0134_0026b_e2e VALUES ('2006-08-13 12:34:56')`)

	textRows := runSQL(t, ctx, `SELECT tstz::text FROM m0134_0026b_e2e`)
	if len(textRows) != 1 {
		t.Fatalf("got %d rows, want 1", len(textRows))
	}
	const want = "2006-08-13 12:34:56-07"
	if got := textRows[0][0].StringValue(); got != want {
		t.Errorf("SELECT tstz::text FROM m0134_0026b_e2e = %q, want %q (PG 18.3 oracle)", got, want)
	}

	// Cross-check the stored UTC instant directly (not just its rendering):
	// a KindTime datum's TimeValue() IS the stored UTC instant (goopg's
	// on-disk/in-memory convention), the same way the round-1 typed-literal
	// case checks it.
	rows := runSQL(t, ctx, `SELECT tstz FROM m0134_0026b_e2e`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	wantUTC := time.Date(2006, 8, 13, 19, 34, 56, 0, time.UTC)
	if got := rows[0][0].TimeValue().UTC(); !got.Equal(wantUTC) {
		t.Errorf("stored instant = %v, want %v (PG 18.3 oracle)", got, wantUTC)
	}
}

// TestTimestamptzEncodeValuePGCtxAppliesSessionTimeZone directly exercises
// encodeValuePGCtx's own zone-less-`timestamptz`-from-KindString branch
// (codec.go, M0134-0026 round 2). This is the branch the round-2 extension
// fixed; the SQL-level end-to-end test above (INSERT ... VALUES) does NOT
// actually reach it in practice — see this file's "Round 2" report entry for
// why (every SQL write path I could construct — INSERT VALUES, INSERT
// SELECT, UPDATE SET, INSERT ... ON CONFLICT — already coerces a zone-less
// timestamptz literal to KindTime, via either build-time literal typing
// (evalTypedStringLit) or coerceRowForConstraintChecks (evalCast), both
// fixed in round 1, BEFORE the row ever reaches this function). Calling
// encodeValuePGCtx directly with a raw KindString datum is the only way to
// prove this specific branch's behavior with real evidence, matching the
// coordinator's design-doc citation and reuse rule exactly as the other
// branches do.
//
// Expected micros are derived from the SAME PG 18.3 oracle capture as
// TestTimestamptzInsertLiteralAppliesSessionTimeZoneEndToEnd (round-2
// capture, port 5545): storing '2006-08-13 12:34:56' as timestamptz under
// TimeZone='America/Los_Angeles' is the UTC instant 2006-08-13 19:34:56.
func TestTimestamptzEncodeValuePGCtxAppliesSessionTimeZone(t *testing.T) {
	ctx := tzCtx("ISO, MDY", "America/Los_Angeles")
	d := NewStringDatum("2006-08-13 12:34:56")
	buf, err := encodeValuePGCtx(catalog.Type{Name: "timestamptz"}, d, ctx, 0)
	if err != nil {
		t.Fatalf("encodeValuePGCtx: %v", err)
	}
	if len(buf) != 8 {
		t.Fatalf("encoded len = %d, want 8", len(buf))
	}
	micros := int64(binary.LittleEndian.Uint64(buf))
	gotUTC := time.UnixMicro(micros + pgEpochUnixMicros).UTC()
	wantUTC := time.Date(2006, 8, 13, 19, 34, 56, 0, time.UTC)
	if !gotUTC.Equal(wantUTC) {
		t.Errorf("encodeValuePGCtx(zone-less timestamptz) under TimeZone=America/Los_Angeles stored instant = %v, want %v (PG 18.3 oracle)",
			gotUTC, wantUTC)
	}
}

// TestTimestamptzEncodeValuePGCtxNilCtxFallsBackToUTC is the nil-ctx
// defensiveness requirement from the round-2 extension: several real callers
// of encodeValuePGCtx pass ctx=nil (EncodeRowPG's toast chunk / catalog /
// sequence / vacuum row encoders, every internal/initdb bootstrap row) and
// must not panic or change behavior — timeZoneFromCtx(nil) already returns
// "" (UTC) by construction, so this pins that this arm keeps working
// end-to-end through a nil ctx exactly as it did before round 2.
func TestTimestamptzEncodeValuePGCtxNilCtxFallsBackToUTC(t *testing.T) {
	d := NewStringDatum("2006-08-13 12:34:56")
	buf, err := encodeValuePGCtx(catalog.Type{Name: "timestamptz"}, d, nil, 0)
	if err != nil {
		t.Fatalf("encodeValuePGCtx(nil ctx): %v", err)
	}
	micros := int64(binary.LittleEndian.Uint64(buf))
	gotUTC := time.UnixMicro(micros + pgEpochUnixMicros).UTC()
	wantUTC := time.Date(2006, 8, 13, 12, 34, 56, 0, time.UTC)
	if !gotUTC.Equal(wantUTC) {
		t.Errorf("encodeValuePGCtx(nil ctx) stored instant = %v, want %v (UTC identity)", gotUTC, wantUTC)
	}
}
