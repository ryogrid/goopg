package executor

import (
	"strings"
	"testing"
)

// TestIntGroupTableMatchesMap pins M0122-0012 C60's group table against a
// map through many doublings: keys built to collide in the Fibonacci hash
// (multiples of 2^40), negatives, zero and the int64 extremes; every key put
// is found again with its own group, and absent keys find nothing.
func TestIntGroupTableMatchesMap(t *testing.T) {
	tb := newIntGroupTable()
	want := map[int64]*groupRuntime{}
	keys := []int64{0, -1, 1, -1 << 63, 1<<63 - 1}
	for i := int64(1); i <= 20000; i++ {
		keys = append(keys, i<<40, -i*7919, i*3+1)
	}
	for i, k := range keys {
		if _, dup := want[k]; dup {
			continue
		}
		if tb.get(k) != nil {
			t.Fatalf("key %d found before it was put", k)
		}
		g := &groupRuntime{setIdx: i}
		tb.put(k, g)
		want[k] = g
		if i%997 == 0 {
			for wk, wg := range want {
				if tb.get(wk) != wg {
					t.Fatalf("after %d puts: key %d lost its group", i, wk)
				}
			}
		}
	}
	for k, g := range want {
		if tb.get(k) != g {
			t.Fatalf("key %d: wrong group", k)
		}
	}
	for _, k := range []int64{2, -2, 1<<41 + 1, 123456789} {
		if _, ok := want[k]; !ok && tb.get(k) != nil {
			t.Fatalf("absent key %d found", k)
		}
	}
	if tb.n != len(want) || 3*tb.n > 2*len(tb.entries) {
		t.Fatalf("n=%d entries=%d keys=%d", tb.n, len(tb.entries), len(want))
	}
}

// TestAggregateIntGroupTableMatchesMap runs hashed aggregates with the group
// table and with the map it replaced and requires the same output, ORDER
// included (the lane emits groups in founding order, never table order):
// tens of thousands of groups (many doublings), keys 2^40 apart, negatives,
// and a NULL then a fraction mid-stream that demote the lane after the table
// has grown.
func TestAggregateIntGroupTableMatchesMap(t *testing.T) {
	t.Cleanup(func() { aggIntTableDisabled = false })
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE igt (k numeric, k8 int8, i int)")
	runSQL(t, ctx, `INSERT INTO igt SELECT (x * 7919) % 40000 - 20000, ((x % 30000)::int8 << 40) - 5, x
		FROM generate_series(1, 120000) x`)
	runSQL(t, ctx, "CREATE TABLE igtd (k numeric, i int)")
	runSQL(t, ctx, "INSERT INTO igtd SELECT x % 9000, x FROM generate_series(1, 30000) x")
	runSQL(t, ctx, "INSERT INTO igtd VALUES (NULL, 1), (2.5, 2), (NULL, 3)")
	runSQL(t, ctx, "INSERT INTO igtd SELECT x % 9500, x FROM generate_series(1, 20000) x")
	for _, tb := range []string{"igt", "igtd"} {
		runSQL(t, ctx, "ANALYZE "+tb)
	}
	for _, q := range []string{
		"SELECT k, count(*), sum(i) FROM igt GROUP BY k",
		"SELECT k8, count(*), min(i) FROM igt GROUP BY k8",
		"SELECT k FROM igt GROUP BY k HAVING sum(i) > 200000",
		"SELECT k, count(*), sum(i) FROM igtd GROUP BY k",
	} {
		got := renderRows(runSQL(t, ctx, q))
		aggIntTableDisabled = true
		want := renderRows(runSQL(t, ctx, q))
		aggIntTableDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: group table output differs from the map (rows %d vs %d)", q, len(got), len(want))
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}
