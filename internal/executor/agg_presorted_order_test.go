package executor

// M0148-0001j — transition call order of DISTINCT / ORDER BY user aggregates.
// A presorted aggregate (PG's aggpresorted) runs its transition per input row,
// interleaved with the group's plain aggregates and skipping DISTINCT repeats,
// as nodeAgg does; an ORDER BY aggregate does not share its state with a plain
// call of the same sfunc (find_compatible_trans).

import (
	"strings"
	"testing"
)

// sfuncCalls returns the ua_accum NOTICE arguments raised since index from.
func sfuncCalls(ctx *Context, from int) []string {
	var out []string
	for _, m := range ctx.Notices[from:] {
		if i := strings.Index(m, "ua_accum "); i >= 0 {
			out = append(out, strings.TrimSpace(m[i+len("ua_accum "):]))
		}
	}
	return out
}

func TestPresortedUserAggTransitionOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sql   string
		calls string
		want  [2]int64
	}{
		{
			// aggregates.sql: the DISTINCT input is sorted below the
			// aggregate, so both aggregates transition per row: 1,1,3,3 —
			// not the DISTINCT aggregate's calls after all of the plain
			// one's (1,3,1,3).
			name:  "DistinctThenPlain",
			sql:   `SELECT ua_sum(DISTINCT v), ua_sum(v) FROM (VALUES (1::bigint), (3)) t(v)`,
			calls: "1,1,3,3",
			want:  [2]int64{4, 4},
		},
		{
			// A DISTINCT repeat makes no transition call; the plain
			// aggregate still sees it.
			name:  "DistinctRepeatSkipped",
			sql:   `SELECT ua_sum(v), ua_sum(DISTINCT v) FROM (VALUES (1::bigint), (3), (1)) t(v)`,
			calls: "1,1,1,3,3",
			want:  [2]int64{5, 4},
		},
		{
			// Unequal ORDER BY clauses never share a transition state.
			name:  "OrderByDoesNotShareWithPlain",
			sql:   `SELECT ua_sum(v ORDER BY v DESC), ua_sum(v) FROM (VALUES (1::bigint), (3)) t(v)`,
			calls: "3,3,1,1",
			want:  [2]int64{4, 4},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newUserAggFixture(t)
			defer cleanup()
			from := len(ctx.Notices)
			rows := runQuery(t, ctx, tc.sql)
			if len(rows) != 1 || len(rows[0]) < 2 {
				t.Fatalf("got %v, want one row of two columns", rows)
			}
			if got := strings.Join(sfuncCalls(ctx, from), ","); got != tc.calls {
				t.Errorf("sfunc calls %s, want %s", got, tc.calls)
			}
			for i := 0; i < 2; i++ {
				if got, _ := datumInt64(rows[0][i]); got != tc.want[i] {
					t.Errorf("column %d = %d, want %d", i, got, tc.want[i])
				}
			}
		})
	}
}
