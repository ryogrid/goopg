package executor

import "testing"

// TestRaiseMsgHonorsDateStyle pins the next M-NIGHTLY DateStyle follow-up
// slice (resume point recorded after the ANALYZE MCV/histogram fix):
// evalRaiseMsg (plpgsql_runtime.go) called Datum.Format() directly to render
// each RAISE %-argument, hardcoding ISO/Postgres-MDY for a DATE/TIMESTAMP/
// TIMESTAMPTZ argument regardless of the session's `SET datestyle` — the same
// bug class already fixed for ANALYZE's pg_stats rendering, array_agg/
// string_agg, and CAST-to-text. Fixed by routing the RAISE %-arg formatting
// through formatDatumDateStyle(val, ctx), which evalRaiseMsg already has a
// live ctx for (it's only reached from live PL/pgSQL execution).
//
// The DATE value is sourced via `SELECT ... INTO` from a real table column
// (bindSelectIntoRow copies the heap-decoded Datum straight into the frame,
// preserving its KindTime/TimeSubDate) rather than a `declare d date := '...'`
// default: the latter routes through coerceDatumToType's string-literal
// coercion (isTimeTypeName branch, plpgsql_runtime.go), which was found
// during this audit to always call tryParseStringAs(KindTime, ...) and
// therefore mint a TIMESTAMP-shaped datum (TimeSub left at TimeSubTimestamp, "00:00:00" tail)
// even for a `date`-typed declaration — a separate, pre-existing PG-parity
// gap logged in the deferral ledger rather than fixed here (out of scope for
// this RAISE-formatting slice).
func TestRaiseMsgHonorsDateStyle(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "datestyle" {
			return "German, DMY", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, `CREATE TABLE raise_src (id int, dcol date)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO raise_src VALUES (1, '2026-01-05')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION raise_date_exc() RETURNS void LANGUAGE plpgsql AS $$
declare d date;
begin
  select dcol into d from raise_src where id = 1;
  raise exception 'bad date: %', d;
end;
$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	err := runQueryExpectErr(ctx, `SELECT raise_date_exc()`)
	if err == nil {
		t.Fatal("raise_date_exc(): expected an error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error = %v (%T), want *ExecError", err, err)
	}
	if want := "bad date: 05.01.2026"; ee.Message != want {
		t.Errorf("Message = %q, want %q (German DateStyle)", ee.Message, want)
	}
}

// TestRaiseMsgDefaultsISOWithNoDateStyleGUC confirms a session with no
// datestyle GUC reachable (GetSetting reports not-found) still defaults to
// ISO/MDY, matching Format()'s pre-existing hardcoded behavior — so the
// common case (default DateStyle) is behavior-unchanged by this fix.
func TestRaiseMsgDefaultsISOWithNoDateStyleGUC(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE raise_src2 (id int, dcol date)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO raise_src2 VALUES (1, '2026-01-05')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION raise_date_exc2() RETURNS void LANGUAGE plpgsql AS $$
declare d date;
begin
  select dcol into d from raise_src2 where id = 1;
  raise exception 'bad date: %', d;
end;
$$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	err := runQueryExpectErr(ctx, `SELECT raise_date_exc2()`)
	if err == nil {
		t.Fatal("raise_date_exc2(): expected an error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error = %v (%T), want *ExecError", err, err)
	}
	if want := "bad date: 2026-01-05"; ee.Message != want {
		t.Errorf("Message = %q, want %q (ISO default, no session GUC)", ee.Message, want)
	}
}
