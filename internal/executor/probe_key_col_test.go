package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestProbeKeyColMatchesEvaluator pins M0122-0012 C45: a hash join whose
// single int-lane probe key is a bare column reads it in place from the probe
// slot and must return exactly the rows — order included — the compiled
// evaluator path returns. Keys are int4, int8 and NUMERIC (HammerDB's TPC-H
// keys), with NULLs on both sides; the joins are inner, left, right, full,
// semi, anti and NOT IN, with the probe on either side (a RIGHT join builds
// the other input), a residual, an expression key (no fast path) and a
// projection between the join and its probe scan.
func TestProbeKeyColMatchesEvaluator(t *testing.T) {
	arms := 0
	old := probeKeyColArmed
	probeKeyColArmed = func() { arms++ }
	t.Cleanup(func() { probeKeyColArmed = old; probeKeyColDisabled = false })
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pk_probe (a int, k int, k8 int8, kn numeric(12,0), q numeric(10,2), s text)")
	runSQL(t, ctx, "INSERT INTO pk_probe SELECT x, CASE WHEN x % 97 = 0 THEN NULL ELSE x % 300 END, (x % 300)::int8 * 1000000000, CASE WHEN x % 89 = 0 THEN NULL ELSE (x % 300)::numeric END, x * 0.5, 's' || x FROM generate_series(1, 3000) x")
	runSQL(t, ctx, "CREATE TABLE pk_build (k int, k8 int8, kn numeric(12,0), w text)")
	runSQL(t, ctx, "INSERT INTO pk_build SELECT x * 7, (x * 7)::int8 * 1000000000, (x * 7)::numeric, 'w' || x FROM generate_series(0, 40) x")
	runSQL(t, ctx, "INSERT INTO pk_build VALUES (NULL, NULL, NULL, 'wnull')")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableNestLoop, ps.EnableMergeJoin = false, false
	ps.MaxParallelWorkersPerGather = 0
	queries := []string{
		"SELECT p.a, p.q, b.w FROM pk_probe p JOIN pk_build b ON p.k = b.k ORDER BY p.a",
		"SELECT count(*), sum(p.q) FROM pk_probe p JOIN pk_build b ON p.k8 = b.k8",
		"SELECT p.a, b.w FROM pk_probe p JOIN pk_build b ON p.kn = b.kn ORDER BY p.a",
		"SELECT p.a, b.w FROM pk_probe p LEFT JOIN pk_build b ON p.k = b.k WHERE p.a < 400 ORDER BY p.a",
		"SELECT b.w, count(p.a) FROM pk_probe p RIGHT JOIN pk_build b ON p.k = b.k GROUP BY b.w ORDER BY b.w",
		"SELECT p.a, b.w FROM pk_probe p FULL JOIN pk_build b ON p.kn = b.kn WHERE p.a IS NULL OR p.a < 100 ORDER BY p.a, b.w",
		"SELECT count(*), sum(p.q) FROM pk_probe p WHERE EXISTS (SELECT 1 FROM pk_build b WHERE b.k = p.k)",
		"SELECT count(*), sum(p.q) FROM pk_probe p WHERE NOT EXISTS (SELECT 1 FROM pk_build b WHERE b.kn = p.kn)",
		"SELECT count(*) FROM pk_probe p WHERE p.k NOT IN (SELECT b.k FROM pk_build b WHERE b.k IS NOT NULL)",
		"SELECT count(*), sum(p.q) FROM pk_probe p JOIN pk_build b ON p.k = b.k AND p.q > b.k",
		"SELECT count(*) FROM pk_probe p JOIN pk_build b ON p.k + 0 = b.k",
		"SELECT sum(x.q) FROM (SELECT p.k, p.q FROM pk_probe p) x JOIN pk_build b ON x.k = b.k",
	}
	for _, q := range queries {
		before := arms
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		n := arms - before
		probeKeyColDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		probeKeyColDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s (armed %d): in-place probe key differs from the evaluator\nin place  %v\nevaluator %v", q, n, got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
		t.Logf("armed=%d rows=%d %s", n, len(got), q)
	}
	if arms == 0 {
		t.Fatal("no join read its probe key in place")
	}
}
