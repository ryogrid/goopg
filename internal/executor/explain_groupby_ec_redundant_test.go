package executor

import (
	"strings"
	"testing"
)

// M0148-0001d: standard_qp_callback builds the group pathkeys with
// remove_redundant = true, so a GROUP BY item whose equivalence class is
// already in the list — equated to an earlier key by a WHERE or inner-join
// ON `col = col` — is dropped from processed_groupClause
// (pathkey_is_redundant). aggregates.out:1211-1236 groups `t1 join t2 on
// t1.a = t2.x and t1.b = t2.y` by `t1.a, t1.b` alone. An outer join's ON
// clause merges no class, so a LEFT JOIN keeps both keys. Shapes verified
// against PG 18.3.
func TestGroupByKeysRedundantByEquivalence(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table gt1 (a int, b int, c int, d int, primary key (a, b))",
		"create table gt2 (x int, y int, z int, primary key (x, y))",
		"insert into gt1 values (1,1,1,1),(1,2,2,2),(2,1,3,3)",
		"insert into gt2 values (1,1,9),(1,2,8),(2,1,7),(3,3,6)",
	} {
		runSQL(t, ctx, q)
	}
	groupKey := func(q string) string {
		for _, l := range renderRows(runSQL(t, ctx, "explain (costs off) "+q)) {
			if s := strings.TrimSpace(l); strings.HasPrefix(s, "Group Key:") {
				return s
			}
		}
		return "(no Group Key)"
	}
	for _, tc := range []struct{ q, want string }{
		{"select * from gt1 inner join gt2 on gt1.a = gt2.x and gt1.b = gt2.y group by gt1.a,gt1.b,gt1.c,gt1.d,gt2.x,gt2.y,gt2.z",
			"Group Key: gt1.a, gt1.b"},
		{"select gt1.*,gt2.x,gt2.z from gt1 inner join gt2 on gt1.a = gt2.x and gt1.b = gt2.y group by gt1.a,gt1.b,gt1.c,gt1.d,gt2.x,gt2.z",
			"Group Key: gt1.a, gt1.b, gt2.z"},
		{"select gt1.c, gt2.z, count(*) from gt1 join gt2 on gt1.c = gt2.z group by gt2.z, gt1.c",
			"Group Key: gt2.z"},
		{"select gt1.c, gt2.z, count(*) from gt1 left join gt2 on gt1.c = gt2.z group by gt1.c, gt2.z",
			"Group Key: gt1.c, gt2.z"},
		{"select c, d, count(*) from gt1 where c = d group by c, d", "Group Key: c"},
		{"select c, d, count(*) from gt1 where c = d and d = 2 group by c, d", "(no Group Key)"},
	} {
		if got := groupKey(tc.q); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q (PG 18.3)", tc.q, got, tc.want)
		}
	}
	// The dropped key still prints its own value: every row of the group
	// carries the value its class representative carries.
	for _, tc := range []struct{ q, want string }{
		{"select gt1.*,gt2.x,gt2.z from gt1 inner join gt2 on gt1.a = gt2.x and gt1.b = gt2.y group by gt1.a,gt1.b,gt1.c,gt1.d,gt2.x,gt2.z order by 1,2",
			"1|1|1|1|1|9;1|2|2|2|1|8;2|1|3|3|2|7"},
		{"select c, d, count(*) from gt1 where c = d group by c, d order by 1", "1|1|1;2|2|1;3|3|1"},
		{"select c, d, count(*) from gt1 where c = d and d = 7 group by c, d", ""},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.q, got, tc.want)
		}
	}
}
