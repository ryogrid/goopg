package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// TestCorrelatedSublinkOverCTEValues pins M0122-0012 slice 91's values: a
// correlated scalar sublink filtering a CTE scan by the outer row's key —
// TPC-DS Q81's shape, now lowered to PARAM_EXEC slots so its result cache
// keys on the key alone — and the same query with the CTE defined inside the
// sublink over the outer row, which stays on the stack path. Both must
// return the rows a plain computation over the data selects, for keys with
// one row, many rows, a NULL key and bpchar padding.
func TestCorrelatedSublinkOverCTEValues(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE r (id int, k char(2), v int)`)
	type rec struct {
		id, v int
		k     string
		null  bool
	}
	var data []rec
	var vals []string
	for i := 1; i <= 60; i++ {
		k := []string{"GA", "TN", "C", "NY"}[i%4]
		null := i%13 == 0
		v := (i * 37) % 101
		data = append(data, rec{id: i, v: v, k: k, null: null})
		ks := "'" + k + "'"
		if null {
			ks = "NULL"
		}
		vals = append(vals, fmt.Sprintf("(%d, %s, %d)", i, ks, v))
	}
	runSQL(t, ctx, "INSERT INTO r VALUES "+strings.Join(vals, ", "))
	// Expected: v > avg(v) over the rows sharing the (non-NULL) key.
	sum, cnt := map[string]int{}, map[string]int{}
	for _, d := range data {
		if !d.null {
			sum[d.k] += d.v
			cnt[d.k]++
		}
	}
	var want []string
	for _, d := range data {
		if !d.null && float64(d.v) > float64(sum[d.k])/float64(cnt[d.k]) {
			want = append(want, fmt.Sprint(d.id))
		}
	}
	sort.Strings(want)
	for name, q := range map[string]string{
		"lowered": `WITH c AS MATERIALIZED (SELECT k, v FROM r)
			SELECT id FROM r r1 WHERE id < 0 OR v > (SELECT avg(v) FROM c WHERE c.k = r1.k)`,
		"stack": `SELECT id FROM r r1 WHERE id < 0 OR v > (WITH c AS MATERIALIZED (SELECT v FROM r r2 WHERE r2.k = r1.k)
			SELECT avg(v) FROM c)`,
	} {
		got := sortedRowValues(t, ctx, q)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: got %v\nwant %v", name, got, want)
		}
	}
	if len(want) < 10 {
		t.Fatalf("fixture selects %d rows; too few to exercise the cache", len(want))
	}
}
