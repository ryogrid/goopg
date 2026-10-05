package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainNamesThroughUnpulledSubquery pins M0146-0042 against PG 18.3.
// A grouped FROM subquery is not pulled up and its Subquery Scan is elided,
// so its relations reuse the parent level's SourceTableIdx values and its
// outputs carry a binding no scan owns. PG still qualifies every column:
// the parent's `cu.c_ck` (goopg met the subquery's dd first and printed
// `c_ck`), a sort key through the subquery to `st.city` (TPC-DS Q79), and a
// join residual through an INTERSECT to the Subquery Scan PG keeps,
// `a1.z` (TPC-DS Q8). Each want line is PG's output for the statement.
func TestExplainNamesThroughUnpulledSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table ss (tk int, ck int, sk int, dk int, np numeric)",
		"create table dd (dk int primary key, dow int)",
		"create table st (sk int primary key, city varchar(60))",
		"create table cu (c_ck int primary key, ln text)",
		"insert into ss select g, g % 100, g % 10, g % 50, g from generate_series(1,5000) g",
		"insert into dd select g, g % 7 from generate_series(0,49) g",
		"insert into st select g, 'c' || g from generate_series(0,9) g",
		"insert into cu select g, 'n' || g from generate_series(0,99) g",
		"analyze ss", "analyze dd", "analyze st", "analyze cu",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"select ln, substr(city,1,30), profit from (select tk, ck, st.city, sum(np) profit from ss, dd, st " +
			"where ss.dk = dd.dk and ss.sk = st.sk and dd.dow = 1 group by tk, ck, st.city) ms, cu " +
			"where ck = c_ck order by ln, substr(city,1,30), profit",
			[]string{"Sort Key: cu.ln, (substr((st.city)::text, 1, 30)), (sum(ss.np))", "Hash Cond: (ss.ck = cu.c_ck)"}},
		{"select st.sk from st, (select z from (select city z, count(*) c from st s2 where sk < 5 group by city " +
			"having count(*) > 0) a1 intersect select city from st s3 where sk > 2) v1 " +
			"where substr(st.city,1,2) < substr(v1.z,1,2)",
			[]string{"Join Filter: (substr((st.city)::text, 1, 2) < substr((a1.z)::text, 1, 2))"}},
	} {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.query, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		for _, w := range c.want {
			found := false
			for _, l := range lines {
				found = found || l == w
			}
			if !found {
				t.Errorf("%s\nwant PG's line %q in:\n%s", c.query, w, strings.Join(lines, "\n"))
			}
		}
	}
	// A column inside an aggregate's argument indexes the aggregate's input,
	// not the Sort's: the sort key's positional walk must not rename `v`
	// after the group key (TPC-DS Q71 printed `sum(time_dim.t_hour)`). PG
	// prints `sum(ss.np)` here, through the appendrel; goopg keeps `sum(v)`.
	q := "select city, sum(v) from (select np v, sk from ss where tk < 100 union all select np, sk from ss " +
		"where tk > 4900) u, st where u.sk = st.sk group by city order by sum(v) desc, city"
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if l := r[0].StringValue(); strings.Contains(l, "sum(st.") {
			t.Errorf("%s\nsort key names the group key inside the aggregate: %s", q, l)
		}
	}
}
