package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestDateIntegerArithmeticMatchesPG pins M0146-0040 against PG 18.3:
// date ± integer is date_pli / date_mii (a date), integer + date is
// integer_pl_date, and date − date is date_mi (an integer day count). goopg
// returned a timestamp-formatted value typed `unknown` for a date column,
// raised "requires numeric operands" for `-`, failed `'…'::date + 30`
// outright, and gave date − date as an interval. A literal operation folds
// to the date Const PG's eval_const_expressions produces. Want values are
// PG's own output.
func TestDateIntegerArithmeticMatchesPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE dd (d date, n int, s int2)")
	runSQL(t, ctx, "INSERT INTO dd VALUES ('2001-07-15', 30, 2)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "SELECT d + 30, pg_typeof(d + 30), d - 30, 30 + d, d + n, n + d, d - n, d + s, " +
		"d - '2001-07-01'::date, pg_typeof(d - '2001-07-01'::date), " +
		"'2001-07-15'::date + 30, '2001-07-15'::date - 30, 7 + '2001-07-15'::date, " +
		"'2001-07-15'::date - '2001-07-01'::date, cast('2001-07-15' as date) + 1 FROM dd"
	want := []string{"2001-08-14", "date", "2001-06-15", "2001-08-14", "2001-08-14", "2001-08-14",
		"2001-06-15", "2001-07-17", "14", "integer",
		"2001-08-14", "2001-06-15", "2001-07-22", "14", "2001-07-16"}
	rows := drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps))
	if len(rows) != 1 || len(rows[0]) != len(want) {
		t.Fatalf("got %d rows / %v", len(rows), rows)
	}
	// Compare meaning, not Format()'s text: a date datum renders in the
	// regress DateStyle (MDY) in-process and in ISO on the wire, and
	// pg_typeof may surface as the type OID before the wire layer names it.
	typeOID := map[string]string{"date": "1082", "integer": "23"}
	for i, w := range want {
		d := rows[0][i]
		got := d.Format()
		switch {
		case d.IsDate():
			if got = d.TimeValue().Format("2006-01-02"); got != w {
				t.Errorf("column %d: date %q, want %q", i+1, got, w)
			}
		case got == w || got == typeOID[w]:
		default:
			if strings.Count(w, "-") == 2 {
				t.Errorf("column %d: got %q (not a date datum), want date %q", i+1, got, w)
			} else {
				t.Errorf("column %d: got %q, want %q", i+1, got, w)
			}
		}
	}

	// A date literal ± integer in a qual folds to PG's date Const.
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT * FROM dd WHERE d <= '2001-07-15'::date + 30", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Filter: (d <= '2001-08-14'::date)") {
		t.Errorf("EXPLAIN lacks the folded date Const:\n%s", strings.Join(lines, "\n"))
	}
}
