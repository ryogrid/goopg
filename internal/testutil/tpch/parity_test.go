package tpch_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestTPCHResultParity runs each Q1..Q22 against goopg AND upstream
// PostgreSQL 18.3 on the SAME synthetic dataset and diffs the rows.
// This is the closest goopg currently gets to the milestone DoD #3
// requirement that "for each query, results are byte-identical or
// otherwise verified-equivalent to the same query against an
// upstream PostgreSQL on the same generated data set."
//
// The test loads the 8 TPC-H tables with the 75-row synthetic
// dataset from `tpch.SampleInserts()`. It does NOT run against
// SF1 — that requires the HammerDB Docker harness which is gated
// on a separate infrastructure path (Docker --network host on
// WSL2). Catching divergences at this scale still surfaces real
// semantic gaps; SF1 differences are usually a superset.
//
// For each query we record:
//   - both servers' execution status (OK / error)
//   - both servers' row counts
//   - first cell that differs, when both succeed
//
// The test is fail-closed on unexpected divergences. We keep a
// tiny explicit allowlist for the known synthetic-dataset numeric
// precision deltas (Q1/Q8/Q14) and fail on every other mismatch,
// as well as on any newly diverging query.
func TestTPCHResultParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping parity test in short mode")
	}
	repoRoot := repoRoot(t)
	if !upstreamPGAvailable(repoRoot) {
		t.Skipf("upstream PostgreSQL not installed under %s/postgres/local_install", repoRoot)
	}

	base := t.TempDir()

	// --- spin up goopg ---
	gp, err := cluster.New("tpch-parity", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "goopg-data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gp.Init(); err != nil {
		t.Fatal(err)
	}
	if err := gp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gp.Stop(cluster.ShutdownImmediate) }()

	// --- spin up upstream PG ---
	up := newUpstreamPG(t, repoRoot, filepath.Join(base, "upstream-data"))
	if err := up.initdb(); err != nil {
		t.Fatalf("upstream initdb: %v", err)
	}
	if err := up.start(); err != nil {
		t.Fatalf("upstream start: %v", err)
	}
	defer func() { _ = up.stop() }()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// --- apply DDL + INSERTs to both ---
	for _, ddl := range tpch.DDL() {
		if _, err := gp.Query(ctx, ddl); err != nil {
			t.Fatalf("goopg DDL %q: %v", firstWords(ddl, 6), err)
		}
		if _, err := up.query(ctx, ddl); err != nil {
			t.Fatalf("upstream DDL %q: %v", firstWords(ddl, 6), err)
		}
	}
	for _, ins := range tpch.SampleInserts() {
		if _, err := gp.Query(ctx, ins); err != nil {
			t.Fatalf("goopg INSERT %q: %v", firstWords(ins, 4), err)
		}
		if _, err := up.query(ctx, ins); err != nil {
			t.Fatalf("upstream INSERT %q: %v", firstWords(ins, 4), err)
		}
	}
	// ANALYZE on both so both have stats (goopg's planner consumes
	// them via the new join-order pre-pass; upstream uses them in
	// its own cost model). Failures are non-fatal — neither side
	// ABSOLUTELY needs them to execute.
	for _, t8 := range []string{"region", "nation", "supplier", "customer",
		"part", "partsupp", "orders", "lineitem"} {
		_, _ = gp.Query(ctx, "ANALYZE "+t8)
		_, _ = up.query(ctx, "ANALYZE "+t8)
	}

	// --- run each query against both, record outcome ---
	queries := tpch.Queries()
	type cell struct {
		ok   bool
		rows [][]string
		err  string
	}
	type matrix struct {
		gp, up cell
	}
	results := make(map[int]matrix)
	for q := 1; q <= 22; q++ {
		var m matrix
		if rows, err := gp.Query(ctx, queries[q]); err == nil {
			m.gp = cell{ok: true, rows: trimRows(rows)}
		} else {
			m.gp = cell{ok: false, err: truncate(err.Error(), 240)}
		}
		if rows, err := up.query(ctx, queries[q]); err == nil {
			m.up = cell{ok: true, rows: trimRows(rows)}
		} else {
			m.up = cell{ok: false, err: truncate(err.Error(), 240)}
		}
		results[q] = m
	}

	// --- summarise ---
	type bucket struct {
		identical, divergent, goopgErrored, upstreamErrored, bothErrored int
	}
	var b bucket
	t.Logf("TPC-H result-parity matrix (synthetic dataset, %d rows total):", totalSampleRows())
	for q := 1; q <= 22; q++ {
		m := results[q]
		switch {
		case !m.gp.ok && !m.up.ok:
			b.bothErrored++
			t.Logf("  Q%-2d both errored — goopg: %s; upstream: %s",
				q, m.gp.err, m.up.err)
		case !m.gp.ok:
			b.goopgErrored++
			t.Logf("  Q%-2d goopg errored, upstream OK (%d rows): %s",
				q, len(m.up.rows), m.gp.err)
		case !m.up.ok:
			b.upstreamErrored++
			t.Logf("  Q%-2d upstream errored, goopg OK (%d rows): %s",
				q, len(m.gp.rows), m.up.err)
		default:
			ri, ci := firstDiff(m.gp.rows, m.up.rows)
			if ri == -1 && ci == -1 {
				b.identical++
				t.Logf("  Q%-2d IDENTICAL (%d rows)", q, len(m.gp.rows))
			} else {
				b.divergent++
				if ci == -2 {
					t.Logf("  Q%-2d DIVERGENT row counts: goopg=%d upstream=%d",
						q, len(m.gp.rows), len(m.up.rows))
				} else {
					t.Logf("  Q%-2d DIVERGENT first diff at row=%d col=%d: goopg=%q upstream=%q",
						q, ri, ci, m.gp.rows[ri][ci], m.up.rows[ri][ci])
				}
			}
		}
	}
	t.Logf("PARITY SUMMARY: identical=%d divergent=%d goopg-errored=%d upstream-errored=%d both-errored=%d",
		b.identical, b.divergent, b.goopgErrored, b.upstreamErrored, b.bothErrored)

	// Known divergences on the synthetic dataset. M0041-0004 closed
	// the previous Q1/Q8/Q14 numeric-precision deltas by switching
	// numericDiv to upstream's select_div_scale rule and adding a
	// *big.Int overflow lane to the NUMERIC carrier — so the
	// allowlist is now empty and the test fails closed on any
	// re-introduced divergence.
	knownDivergences := map[int]string{}

	// Hard expectation: every query that errored on goopg but
	// succeeded on upstream is a real regression goopg must close.
	for q := 1; q <= 22; q++ {
		m := results[q]
		if !m.gp.ok && m.up.ok {
			t.Errorf("Q%d goopg errored while upstream succeeded (%d rows): %s",
				q, len(m.up.rows), m.gp.err)
		}

		if m.gp.ok && m.up.ok {
			ri, ci := firstDiff(m.gp.rows, m.up.rows)
			hasDiff := ri != -1 || ci != -1
			_, allowlisted := knownDivergences[q]
			switch {
			case hasDiff && !allowlisted:
				if ci == -2 {
					t.Errorf("Q%d unexpected parity divergence: row counts differ goopg=%d upstream=%d",
						q, len(m.gp.rows), len(m.up.rows))
				} else {
					t.Errorf("Q%d unexpected parity divergence at row=%d col=%d: goopg=%q upstream=%q",
						q, ri, ci, m.gp.rows[ri][ci], m.up.rows[ri][ci])
				}
			case !hasDiff && allowlisted:
				t.Errorf("Q%d now matches upstream; remove stale known-divergence entry (%s)",
					q, knownDivergences[q])
			}
		}
	}
}

func totalSampleRows() int {
	n := 0
	for _, ins := range tpch.SampleInserts() {
		n += strings.Count(ins, "), (") + 1
	}
	return n
}

// keep linter happy when only some helpers are used
var _ = fmt.Sprintf
