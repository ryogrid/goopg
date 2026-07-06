package executor

import "testing"

// TestIntervalOrderingOperators pins compareDatum's new KindInterval case
// (M0122-0004): before this fix, `<`/`>`/`<=`/`>=`/ORDER BY/MIN/MAX all
// raised 42883 ("comparison not supported for kind ...") for interval
// values, since compareDatum had no case for KindInterval and fell through
// to the generic error. Verified against real PostgreSQL 18.3: interval
// comparison converts months to days at a fixed 30-day rate before
// comparing (interval_cmp_value, timestamp.c) — e.g. `interval '1' year`
// (12 months = 360 "days") is greater than `interval '90' day`.
func TestIntervalOrderingOperators(t *testing.T) {
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
		{"SELECT interval '90' day < interval '1' year FROM t", true},   // 90 < 360
		{"SELECT interval '1' year > interval '90' day FROM t", true},   // 360 > 90
		{"SELECT interval '3' month = interval '90' day FROM t", true},  // 90 == 90
		{"SELECT interval '3' month <= interval '89' day FROM t", false},
		{"SELECT interval '1' year >= interval '12' month FROM t", true}, // 360 == 360
		{"SELECT interval '-3' month < interval '0' month FROM t", true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			if got := scalarBool(c.sql); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestIntervalOrderByAndMinMax pins ORDER BY and MIN/MAX aggregate support
// over interval-valued expressions, which route through the same
// compareDatum fix as the plain ordering operators above.
func TestIntervalOrderByAndMinMax(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	mkQuery := func(agg string) string {
		return "SELECT " + agg + " FROM (" +
			"SELECT interval '3' month AS iv " +
			"UNION ALL SELECT interval '1' year " +
			"UNION ALL SELECT interval '90' day" +
			") s"
	}

	orderRows := runQuery(t, ctx, mkQuery("iv")+" ORDER BY iv")
	if len(orderRows) != 3 {
		t.Fatalf("ORDER BY: got %d rows, want 3", len(orderRows))
	}
	// Ascending by 30-day-month linearization: 3 month(90) == 90 day(90) < 1 year(360).
	// The two 90-"day" equivalents (3 month, 90 day) may appear in either
	// relative order (equal keys); the 1-year row must sort last.
	last := orderRows[2][0]
	if last.IntervalMonthsValue() != 12 || last.IntervalDaysValue() != 0 {
		t.Fatalf("last row = %d months %d days, want 12 months 0 days (1 year)",
			last.IntervalMonthsValue(), last.IntervalDaysValue())
	}
	for _, r := range orderRows[:2] {
		months, days := r[0].IntervalMonthsValue(), r[0].IntervalDaysValue()
		total := int64(months)*30 + int64(days)
		if total != 90 {
			t.Fatalf("row = %d months %d days (total %d), want total 90", months, days, total)
		}
	}

	maxRows := runQuery(t, ctx, mkQuery("max(iv)"))
	if len(maxRows) != 1 {
		t.Fatalf("MAX: got %d rows, want 1", len(maxRows))
	}
	if got := maxRows[0][0]; got.IntervalMonthsValue() != 12 || got.IntervalDaysValue() != 0 {
		t.Fatalf("max(iv) = %d months %d days, want 12 months 0 days (1 year)",
			got.IntervalMonthsValue(), got.IntervalDaysValue())
	}

	minRows := runQuery(t, ctx, mkQuery("min(iv)"))
	if len(minRows) != 1 {
		t.Fatalf("MIN: got %d rows, want 1", len(minRows))
	}
	if got := minRows[0][0]; int64(got.IntervalMonthsValue())*30+int64(got.IntervalDaysValue()) != 90 {
		t.Fatalf("min(iv) = %d months %d days, want total 90", got.IntervalMonthsValue(), got.IntervalDaysValue())
	}
}
