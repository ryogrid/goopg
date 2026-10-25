package executor

import (
	"fmt"
	"strings"
	"testing"
)

// TestScanQualLeadMatchesWholeQual pins M0122-0012 slice 86: a seq scan whose
// absorbed qual is an AND decodes only its leading conjunct's columns first
// and rejects the row when that conjunct is FALSE (scanQualLead). Every
// result, and every error, must be the one the qual gives evaluated whole:
// with the lead stage off (scanSkipDisabled) and with the early path off
// altogether (the late position, the deleted filterOp's). The quals cover a
// lead that rejects most rows ahead of numeric / float / text columns, a
// lead that is NULL for some rows, a lead that errors, and a later conjunct
// that would error on exactly the rows the lead rejects. Tail poison is armed.
func TestScanQualLeadMatchesWholeQual(t *testing.T) {
	ctx := scanQualLeadFixture(t)
	for _, q := range []string{
		"SELECT i4, n, last FROM ql WHERE i2 >= 5 AND i2 <= 60 AND (n > 100000000000000000000000000000000 OR f8 < 100 OR last % 3 = 0)",
		"SELECT tx, n FROM ql WHERE tx < 'text-5' AND last > 100",
		"SELECT i8, last FROM ql WHERE i2 < 100 AND (i8 > 0 AND n > 0)",
		"SELECT d, last FROM ql WHERE i2 < 4990 AND n / (i2 - 4995) < 0",
		"SELECT last FROM ql WHERE i4 / (last - 1000) > 0 AND n > 0",
	} {
		plan, err := testPlanDeform(t, ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		op := mustBuildDeform(t, plan)
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		scans := seqScans(op)
		armed := len(scans) == 1 && scans[0].lead.idx != noExpr
		_ = op.Close()
		if !armed {
			t.Fatalf("%s: the scan plans no lead stage; nothing is exercised", q)
		}
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		got := leadResult(t, ctx, q)
		seqScanDeformPoison = old
		scanSkipDisabled = true
		whole := leadResult(t, ctx, q)
		scanSkipDisabled = false
		scanPrefilterOn.Store(false)
		late := leadResult(t, ctx, q)
		scanPrefilterOn.Store(true)
		if got != whole || got != late {
			t.Fatalf("%s:\nlead  %.200s\nwhole %.200s\nlate  %.200s", q, got, whole, late)
		}
		if got == "rows:" {
			t.Fatalf("%s: no rows", q)
		}
	}
}

// TestScanQualLeadProbeSwitchesOff pins the stage's probe: a lead that
// rejects under a quarter of the first scanLeadProbeRows rows is switched off
// for the rest of the scan, and a selective one stays on. Either way the rows
// are the whole qual's.
func TestScanQualLeadProbeSwitchesOff(t *testing.T) {
	ctx := scanQualLeadFixture(t)
	for _, c := range []struct {
		q      string
		stayOn bool
	}{
		{"SELECT last FROM ql WHERE last > 0 AND n > 0", false},
		{"SELECT last FROM ql WHERE i2 < 500 AND n > 0", true},
	} {
		plan, err := testPlanDeform(t, ctx, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		op := mustBuildDeform(t, plan)
		rows, err := Run(op, ctx)
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		scans := seqScans(op)
		if len(scans) != 1 {
			t.Fatalf("%s: %d seq scans", c.q, len(scans))
		}
		if l := scans[0].lead; (l.idx != noExpr) != c.stayOn || l.seen < scanLeadProbeRows {
			t.Fatalf("%s: lead on=%v after %d rows (%d rejected), want on=%v", c.q, l.idx != noExpr, l.seen, l.rejected, c.stayOn)
		}
		scanSkipDisabled = true
		whole := leadResult(t, ctx, c.q)
		scanSkipDisabled = false
		if got := "rows:" + strings.Join(sortedCells(rows), "|"); got != whole {
			t.Fatalf("%s: lead %d rows, whole qual %.100s", c.q, len(rows), whole)
		}
	}
}

func scanQualLeadFixture(t *testing.T) *Context {
	t.Helper()
	ctx := deformW8Fixture(t)
	// pad keeps every qual short of the last column: a qual reaching it gets
	// no prefilter, so no lead stage either.
	runSQL(t, ctx, `CREATE TABLE ql (
		i2 int2, i4 int4, i8 int8, n numeric, tx text, vc varchar(10), ch char(4),
		d date, ts timestamp, f8 float8, big text, last int, pad int)`)
	runSQL(t, ctx, `INSERT INTO ql SELECT
		x::int2, x * 7, x::int8 * 100000000000, x::numeric * 123456789012345678901234567890,
		CASE WHEN x % 5 = 0 THEN NULL ELSE 'text-' || x END, 'v' || (x % 100), 'c' || (x % 10),
		('2020-01-' || (1 + x % 28))::date, ('2020-01-01 ' || (x % 24) || ':00:00')::timestamp, x / 7.0,
		CASE WHEN x % 3 = 0 THEN repeat('compressible-', 400) ELSE 'short' || x END, x, x % 11
		FROM generate_series(1, 7000) x`)
	return ctx
}

// leadResult is the query's sorted rows, or its error, as one string.
func leadResult(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	plan, err := testPlanDeform(t, ctx, sql)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	rows, err := Run(mustBuildDeform(t, plan), ctx)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "rows:" + strings.Join(sortedCells(rows), "|")
}
