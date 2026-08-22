package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0134-0076 Bucket F — EXTRACT(field FROM ts) and date_part('field', ts) on a
// timestamptz source must extract in the SESSION TimeZone (not UTC), support the
// msec/usec/julian field aliases, return the real session offset for
// timezone/timezone_hour/timezone_minute, and handle ±infinity inputs.
//
// The two spellings are sibling paths (evalExtract / evalDatePart) and must
// agree — pattern_sibling_paths_must_agree. Every assertion below that a field
// has a value runs BOTH spellings.
//
// PG oracle: timestamp_part_common / timestamptz_part_common
// (postgres/src/backend/utils/adt/timestamp.c:5499/:5772). A timestamptz
// passes NULL zone to timestamp2tm → the session TimeZone; DTK_TZ/TZ_HOUR/
// TZ_MINUTE return the resolved offset; the field switch covers msec/usec/
// julian; a non-finite input returns NULL for the oscillating units and
// ±Infinity for the monotonically-increasing ones (NonFiniteTimestampTzPart,
// timestamp.c:5441).

// sessionZoneCtx builds the same fixture TestExtractEpochFromTimestamp uses but
// answers the session GUCs the way a real TimeZone='...' session would. The
// GetSetting override is applied AFTER newDDLFixture (which sets none).
func sessionZoneCtx(t *testing.T, zone string) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	ctx.GetSetting = func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return "ISO, MDY", true
		case "timezone":
			return zone, true
		}
		return "", false
	}
	return ctx
}

// TestExtractDatePartApplySessionTimeZone pins the core Bucket-F fix: a stored
// UTC instant extracted under TimeZone=America/Los_Angeles answers in PST
// (UTC-8 on 2001-02-16, a non-DST date). The brief's acceptance instant is
// 2001-02-16 20:38:40 UTC → hour=12, day=16 under PST. PG oracle:
// date_part('hour', '2001-02-16 20:38:40+00'::timestamptz) under
// America/Los_Angeles = 12 (timestamptz_part passes NULL zone → session zone).
// A plain timestamp (no tz) keeps its stored wall clock — no zone to apply.
func TestExtractDatePartApplySessionTimeZone(t *testing.T) {
	ctx := sessionZoneCtx(t, "America/Los_Angeles")

	cases := []struct{ sql, want string }{
		// timestamptz: extracted in the session zone (PST = UTC-8).
		{"SELECT extract(hour from timestamptz '2001-02-16 20:38:40+00')", "12"},
		{"SELECT extract(day from timestamptz '2001-02-16 20:38:40+00')", "16"},
		{"SELECT extract(year from timestamptz '2001-02-16 20:38:40+00')", "2001"},
		{"SELECT date_part('hour', timestamptz '2001-02-16 20:38:40+00')", "12"},
		{"SELECT date_part('day', timestamptz '2001-02-16 20:38:40+00')", "16"},
		{"SELECT date_part('year', timestamptz '2001-02-16 20:38:40+00')", "2001"},
		// timestamp: NOT shifted by the session zone (UTC-session identity).
		{"SELECT extract(hour from timestamp '2001-02-16 20:38:40')", "20"},
		{"SELECT date_part('hour', timestamp '2001-02-16 20:38:40')", "20"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestExtractDatePartFieldAliases pins msec/usec (aliases of milliseconds/
// microseconds) and julian. PG oracle: datetktbl maps msec→DTK_MILLISEC and
// usec→DTK_MICROSEC (datetime.c), timestamp_part_common's DTK_MILLISEC/
// DTK_MICROSEC/DTK_JULIAN arms. julian = date2j + (time-of-day)/86400 — at
// midnight the fractional part is 0, so date '2000-01-01' → date2j = 2451545.
// EXTRACT returns numeric (scale-preserved), date_part returns float8
// (trailing zeros stripped) — the split the brief forbids changing.
func TestExtractDatePartFieldAliases(t *testing.T) {
	ctx := sessionZoneCtx(t, "America/Los_Angeles")

	cases := []struct{ sql, want string }{
		// msec/usec equal milliseconds/microseconds (40s fraction-free instant).
		// EXTRACT(milliseconds) is numeric scale 3, EXTRACT(microseconds) is a
		// plain integer numeric (scale 0) — PG's DTK_MICROSEC returns intresult
		// directly (timestamp.c:5557); see timestamptz.out extract block.
		{"SELECT extract(msec from timestamptz '2001-02-16 20:38:40+00')", "40000.000"},
		{"SELECT extract(milliseconds from timestamptz '2001-02-16 20:38:40+00')", "40000.000"},
		{"SELECT extract(usec from timestamptz '2001-02-16 20:38:40+00')", "40000000"},
		{"SELECT extract(microseconds from timestamptz '2001-02-16 20:38:40+00')", "40000000"},
		// date_part float8 spelling strips trailing zeros.
		{"SELECT date_part('msec', timestamptz '2001-02-16 20:38:40+00')", "40000"},
		{"SELECT date_part('usec', timestamptz '2001-02-16 20:38:40+00')", "40000000"},
		// julian: midnight date → date2j exactly (PG 18.3 oracle:
		// extract(julian from date '2000-01-01') = 2451545).
		{"SELECT extract(julian from date '2000-01-01')", "2451545"},
		{"SELECT date_part('julian', date '2000-01-01')", "2451545"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestExtractDatePartTimezoneFields pins the real session offset for the
// timezone/timezone_hour/timezone_minute fields of a timestamptz, and the
// 22023 rejection for a zone-less timestamp. PG oracle: DTK_TZ/TZ_HOUR/TZ_MINUTE
// arms (timestamp.c:5831-5841) return the session offset (seconds east);
// PST = -28800. Go's Zone() uses the same sign convention. For timestamp/date,
// PG raises 0A000 "unit ... not supported for type timestamp without time zone";
// goopg keeps EXTRACT/date_part's pre-existing 22023 spelling (deferral).
func TestExtractDatePartTimezoneFields(t *testing.T) {
	ctx := sessionZoneCtx(t, "America/Los_Angeles")

	cases := []struct{ sql, want string }{
		{"SELECT extract(timezone from timestamptz '2001-02-16 20:38:40+00')", "-28800"},
		{"SELECT extract(timezone_hour from timestamptz '2001-02-16 20:38:40+00')", "-8"},
		{"SELECT extract(timezone_minute from timestamptz '2001-02-16 20:38:40+00')", "0"},
		{"SELECT date_part('timezone', timestamptz '2001-02-16 20:38:40+00')", "-28800"},
		{"SELECT date_part('timezone_hour', timestamptz '2001-02-16 20:38:40+00')", "-8"},
		{"SELECT date_part('timezone_minute', timestamptz '2001-02-16 20:38:40+00')", "0"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}

	// A zone-less timestamp has no offset — both spellings reject with 22023.
	_, err := runQueryErr(t, ctx, "SELECT extract(timezone from timestamp '2001-02-16 20:38:40') FROM t")
	requireExecError(t, err, "22023", `unit "timezone" not supported for type timestamp`)
	_, err = runQueryErr(t, ctx, "SELECT date_part('timezone', timestamp '2001-02-16 20:38:40') FROM t")
	requireExecError(t, err, "22023", `unit "timezone" not supported for type timestamp`)
}

// TestExtractDatePartNonFinite pins the ±infinity arms of both spellings via
// direct evaluator calls (the input literal `timestamptz 'infinity'` is bucket
// A's territory — out of scope here, so the datum is constructed via
// NewTimestampInfinity).
//
// PG oracle: NonFiniteTimestampTzPart (timestamp.c:5441-5493) returns 0.0 for
// the OSCILLATING units (microsec..doy, tz/tz_hour/tz_minute) — the caller
// turns that into NULL — and ±Infinity for the MONOTONICALLY-INCREASING units
// (year, decade, century, millennium, julian, isoyear, epoch). The regress
// expected output shows this grouping on the infinity rows of the year/
// isoyear/decade/extract blocks: year=Infinity, isoyear=Infinity, but
// month/day/hour/... blank (timestamptz.out:961-962,1110-1111,1187-1188,
// 1341-1342). The brief's "calendar fields → NULL" is the doc's simplification;
// the oracle (and the regress gate) require the monotonic-vs-oscillating split.
func TestExtractDatePartNonFinite(t *testing.T) {
	ctx := tzCtx("ISO, MDY", "America/Los_Angeles")

	extract := func(field string, d Datum) (Datum, error) {
		x := &optimizer.ExtractExpr{
			Field:          field,
			Source:         &optimizer.ColumnRef{Index: 0, Name: "ts"},
			SourceTypeName: "timestamptz",
		}
		return evalExtract(x, Row{d}, ctx)
	}
	datePart := func(field string, d Datum) (Datum, error) {
		x := &optimizer.FuncCall{
			Name: "date_part",
			Args: []optimizer.Expr{
				&optimizer.StringConst{Value: field},
				&optimizer.ColumnRef{Index: 0, Name: "ts"},
			},
		}
		return evalDatePart(x, rowSlotView(Row{d}), ctx)
	}

	// Oscillating units → NULL (both spellings, both signs).
	for _, field := range []string{
		"microseconds", "milliseconds", "msec", "usec", "second", "minute",
		"hour", "day", "month", "quarter", "week", "dow", "isodow", "doy",
		"timezone", "timezone_hour", "timezone_minute",
	} {
		for _, pos := range []bool{true, false} {
			d, err := extract(field, NewTimestampInfinity(pos))
			if err != nil {
				t.Fatalf("EXTRACT(%s FROM nonfinite): %v", field, err)
			}
			if !d.IsNull() {
				t.Errorf("EXTRACT(%s FROM nonfinite) = %v, want NULL", field, d)
			}
			d, err = datePart(field, NewTimestampInfinity(pos))
			if err != nil {
				t.Fatalf("date_part(%q, nonfinite): %v", field, err)
			}
			if !d.IsNull() {
				t.Errorf("date_part(%q, nonfinite) = %v, want NULL", field, d)
			}
		}
	}

	// Monotonically-increasing units → ±Infinity (both spellings).
	for _, tc := range []struct {
		field string
		d     Datum
		want  string
	}{
		{"year", NewTimestampInfinity(true), "Infinity"},
		{"year", NewTimestampInfinity(false), "-Infinity"},
		{"decade", NewTimestampInfinity(true), "Infinity"},
		{"century", NewTimestampInfinity(true), "Infinity"},
		{"millennium", NewTimestampInfinity(true), "Infinity"},
		{"isoyear", NewTimestampInfinity(true), "Infinity"},
		{"epoch", NewTimestampInfinity(true), "Infinity"},
		{"epoch", NewTimestampInfinity(false), "-Infinity"},
		{"julian", NewTimestampInfinity(true), "Infinity"},
		{"julian", NewTimestampInfinity(false), "-Infinity"},
	} {
		d, err := extract(tc.field, tc.d)
		if err != nil {
			t.Fatalf("EXTRACT(%s FROM nonfinite): %v", tc.field, err)
		}
		if got := d.Format(); got != tc.want {
			t.Errorf("EXTRACT(%s FROM nonfinite) = %q, want %q", tc.field, got, tc.want)
		}
		d, err = datePart(tc.field, tc.d)
		if err != nil {
			t.Fatalf("date_part(%q, nonfinite): %v", tc.field, err)
		}
		if got := d.Format(); got != tc.want {
			t.Errorf("date_part(%q, nonfinite) = %q, want %q", tc.field, got, tc.want)
		}
	}
}
