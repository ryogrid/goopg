package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// probeDeferArms counts armed probes; tests read it to prove the deferred
// path ran.
var probeDeferArms int

// TestLazyProbeDeformMatchesEager pins M0122-0012 C32: a hash join over a seq
// scan probe whose non-key columns are decoded only for the rows the join uses
// returns exactly the rows — order included — the eager decode does. The
// probe table has NULL keys, a toasted text column, numeric and text columns
// past the key, few matching rows and many non-matching ones; the queries
// cover inner, left (probe preserved), right, full, semi, anti, null-aware
// anti (NOT IN), a residual on a deferred column, multi-column keys, a probe
// under a narrowing projection, an empty build, and a key past other read
// columns (the first pass decodes everything up to the last key). Each sits under a
// borrowing root (aggregate or ORDER BY), as the arming requires.
func TestLazyProbeDeformMatchesEager(t *testing.T) {
	old := probeDeferArmed
	probeDeferArmed = func() { probeDeferArms++ }
	t.Cleanup(func() { probeDeferArmed = old })
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pd_probe (a int, k int, q numeric(12,2), p numeric(12,2), s text, big text)")
	runSQL(t, ctx, "INSERT INTO pd_probe SELECT x, CASE WHEN x % 97 = 0 THEN NULL ELSE x % 500 END, (x % 50) * 1.5, x * 0.25, 's' || x, CASE WHEN x % 211 = 0 THEN repeat('z', 9000) ELSE 'b' END FROM generate_series(1, 4000) x")
	runSQL(t, ctx, "CREATE TABLE pd_build (k int, k2 int, w text)")
	runSQL(t, ctx, "INSERT INTO pd_build SELECT x * 7, x % 3, 'w' || x FROM generate_series(0, 40) x")
	runSQL(t, ctx, "INSERT INTO pd_build VALUES (NULL, 1, 'wnull')")
	runSQL(t, ctx, "CREATE TABLE pd_empty (k int)")
	// Every row of pd_all matches: past probeDeferDisarmRows deferred rows the
	// scan disarms mid-scan, and the rows on either side of the switch must
	// still come out complete.
	runSQL(t, ctx, "CREATE TABLE pd_all (k int, q numeric(10,2), s text)")
	runSQL(t, ctx, "INSERT INTO pd_all SELECT x % 41 * 7, x * 0.5, 't' || x FROM generate_series(1, 10000) x")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableNestLoop = false
	ps.EnableMergeJoin = false
	ps.MaxParallelWorkersPerGather = 0
	queries := []string{
		"SELECT count(*), sum(p.q), sum(p.p), max(p.s), max(length(p.big)) FROM pd_probe p JOIN pd_build b ON p.k = b.k",
		"SELECT p.a, p.q, p.s, length(p.big), b.w FROM pd_probe p JOIN pd_build b ON p.k = b.k ORDER BY p.a",
		"SELECT p.a, p.q, b.w FROM pd_probe p LEFT JOIN pd_build b ON p.k = b.k WHERE p.a < 300 ORDER BY p.a",
		"SELECT count(*), sum(p.q), count(b.w) FROM pd_probe p LEFT JOIN pd_build b ON p.k = b.k",
		"SELECT b.w, p.q FROM pd_build b RIGHT JOIN pd_probe p ON p.k = b.k ORDER BY p.a",
		"SELECT p.a, p.q, b.w FROM pd_probe p FULL JOIN pd_build b ON p.k = b.k ORDER BY p.a, b.w",
		"SELECT count(*), sum(p.q) FROM pd_probe p WHERE EXISTS (SELECT 1 FROM pd_build b WHERE b.k = p.k)",
		"SELECT count(*), sum(p.q), max(p.s) FROM pd_probe p WHERE NOT EXISTS (SELECT 1 FROM pd_build b WHERE b.k = p.k)",
		"SELECT count(*), sum(p.q) FROM pd_probe p WHERE p.k NOT IN (SELECT b.k FROM pd_build b WHERE b.k IS NOT NULL)",
		"SELECT count(*), sum(p.q) FROM pd_probe p JOIN pd_build b ON p.k = b.k AND p.q > b.k2",
		"SELECT count(*), sum(p.q) FROM pd_probe p JOIN pd_build b ON p.k = b.k AND p.a % 3 = b.k2",
		"SELECT sum(x.q) FROM (SELECT p.k, p.q FROM pd_probe p) x JOIN pd_build b ON x.k = b.k",
		"SELECT count(*), sum(p.q) FROM pd_probe p JOIN pd_empty e ON p.k = e.k",
		"SELECT count(*), sum(p.q), max(length(p.big)) FROM pd_probe p JOIN pd_build b ON p.s = 's' || b.k::text",
		"SELECT count(*), sum(a.q), max(a.s), min(a.s) FROM pd_all a JOIN pd_build b ON a.k = b.k",
		"SELECT a.q, a.s FROM pd_all a JOIN pd_build b ON a.k = b.k ORDER BY a.q DESC, a.s LIMIT 20",
	}
	for _, q := range queries {
		probeDeferArms = 0
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		armed := probeDeferArms
		probeDeferDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		probeDeferDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s (armed %d): deferred probe differs from eager\ndeferred %v\neager    %v", q, armed, got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
		t.Logf("armed=%d rows=%d %s", armed, len(got), q)
	}
}
