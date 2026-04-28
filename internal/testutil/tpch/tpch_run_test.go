package tpch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestRunTPCHQueriesAgainstSyntheticData spins up a real goopg
// cluster, creates the eight TPC-H tables, loads a tiny synthetic
// dataset (~5-15 rows per table), and runs each Q1..Q22. The test
// asserts that every query *executes* — i.e., the executor returns
// rows or an empty result without raising an error. It does NOT
// assert exact row content; result-set parity is verified against
// upstream PG by the (still-pending) HammerDB SF1 path.
//
// The harness's value: it surfaces executor-time failures the
// plan-time + Build-time tests can't see — missing built-in
// functions called on real data, NULL-handling crashes, type
// coercion gaps, etc. Each failure points at the first Q-number
// that breaks, so triage can attack the root cause directly.
func TestRunTPCHQueriesAgainstSyntheticData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed TPC-H smoke in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("tpch", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for _, ddl := range tpch.DDL() {
		if _, err := c.Query(ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", firstWords(ddl, 6), err)
		}
	}
	for _, ins := range tpch.SampleInserts() {
		if _, err := c.Query(ctx, ins); err != nil {
			t.Fatalf("INSERT %q: %v", firstWords(ins, 4), err)
		}
	}
	if _, err := c.Query(ctx, "ANALYZE region"); err != nil {
		t.Logf("ANALYZE region: %v (continuing)", err)
	}

	queries := tpch.Queries()
	type result struct {
		ok   bool
		rows int
		err  string
	}
	results := make(map[int]result)
	for q := 1; q <= 22; q++ {
		// Q15's first stmt is CREATE OR REPLACE VIEW; running it
		// twice in the same cluster session is fine, but we don't
		// follow up with the SELECT/DROP halves yet.
		rows, err := c.Query(ctx, queries[q])
		if err != nil {
			results[q] = result{false, 0, truncate(err.Error(), 200)}
			continue
		}
		results[q] = result{true, len(rows), ""}
	}

	var passed, failed []int
	for q := 1; q <= 22; q++ {
		if results[q].ok {
			passed = append(passed, q)
		} else {
			failed = append(failed, q)
		}
	}
	t.Logf("TPC-H executor-time coverage: %d/22 ran OK", len(passed))
	for q := 1; q <= 22; q++ {
		if results[q].ok {
			t.Logf("  Q%-2d OK   (%d rows)", q, results[q].rows)
		} else {
			t.Logf("  Q%-2d FAIL %s", q, results[q].err)
		}
	}
	// Fail-closed: every Q1..Q22 must run without an executor error.
	// Returning zero rows is fine for queries whose synthetic data
	// falls outside the filter (e.g., Q2 / Q12 / Q15 / Q16 /
	// Q18 / Q20 / Q21 / Q22), but a `pq:` error means there's a real
	// regression to fix. Result-set parity vs upstream PG is verified
	// separately via the (still-pending) HammerDB SF1 path.
	for q := 1; q <= 22; q++ {
		if !results[q].ok {
			t.Errorf("Q%d expected to execute but errored: %s", q, results[q].err)
		}
	}
}

func firstWords(s string, n int) string {
	parts := strings.Fields(s)
	if len(parts) <= n {
		return s
	}
	return strings.Join(parts[:n], " ") + " …"
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		next := filepath.Dir(cur)
		if next == cur {
			t.Fatalf("could not find go.mod from %s", wd)
		}
		cur = next
	}
}
