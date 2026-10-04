package executor

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

var cteScanRowsRE = regexp.MustCompile(`rows=([0-9]+)`)

// TestCTEScanOverFinalizeAggregateReadsItsRows pins M0146-0009q. PG's
// Finalize aggregate carries the grouped rel's dNumGroups, estimated once
// over the original input, and a CTE Scan reads the CTE's rows. goopg's
// EstimateRows re-estimated a Final node over the Gather of partial states,
// where a two-key grouping over a join finds no column statistics: TPC-DS
// Q59's `CTE Scan on wss` read 6265 of its Finalize HashAggregate's 62646.
// On this fixture PG 18.3 prints rows=3432 for the Finalize aggregate and
// for both CTE Scans; goopg's scans read 2400.
func TestCTEScanOverFinalizeAggregateReadsItsRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE fq_s (sd int, st int, p numeric)",
		"INSERT INTO fq_s SELECT g % 2000, g % 12, g FROM generate_series(1, 400000) g",
		"CREATE TABLE fq_d (dk int, wk int)",
		"INSERT INTO fq_d SELECT g, g / 7 FROM generate_series(0, 1999) g",
		"ANALYZE fq_s",
		"ANALYZE fq_d",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.ParallelSetupCost, ps.ParallelTupleCost, ps.MinParallelTableScanSize = 0, 0, 0
	plan := renderRows(runSQLWith(t, ctx, `EXPLAIN WITH w AS (SELECT wk, st, sum(p) s FROM fq_s, fq_d
		WHERE sd = dk GROUP BY wk, st)
		SELECT * FROM w w1, w w2 WHERE w1.wk = w2.wk - 52 AND w1.st = w2.st`, ps))
	joined := strings.Join(plan, "\n")
	rowsOf := func(sub string) []string {
		var out []string
		for _, l := range plan {
			if strings.Contains(l, sub) {
				if m := cteScanRowsRE.FindStringSubmatch(l); m != nil {
					out = append(out, m[1])
				}
			}
		}
		return out
	}
	final := rowsOf("Finalize HashAggregate")
	if len(final) != 1 {
		t.Fatalf("fixture: want one Finalize HashAggregate (a parallel CTE body):\n%s", joined)
	}
	scans := rowsOf("CTE Scan on w")
	if len(scans) != 2 {
		t.Fatalf("want two CTE Scans:\n%s", joined)
	}
	for _, r := range scans {
		if r != final[0] {
			t.Errorf("CTE Scan reads rows=%s, want the Finalize aggregate's rows=%s:\n%s", r, final[0], joined)
		}
	}
}
