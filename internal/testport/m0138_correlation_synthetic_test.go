// TestPort_M0138CorrelationSyntheticGoopgVsPG is M0138-0009's synthetic-table
// resume point. The deferral ledger's `m0138-0001` correlation-tie-break row
// (REOPENED by M0138-0005) found the TPC-DS corpus census banding goopg's
// pg_stats.correlation to [0.09,0.16] on 36/120 columns against PG's 7/120,
// concentrated on high-duplicate-density FK columns — but M0138-0004's
// SliceStable tie-break fix (verified mechanically correct in isolation by
// executor.TestAnalyzeCorrelationTieBreakMatchesPGTupnoOrder) did not move
// that reading at all. The row's un-eliminated alternative is a TPC-DS
// loader/physical-layout artifact rather than an ANALYZE-side bug.
//
// This test isolates the two hypotheses by loading a HAND-CONSTRUCTED, fully
// deterministic sequence — value = rowIndex % 11, a repeating
// low-cardinality cycle representative of the census's flagged FK shape —
// via the exact server-side `COPY t FROM '<path>'` mechanism
// scripts/tpcds-load.sh uses (single connection, no concurrency, identical
// row order on both sides), into a real PG 18.3 instance and a fresh goopg
// instance.
//
// N=2200 keeps the sample well under the default `targrows` (stats_target
// 100 * upstreamSampleMultiplier 300 = 30000, operators_analyze.go:815), so
// ANALYZE's block sampler degrades to a full scan on BOTH engines — no
// reservoir-sampling randomness on either side. With loader order and
// sampling both controlled away, any surviving correlation discrepancy can
// only be a live ANALYZE-mechanism divergence (computation or physical scan
// order), not the TPC-DS loader.
package testport

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

func TestPort_M0138CorrelationSyntheticGoopgVsPG(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real PG 18.3 instance alongside goopg; skip in short mode")
	}
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	const n = 2200
	const k = 11
	vals := make([]int, n)
	for i := range vals {
		vals[i] = i % k
	}
	want := expectedFullPopulationCorrelation(vals)

	tsvPath := filepath.Join(t.TempDir(), "m0138_corr_synth.tsv")
	var sb strings.Builder
	for _, v := range vals {
		sb.WriteString(strconv.Itoa(v))
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(tsvPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write synthetic tsv: %v", err)
	}

	const tbl = "m0138_corr_synth"
	const col = "fk_val"
	createSQL := fmt.Sprintf("CREATE TABLE %s (%s int4)", tbl, col)
	copySQL := fmt.Sprintf("COPY %s FROM '%s'", tbl, tsvPath)
	analyzeSQL := fmt.Sprintf("ANALYZE %s", tbl)
	corrSQL := fmt.Sprintf(
		"SELECT correlation FROM pg_stats WHERE tablename = '%s' AND attname = '%s'", tbl, col)

	// --- goopg side ---
	gc, err := cluster.New("m0138-corr-synth-goopg", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(t.TempDir(), "goopg-data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New goopg: %v", err)
	}
	mustInitStart(t, gc)
	t.Cleanup(func() { _ = gc.Stop(cluster.ShutdownImmediate) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, stmt := range []string{createSQL, copySQL, analyzeSQL} {
		if _, err := gc.Query(ctx, stmt); err != nil {
			t.Fatalf("goopg %q: %v", stmt, err)
		}
	}
	goopgRows, err := gc.Query(ctx, corrSQL)
	if err != nil {
		t.Fatalf("goopg correlation query: %v", err)
	}
	if len(goopgRows) != 1 {
		t.Fatalf("goopg pg_stats rows = %d, want 1", len(goopgRows))
	}
	goopgCorr, err := strconv.ParseFloat(goopgRows[0][0], 64)
	if err != nil {
		t.Fatalf("goopg correlation %q not a float: %v", goopgRows[0][0], err)
	}

	// --- real PG 18.3 side ---
	pgc, err := pgcluster.New("m0138-corr-synth-pg", pgcluster.Options{
		RepoRoot:    repo,
		StartupWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	if err := pgc.Start(); err != nil {
		t.Fatalf("pgcluster.Start: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Stop() })

	for _, stmt := range []string{createSQL, copySQL, analyzeSQL} {
		pgc.Exec(t, stmt)
	}
	pgCorrStr := pgc.QueryScalar(t, corrSQL)
	pgCorr, err := strconv.ParseFloat(pgCorrStr, 64)
	if err != nil {
		t.Fatalf("PG correlation %q not a float: %v", pgCorrStr, err)
	}

	t.Logf("expected (hand-derived, PG tupno-tie-break formula) = %.6f", want)
	t.Logf("goopg pg_stats.correlation                          = %.6f", goopgCorr)
	t.Logf("PG 18.3 pg_stats.correlation                        = %.6f", pgCorr)

	// Both engines load the identical deterministic sequence via the exact
	// mechanism the TPC-DS loader uses (server-side COPY FROM file, single
	// connection, no concurrency) and sample it in full (N well under
	// targrows) --- so both should reproduce the hand-derived population
	// correlation almost exactly. A discrepancy here (rather than only in
	// the TPC-DS corpus) would mean the divergence is NOT a loader/physical
	// -layout artifact but a live ANALYZE-mechanism bug independent of the
	// tie-break M0138-0004 already fixed.
	const tol = 0.01
	if math.Abs(goopgCorr-want) > tol {
		t.Errorf("goopg correlation=%.6f diverges from hand-derived expectation=%.6f by >%.2g", goopgCorr, want, tol)
	}
	if math.Abs(pgCorr-want) > tol {
		t.Errorf("PG correlation=%.6f diverges from hand-derived expectation=%.6f by >%.2g", pgCorr, want, tol)
	}
	if math.Abs(goopgCorr-pgCorr) > tol {
		t.Errorf("goopg vs PG correlation diverge by %.6f (goopg=%.6f pg=%.6f) despite identical deterministic load order --- this is the M0138-0009 finding, distinct from the tie-break bug M0138-0004 already fixed",
			math.Abs(goopgCorr-pgCorr), goopgCorr, pgCorr)
	}
}

// expectedFullPopulationCorrelation hand-derives PG's STATISTIC_KIND_CORRELATION
// (analyze.c compute_scalar_stats, :2853-2890) for a FULLY sampled column (no
// reservoir subsampling): Pearson correlation between original scan position
// and post-sort (ties broken by ascending original position, matching PG's
// compare_scalars "for equal datums, sort by tupno") logical position.
func expectedFullPopulationCorrelation(vals []int) float64 {
	type item struct{ val, pos int }
	items := make([]item, len(vals))
	for i, v := range vals {
		items[i] = item{val: v, pos: i}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].val < items[j].val })

	n := float64(len(items))
	var corrXYSum float64
	for sortedPos, it := range items {
		corrXYSum += float64(it.pos) * float64(sortedPos)
	}
	corrXSum := (n - 1) * n / 2
	corrX2Sum := (n - 1) * n * (2*n - 1) / 6
	denom := n*corrX2Sum - corrXSum*corrXSum
	return (n*corrXYSum - corrXSum*corrXSum) / denom
}
