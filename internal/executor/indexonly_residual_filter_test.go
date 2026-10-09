package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestIndexOnlyScanKeepsResidualFilter pins M0146-0019a: build_index_paths
// builds an index-only path whenever check_index_only holds, and a
// restriction the index cannot bind stays as the scan's Filter (TPC-H Q16's
// `partsupp_pk` with `NOT (ps_suppkey = ANY (hashed SubPlan))`). goopg refused
// any index-only leaf with a residual qual. The uncorrelated NOT IN is a
// hashed SubPlan, priced once at startup (cost_subplan's useHashTable arm),
// not per row. Plans and values are PG 18.3's.
func TestIndexOnlyScanKeepsResidualFilter(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE ios_r (a int NOT NULL, b int NOT NULL, pad text)",
		"CREATE TABLE ios_s (k int)",
		"INSERT INTO ios_r SELECT g, g % 97, repeat('x', 400) FROM generate_series(1, 4000) g",
		"INSERT INTO ios_s SELECT g * 3 FROM generate_series(1, 20) g",
		"CREATE INDEX ios_r_ab ON ios_r (a, b)",
		"CREATE TABLE ios_p (u1 int NOT NULL, u2 int NOT NULL)",
		"INSERT INTO ios_p SELECT g, (g * 7) % 1000 FROM generate_series(0, 999) g",
		"CREATE UNIQUE INDEX ios_p_21 ON ios_p (u2, u1)",
	)
	vacuumThen(t, ctx, "ios_r")
	vacuumThen(t, ctx, "ios_p")
	runComposite(t, ctx, "ANALYZE ios_r", "ANALYZE ios_s", "ANALYZE ios_p")

	seqOff := optimizer.DefaultPlannerSettings()
	seqOff.EnableSeqScan = false
	cases := []struct {
		ps    optimizer.PlannerSettings
		query string
		plan  []string
		rows  string
	}{
		{
			optimizer.DefaultPlannerSettings(),
			"SELECT count(*), sum(a) FROM ios_r WHERE b NOT IN (SELECT k FROM ios_s)",
			[]string{
				"Index Only Scan using ios_r_ab on ios_r",
				"Filter: (NOT (ANY (b = (hashed SubPlan 1).col1)))",
			},
			"3173|6357447",
		},
		{
			optimizer.DefaultPlannerSettings(),
			"SELECT sum(a) FROM ios_r WHERE b % 5 = 1",
			[]string{
				"Index Only Scan using ios_r_ab on ios_r",
				"Filter: ((b % 5) = 1)",
			},
			"1650510",
		},
		{
			// Every column covered, in index order (u2, u1): the scan is as
			// wide as the table but permuted (regress create_index's
			// onek_with_null shape). enable_seqscan = off, as PG needs it to
			// prefer the index on so narrow a table.
			seqOff,
			"SELECT sum(u1), sum(u2) FROM ios_p WHERE u1 % 3 = 1",
			[]string{
				"Index Only Scan using ios_p_21 on ios_p",
				"Filter: ((u1 % 3) = 1)",
			},
			"166167|166169",
		},
	}
	for _, c := range cases {
		plan := strings.Join(explainLines(t, ctx, c.ps, "EXPLAIN (COSTS OFF) "+c.query), "\n")
		for _, want := range c.plan {
			if !strings.Contains(plan, want) {
				t.Errorf("%s: plan lacks %q:\n%s", c.query, want, plan)
			}
		}
		got := strings.Join(renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, c.query, c.ps))), ";")
		if got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q", c.query, got, c.rows)
		}
	}
}
