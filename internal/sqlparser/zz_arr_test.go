package sqlparser

import "testing"

func TestArrayIntervalForms(t *testing.T) {
	for _, q := range []string{
		"SELECT ARRAY[1,2,3]",
		"SELECT ARRAY[1,2,3][2]",
		"SELECT arr[1:2] FROM t",
		"SELECT arr[:3], x[4:] FROM t",
		"SELECT interval '90' day",
		"SELECT interval '1' hour to minute",
		"SELECT interval '5' second(2)",
		"SELECT interval '90' days FROM t",
		"SELECT sum(i) OVER (ORDER BY i ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING) FROM t",
		"SELECT avg(x) OVER (ORDER BY y RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t",
		"SELECT count(*) OVER (ROWS UNBOUNDED PRECEDING) FROM t",
		"SELECT sum(i) OVER w FROM t WINDOW w AS (ORDER BY i ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE TIES)",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
			continue
		}
		if l != n {
			t.Errorf("DIFF %q\n  L=%s\n  N=%s", q, l, n)
		}
	}
}
