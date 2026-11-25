package executor

import "testing"

// TestIntervalSumAvg pins M0149-0001h1a79 against PG 18.3's interval_sum /
// interval_avg (timestamp.c): finite inputs add with overflow checking, avg
// divides by the finite count (interval_div), an infinity of one sign wins,
// and both signs together are "interval out of range" (22008). goopg used to
// reject the input with "aggregate sum requires numeric argument".
func TestIntervalSumAvg(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for sql, want := range map[string][2]string{
		"SELECT sum(i), avg(i) FROM (VALUES ('1 hour'::interval),('2 hours')) v(i)":             {"03:00:00", "01:30:00"},
		"SELECT sum(i), avg(i) FROM (VALUES ('1 mon'::interval),('1 day 3 hours'),(null)) v(i)": {"1 mon 1 day 03:00:00", "15 days 13:30:00"},
		"SELECT sum(i), avg(i) FROM (VALUES ('infinity'::interval),('1 day')) v(i)":             {"infinity", "infinity"},
		"SELECT sum(i), avg(i) FROM (VALUES ('-infinity'::interval),('-infinity')) v(i)":        {"-infinity", "-infinity"},
		"SELECT sum(i), avg(i) FROM (SELECT '1 hour'::interval AS i WHERE false) v":             {"NULL", "NULL"},
	} {
		rows := runQuery(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", sql, len(rows))
		}
		for c := 0; c < 2; c++ {
			got := "NULL"
			if !rows[0][c].IsNull() {
				got = rows[0][c].Format()
			}
			if got != want[c] {
				t.Errorf("%s column %d = %q, want %q", sql, c+1, got, want[c])
			}
		}
	}
	_, err := runQueryErr(t, ctx, "SELECT sum(i) FROM (VALUES ('infinity'::interval),('-infinity')) v(i)")
	if code := execErrCode(err); code != "22008" {
		t.Errorf("sum(+inf, -inf) err = %v (%s), want 22008 interval out of range", err, code)
	}
}
