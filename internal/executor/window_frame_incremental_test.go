package executor

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// TestWindowFrameIncrementalMatchesRecompute pins M0122-0012 slice 62:
// evalExplicitFrameAggFuncs keeps a frame aggregate's running state while the
// frame head stays put (PG's eval_windowaggregates) instead of re-aggregating
// every row's frame from its start. Every query here must return exactly
// what the from-scratch path (frameAggIncremental off) returns, for sum /
// count / avg / min / max over int, numeric (small and beyond 64 bits) and
// float8 values with NULLs and a FILTER, in frames whose head is fixed
// (UNBOUNDED PRECEDING, ROWS and GROUPS, tails at CURRENT ROW / k FOLLOWING /
// UNBOUNDED FOLLOWING), whose head slides (restarts), with an EXCLUDE clause
// (always restarts), and over RANGE frames.
func TestWindowFrameIncrementalMatchesRecompute(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE wf (grp int, ord int, i int, n numeric, f float8)"); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(62))
	var vals []string
	for g := 0; g < 4; g++ {
		for o := 0; o < 25; o++ {
			iv, nv, fv := fmt.Sprint(rng.Intn(9)-4), fmt.Sprintf("%d.%02d", rng.Intn(50)-25, rng.Intn(100)), fmt.Sprintf("%g", rng.Float64()*10-5)
			if rng.Intn(6) == 0 {
				iv = "NULL"
			}
			if rng.Intn(6) == 0 {
				nv = "NULL"
			}
			if rng.Intn(9) == 0 {
				nv = "123456789012345678901234567890.5"
			}
			if rng.Intn(6) == 0 {
				fv = "NULL"
			}
			vals = append(vals, fmt.Sprintf("(%d, %d, %s, %s, %s)", g, o/2, iv, nv, fv))
		}
	}
	if err := runDDL(t, ctx, "INSERT INTO wf VALUES "+strings.Join(vals, ", ")); err != nil {
		t.Fatal(err)
	}
	frames := []string{
		"ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW",
		"ROWS BETWEEN UNBOUNDED PRECEDING AND 2 FOLLOWING",
		"ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING",
		"ROWS BETWEEN 2 PRECEDING AND CURRENT ROW",
		"ROWS BETWEEN 1 FOLLOWING AND 3 FOLLOWING",
		"GROUPS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW",
		"GROUPS BETWEEN UNBOUNDED PRECEDING AND 1 FOLLOWING",
		"RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW",
		"RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING",
		"ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW",
	}
	aggs := []string{"sum(i)", "sum(n)", "sum(f)", "count(*)", "count(n)", "avg(i)", "avg(n)", "min(f)", "max(n)",
		"sum(n) FILTER (WHERE i > 0)"}
	for _, fr := range frames {
		var cols []string
		for _, a := range aggs {
			cols = append(cols, fmt.Sprintf("%s OVER (PARTITION BY grp ORDER BY ord %s)", a, fr))
		}
		q := "SELECT grp, ord, " + strings.Join(cols, ", ") + " FROM wf ORDER BY grp, ord, i, n, f"
		frameAggIncremental = false
		want := runQuery(t, ctx, q)
		frameAggIncremental = true
		got := runQuery(t, ctx, q)
		if len(got) != len(want) || len(got) != 100 {
			t.Fatalf("%s: %d rows incremental, %d recomputed", fr, len(got), len(want))
		}
		for r := range want {
			for c := range want[r] {
				if g, w := got[r][c].Format(), want[r][c].Format(); g != w {
					t.Fatalf("%s row %d col %d: incremental %s, recomputed %s", fr, r, c, g, w)
				}
			}
		}
	}
}
