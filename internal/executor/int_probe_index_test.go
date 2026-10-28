package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestIntProbeIndexMatchesMap pins M0122-0012 C59's probe table against the
// map it indexes: every present key returns the same rows slice, absent keys
// return nil, and keys built to collide in the Fibonacci hash (multiples of
// 2^40, negatives, zero, the int64 extremes) probe past one another. The
// index follows the map: another map of the same length, a shrunk map and a
// nil map each rebuild it or fall back.
func TestIntProbeIndexMatchesMap(t *testing.T) {
	m := map[int64][]Row{}
	keys := []int64{0, -1, 1, -1 << 63, 1<<63 - 1}
	for i := int64(1); i <= 3000; i++ {
		keys = append(keys, i<<40, -i*7919, i*3)
	}
	for i, k := range keys {
		m[k] = append(m[k], Row{NewIntDatum(k), NewIntDatum(int64(i))})
		if i%5 == 0 {
			m[k] = append(m[k], Row{NewIntDatum(k), NewIntDatum(-int64(i))})
		}
	}
	o := &joinOp{lazyIntHash: m}
	check := func(label string, probes []int64) {
		t.Helper()
		for _, k := range probes {
			got, want := o.intProbeRows(k), o.lazyIntHash[k]
			if len(got) != len(want) || (len(want) > 0 && &got[0] != &want[0]) {
				t.Fatalf("%s: key %d: index rows %d, map rows %d", label, k, len(got), len(want))
			}
		}
	}
	absent := []int64{2, -2, 1 << 41 >> 1, 5 << 40 >> 3, 123456789}
	check("built", append(append([]int64(nil), keys...), absent...))
	if o.intProbe.entries == nil {
		t.Fatal("the index was not built")
	}
	// Another map with the same length: the index must not answer from the
	// old one.
	m2 := map[int64][]Row{}
	for i, k := range keys {
		m2[k+1] = []Row{{NewIntDatum(k + 1), NewIntDatum(int64(i))}}
	}
	if len(m2) != len(m) {
		t.Fatalf("precondition: %d vs %d keys", len(m2), len(m))
	}
	o.lazyIntHash = m2
	check("replaced", append(append([]int64(nil), keys...), absent...))
	delete(m2, keys[3]+1)
	check("shrunk", keys)
	o.lazyIntHash = nil
	if o.intProbeRows(1) != nil {
		t.Fatal("nil map: rows")
	}
}

// TestIntProbeIndexJoinsMatchMap runs hash joins with the probe index and
// with the map lookup and requires identical output: a multi-batch build
// whose map is replaced per batch (the index must follow), and SQL inner,
// outer, semi and anti joins on int, int8 and NUMERIC keys with duplicates,
// NULLs and unmatched probe keys.
func TestIntProbeIndexJoinsMatchMap(t *testing.T) {
	t.Cleanup(func() { intProbeIndexDisabled = false })
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 700, "b")
	probeRows := intKeyRows(3000, 900, "p")
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
	for _, wm := range []int64{unboundedWorkMem, 256 << 10} {
		got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, wm)
		intProbeIndexDisabled = true
		want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, wm)
		intProbeIndexDisabled = false
		if wm != unboundedWorkMem && (bs == nil || bs.nbatch < 2) {
			t.Fatalf("precondition: work_mem %d did not batch", wm)
		}
		assertSameRows(t, want, got)
	}

	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ipx_p (a int, k int, k8 int8, kn numeric(14,0))")
	runSQL(t, ctx, `INSERT INTO ipx_p SELECT x, CASE WHEN x % 97 = 0 THEN NULL ELSE (x % 400) - 50 END,
		(x % 400)::int8 << 40, CASE WHEN x % 89 = 0 THEN NULL ELSE (x % 400)::numeric END FROM generate_series(1, 4000) x`)
	runSQL(t, ctx, "CREATE TABLE ipx_b (k int, k8 int8, kn numeric(14,0), w text)")
	runSQL(t, ctx, `INSERT INTO ipx_b SELECT (x % 300) - 20, (x % 300)::int8 << 40, (x % 300)::numeric, 'w' || x
		FROM generate_series(0, 899) x`)
	runSQL(t, ctx, "INSERT INTO ipx_b VALUES (NULL, NULL, NULL, 'wnull')")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableNestLoop, ps.EnableMergeJoin = false, false
	ps.MaxParallelWorkersPerGather = 0
	for _, q := range []string{
		"SELECT p.a, b.w FROM ipx_p p JOIN ipx_b b ON p.k = b.k ORDER BY p.a, b.w",
		"SELECT count(*), sum(p.a) FROM ipx_p p JOIN ipx_b b ON p.k8 = b.k8",
		"SELECT p.a, b.w FROM ipx_p p JOIN ipx_b b ON p.kn = b.kn ORDER BY p.a, b.w",
		"SELECT p.a, b.w FROM ipx_p p LEFT JOIN ipx_b b ON p.k = b.k WHERE p.a < 300 ORDER BY p.a, b.w",
		"SELECT b.w, count(p.a) FROM ipx_p p RIGHT JOIN ipx_b b ON p.k = b.k GROUP BY b.w ORDER BY b.w",
		"SELECT p.a, b.w FROM ipx_p p FULL JOIN ipx_b b ON p.k8 = b.k8 WHERE p.a IS NULL OR p.a < 50 ORDER BY p.a, b.w",
		"SELECT count(*) FROM ipx_p p WHERE EXISTS (SELECT 1 FROM ipx_b b WHERE b.k = p.k)",
		"SELECT count(*) FROM ipx_p p WHERE NOT EXISTS (SELECT 1 FROM ipx_b b WHERE b.kn = p.kn)",
	} {
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		intProbeIndexDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		intProbeIndexDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: probe index differs from the map\nindex %v\nmap   %v", q, got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}
