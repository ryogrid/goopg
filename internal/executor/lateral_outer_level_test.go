package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestJoinLateralRefersToEarlierFromItem pins M0146-0032 against PG 18.3
// (regress join.sql's `int8_tbl a, int8_tbl x left join lateral (select
// a.q1 from int4_tbl y) ss(z) on x.q2 = ss.z`): a JOIN LATERAL right side
// sees the join's left input AND the comma items before it, at two levels —
// the join pushes only its left row, the earlier items' row comes from the
// enclosing comma join. goopg flattened both into one level, so `a.q1`
// read position 0 of x's row (x.q1). PG: 57 rows, 40 with a non-NULL z,
// and every z equals a.q1.
func TestJoinLateralRefersToEarlierFromItem(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE i8 (q1 int8, q2 int8)")
	runSQL(t, ctx, "INSERT INTO i8 VALUES (123,456),(123,4567890123456789),(4567890123456789,123),(4567890123456789,4567890123456789),(4567890123456789,-4567890123456789)")
	runSQL(t, ctx, "CREATE TABLE i4 (f1 int4)")
	runSQL(t, ctx, "INSERT INTO i4 VALUES (0),(123456),(-123456),(2147483647),(-2147483647)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0

	rows := drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"SELECT a.q1, x.q2, ss.z FROM i8 a, i8 x LEFT JOIN LATERAL (SELECT a.q1 FROM i4 y) ss(z) ON x.q2 = ss.z", ps))
	nonNull, mismatched := 0, 0
	for _, r := range rows {
		if r[2].IsNull() {
			continue
		}
		nonNull++
		if r[2].Format() != r[0].Format() || r[1].Format() != r[0].Format() {
			mismatched++
		}
	}
	if len(rows) != 57 || nonNull != 40 || mismatched != 0 {
		t.Errorf("rows=%d non-null z=%d z≠a.q1=%d; PG: 57 rows, 40 non-null, 0 mismatched", len(rows), nonNull, mismatched)
	}

	// Both levels in one lateral expression.
	rows = drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"SELECT a.q1, x.q1, s.v FROM i8 a, i8 x LEFT JOIN LATERAL (SELECT a.q1 + x.q1 AS v) s ON true WHERE a.q2 = 456 ORDER BY 1, 2, 3", ps))
	var got []string
	for _, r := range rows {
		got = append(got, r[0].Format()+":"+r[1].Format()+":"+r[2].Format())
	}
	want := "123:123:246,123:123:246,123:4567890123456789:4567890123456912,123:4567890123456789:4567890123456912,123:4567890123456789:4567890123456912"
	if strings.Join(got, ",") != want {
		t.Errorf("two-level lateral expression:\n got %s\nwant %s", strings.Join(got, ","), want)
	}
}
