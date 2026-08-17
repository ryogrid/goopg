package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// S8 Slice 2c-i (0134-0001 P2) — brief acceptance criterion 1, the
// order-independence data test. applyIndexOrderedGroupingRule
// (internal/optimizer/groupagg_indexorder.go) never permutes Aggregate's
// GroupExprs — only an EXPLAIN-only GroupKeyOrder side-channel is added — on
// the premise that a sorted GroupAggregate's group-boundary test
// (finalizeGroup, operators_join_agg.go) is order-independent: an input
// physically sorted by (x, y) still makes every run of equal (y, x) values
// contiguous. This test verifies that premise against the RESULT ROWS AND
// VALUES, not just the plan shape (TestIndexOrderedGroupingCanonical in
// internal/optimizer covers the plan shape). If it fails, the design's
// fallback — a compensating output permutation, the posMap-style rewrite
// remapAggExprsWithBindings performs — is required instead.
func TestIndexOrderedGroupingResultRowsSurviveGroupByYX(t *testing.T) {
	runIndexOrderedGroupingDataTest(t, "select y, x, count(*) from btg group by y, x")
}

func TestIndexOrderedGroupingResultRowsSurviveGroupByXY(t *testing.T) {
	runIndexOrderedGroupingDataTest(t, "select y, x, count(*) from btg group by x, y")
}

func runIndexOrderedGroupingDataTest(t *testing.T, sql string) {
	t.Helper()
	// This bare executor fixture has no session/GUC dispatch, so a literal
	// `SET enable_hashagg = off` statement would not reach
	// applyIndexOrderedGroupingRule's gate (it reads the package-level
	// kill-switch directly, same as every other S8 unit test that calls
	// SetHashAggEnabled). Flip it here so the rule actually fires and this
	// test exercises the sorted-index path it claims to.
	optimizer.SetHashAggEnabled(false)
	defer optimizer.SetHashAggEnabled(true)
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE btg (x int4, y int4, z text, w int4)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX btg_x_y_idx ON btg (x, y)"); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// 30 rows, x = i%10, y = (i%10+1)%10: x and y are DELIBERATELY different
	// (never equal) so a column-swap bug flips which value lands under which
	// output name instead of silently agreeing with itself.
	for i := 0; i < 30; i++ {
		runSQL(t, ctx, insertBtgRow(i))
	}

	rows := runSQL(t, ctx, sql)
	got := map[[2]int64]int64{}
	for _, r := range rows {
		if len(r) != 3 {
			t.Fatalf("%s: row has %d columns, want 3: %v", sql, len(r), r)
		}
		y := r[0].Int
		x := r[1].Int
		c := r[2].Int
		got[[2]int64{y, x}] = c
	}
	// (x, y) pairs are (0,1), (1,2), ..., (9,0), each occurring exactly 3
	// times (i, i+10, i+20 share x=y=i%10... no: share the SAME i%10, so the
	// same (x, y) pair).
	want := map[[2]int64]int64{}
	for i := 0; i < 10; i++ {
		x := int64(i)
		y := int64((i + 1) % 10)
		want[[2]int64{y, x}] = 3
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %d groups, want %d: got=%v want=%v", sql, len(got), len(want), got, want)
	}
	for k, wc := range want {
		gc, ok := got[k]
		if !ok {
			t.Fatalf("%s: missing group y=%d,x=%d (result rows permuted?): got=%v want=%v", sql, k[0], k[1], got, want)
		}
		if gc != wc {
			t.Fatalf("%s: group y=%d,x=%d count = %d, want %d", sql, k[0], k[1], gc, wc)
		}
	}
}

func insertBtgRow(i int) string {
	x := i % 10
	y := (i + 1) % 10
	w := i
	digits := "0123456789"
	return "INSERT INTO btg (x, y, z, w) VALUES (" +
		string(digits[x]) + ", " + string(digits[y]) + ", 'abc" + string(digits[x]) + "', " + itoaBtg(w) + ")"
}

func itoaBtg(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
