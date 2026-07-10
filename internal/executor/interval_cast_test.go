package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runQueryErr mirrors runQuery but returns the first error encountered
// (parse/plan/build/open/drain) instead of failing the test, so callers can
// assert on the specific error produced by an intentionally-invalid query.
func runQueryErr(t *testing.T, ctx *Context, sql string) ([]Row, error) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return nil, err
	}
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		return nil, err
	}
	rows, err := drainScan(op)
	_ = op.Close()
	return rows, err
}

// TestIntervalCastFromString pins the `::interval` / `CAST(... AS interval)`
// runtime cast path (M0122-0004): before this fix, evalCast had no "interval"
// case, so a string cast silently fell through to the generic pass-through
// and stayed a KindString instead of becoming a real interval value (e.g.
// arithmetic against it would misbehave instead of erroring or computing
// correctly). The accepted grammar mirrors the existing `INTERVAL '<n>
// <unit>'` typed-literal syntax (day/month/year only — sub-day and
// multi-component interval strings remain a documented v0 scope limit).
func TestIntervalCastFromString(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	scalarBool := func(sql string) bool {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", sql, len(rows))
		}
		return rows[0][0].BoolValue()
	}

	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT '3 days'::interval = interval '3' day FROM t", true},
		{"SELECT '1 year'::interval = interval '1' year FROM t", true},
		{"SELECT '3 month'::interval = interval '3' month FROM t", true},
		{"SELECT '3 months'::interval = interval '3' month FROM t", true},
		{"SELECT '-1 day'::interval < interval '0' day FROM t", true},
		{"SELECT CAST('90 day' AS interval) = interval '1' year FROM t", false},
		{"SELECT CAST('90 day' AS interval) < interval '1' year FROM t", true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			if got := scalarBool(c.sql); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestIntervalCastFromStringInvalidSyntax pins the 22007 error PostgreSQL's
// interval_in raises for a string that isn't a valid interval body. The cast
// path now accepts multi-field bodies, HH:MM:SS times (unimplemented_feat
// #5(b)), and a trailing unitless number defaulting to seconds (#5(d-i)), so
// only genuinely malformed bodies (unsupported unit, garbage, a non-final
// number with no unit) must still error instead of silently passing the raw
// string through unparsed.
func TestIntervalCastFromStringInvalidSyntax(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []string{
		"SELECT 'garbage'::interval FROM t",
		"SELECT '1 fortnight'::interval FROM t",
		"SELECT '1 year 2 fortnights'::interval FROM t",
		"SELECT 'day month'::interval FROM t",
		// A trailing unitless number defaults to seconds (#5(d-i)), but a
		// non-final bare number is the ambiguous type-carry case PG rejects,
		// and a bare number after a time word / seconds unit collides on the
		// SECOND field mask. All three error in PostgreSQL 18.3.
		"SELECT '1 2 days'::interval FROM t",
		"SELECT '1 day 05:00:00 5'::interval FROM t",
		"SELECT '5 5'::interval FROM t",
		// Year-month hyphen field bounds (PG DecodeInterval DTK_NUMBER hyphen
		// branch): the month part must be 0 ≤ m < 12 with nothing trailing.
		"SELECT '1-12'::interval FROM t",  // month == MONTHS_PER_YEAR (out of range)
		"SELECT '1-13'::interval FROM t",  // month > MONTHS_PER_YEAR
		"SELECT '1--2'::interval FROM t",  // negative month part
		"SELECT '1-2-3'::interval FROM t", // trailing "-3" after the month part
		"SELECT '1-2x'::interval FROM t",  // trailing non-digit after the month part
		// quarter/qtr and the timezone tokens appear in PG's deltatktbl but have
		// no case in DecodeInterval's per-unit switch, so they raise
		// DTERR_BAD_FORMAT (22007) rather than decoding (unimplemented_feat
		// #5(d-ii)); goopg's canonicalIntervalUnit must reject them too.
		"SELECT '1 qtr'::interval FROM t",
		"SELECT '1 quarter'::interval FROM t",
		"SELECT '1 tz'::interval FROM t",
		"SELECT '1 timezone'::interval FROM t",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			_, err := runQueryErr(t, ctx, sql)
			if err == nil {
				t.Fatalf("%s: expected error, got none", sql)
			}
			if execErr, ok := err.(*ExecError); ok {
				if execErr.Code != "22007" {
					t.Errorf("%s: got SQLSTATE %s, want 22007", sql, execErr.Code)
				}
			} else {
				t.Errorf("%s: expected *ExecError, got %T: %v", sql, err, err)
			}
		})
	}
}
