package executor

import (
	"sort"
	"strings"
	"testing"
)

// M0146-0005bn: PG's subquery_push_qual moves an outer restriction on a
// grouped subquery's grouping column into the subquery before planning it,
// and the subquery's HAVING preprocessing moves it on to WHERE — so the
// qual filters at the scan (TPC-DS Q78's `ss_sold_year = 1998` reaching
// date_dim). PG 18.3 for the derived-table case below:
//
//	GroupAggregate / HashAggregate  Group Key: k
//	  ->  Seq Scan on g  Filter: (y = 1998)
func subqueryPushFixture(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, sql := range []string{
		"CREATE TABLE g (y int, k int, v int)",
		"CREATE TABLE p (y int, k int)",
		"INSERT INTO g VALUES (1998, 1, 10), (1998, 1, 5), (1999, 1, 7), (1998, 2, 3), (1999, 3, 4)",
		"INSERT INTO p VALUES (1998, 1), (1998, 2), (1999, 3), (1998, 4)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	return ctx
}

func TestSubqueryPushQualReachesTheScan(t *testing.T) {
	ctx := subqueryPushFixture(t)
	for _, sql := range []string{
		`SELECT * FROM (SELECT y AS yr, k, sum(v) s FROM g GROUP BY y, k) d WHERE yr = 1998`,
		`WITH d AS (SELECT y AS yr, k, sum(v) s FROM g GROUP BY y, k) SELECT * FROM d WHERE yr = 1998`,
	} {
		lines := runExplain(t, ctx, sql)
		joined := strings.Join(lines, "\n")
		scan := -1
		for i, l := range lines {
			if strings.Contains(l, "Seq Scan on g") {
				scan = i
			}
		}
		if scan < 0 || scan+1 >= len(lines) || !strings.Contains(lines[scan+1], "Filter: (y = 1998)") {
			t.Errorf("%s\nwant the pushed qual at the scan:\n%s", sql, joined)
		}
		if strings.Count(joined, "1998") != 1 {
			t.Errorf("%s\nwant the qual moved, not copied:\n%s", sql, joined)
		}
	}
}

// The constant follows `p.y = d.yr` into the LEFT join's nullable side
// (reconsider_outer_join_clauses): each grouped item filters at its scan.
func TestSubqueryPushQualFollowsLeftJoinEquality(t *testing.T) {
	ctx := subqueryPushFixture(t)
	sql := `SELECT a.yr, a.k, a.s, b.s FROM
	          (SELECT y AS yr, k, sum(v) s FROM g GROUP BY y, k) a
	          LEFT JOIN (SELECT y AS yr, k, count(*) s FROM p GROUP BY y, k) b
	            ON b.yr = a.yr AND b.k = a.k
	        WHERE a.yr = 1998`
	lines := runExplain(t, ctx, sql)
	joined := strings.Join(lines, "\n")
	if n := strings.Count(joined, "Filter: (y = 1998)"); n != 2 {
		t.Errorf("want the constant at both scans, got %d:\n%s", n, joined)
	}
	got := formatRows(runQueryRows(t, ctx, sql))
	sort.Strings(got)
	want := []string{"1998,1,15,1", "1998,2,3,1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// A WHERE qual on a LEFT join's nullable side is not a restriction of that
// rel alone: pushing it into the subquery would keep the null-extended
// rows it removes. It must stay above the join.
func TestSubqueryPushQualSkipsNullableSide(t *testing.T) {
	ctx := subqueryPushFixture(t)
	sql := `SELECT p.k, b.s FROM p
	          LEFT JOIN (SELECT y AS yr, k, sum(v) s FROM g GROUP BY y, k) b ON b.k = p.k
	        WHERE b.yr = 1999`
	got := formatRows(runQueryRows(t, ctx, sql))
	sort.Strings(got)
	want := []string{"1,7", "3,4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
