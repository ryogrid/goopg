package testport

// regress_suite_recovery_test.go — guard for TestPort_RegressSuite's
// wedge/crash recovery path.
//
// Why this exists (M-NIGHTLY, 2026-08-06): when a regress case hits the 120 s
// per-case deadline the suite marks the cluster poisoned and restarts it, then
// restores the shared test_setup.sql fixtures. Restoring them by simply
// re-running test_setup.sql is what produced the recurring nightly
// "suite-wedge casualties": psql runs without ON_ERROR_STOP, so every
// `CREATE TABLE` fails ("relation already exists") while the `INSERT`/`COPY`
// after it succeeds, and each fixture table doubles. Cases downstream of the
// wedge then report real output mismatches that the tree does not have —
// 8 of the 10 action items of nightly run 20260806-191958 were this.
//
// The guard asserts the invariant the recovery path must hold: after a
// recovery cycle the fixtures are byte-for-byte the ones the suite started
// with. Its second half deliberately performs the unguarded restore and
// asserts the doubling, so the guard can never pass vacuously.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// fixtureCounts are the canonical cardinalities test_setup.sql establishes.
// They are properties of postgres/src/test/regress/sql/test_setup.sql, not of
// goopg, so they only move when the postgres submodule does.
var fixtureCounts = map[string]int{
	"int4_tbl":    5,
	"text_tbl":    2,
	"varchar_tbl": 4,
	"onek":        1000,
	"tenk1":       10000,
}

func TestPort_RegressSuiteRecoveryKeepsFixturesPristine(t *testing.T) {
	root := repoRoot(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not in PATH or postgres/local_install/bin")
	}

	c := newCluster(t, "regress_recovery")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	exec := &ClusterRegressExecutor{Cluster: c, PsqlBin: psqlBin, RepoRoot: root}

	runRegressSetup(t, root, psqlBin, c)
	before := readFixtureCounts(t, c)
	for name, want := range fixtureCounts {
		if before[name] != want {
			t.Fatalf("fixture %s: setup produced %d rows, want %d "+
				"(test_setup.sql or its data files changed — re-pin fixtureCounts)",
				name, before[name], want)
		}
	}

	// The recovery cycle the suite performs when a case wedges the cluster.
	if err := c.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !exec.fixturesPresent() {
		t.Fatal("fixtures did not survive the restart; recovery would re-bootstrap and the guard below would be vacuous")
	}
	restoreRegressFixtures(t, root, psqlBin, c, exec)

	after := readFixtureCounts(t, c)
	for name, want := range fixtureCounts {
		if after[name] != want {
			t.Errorf("fixture %s after wedge recovery: %d rows, want %d "+
				"(recovery corrupted the shared fixtures; every downstream regress case "+
				"reading this table will report a bogus output mismatch)",
				name, after[name], want)
		}
	}

	// Non-vacuity: the unguarded restore — re-running test_setup.sql on a
	// cluster that already carries the fixtures — must double them. If this
	// ever stops holding, test_setup.sql became idempotent upstream and
	// restoreRegressFixtures's guard is no longer load-bearing.
	runRegressSetup(t, root, psqlBin, c)
	doubled := readFixtureCounts(t, c)
	for name, want := range fixtureCounts {
		if doubled[name] != 2*want {
			t.Errorf("unguarded re-setup of %s: %d rows, want %d — the hazard "+
				"restoreRegressFixtures guards against no longer reproduces; "+
				"re-derive the guard before trusting it",
				name, doubled[name], 2*want)
		}
	}
}

// readFixtureCounts returns the current row count of each shared fixture table.
// A table that does not resolve reports -1 rather than failing, so the caller's
// assertion message names the table.
func readFixtureCounts(t *testing.T, c *cluster.Cluster) map[string]int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out := make(map[string]int, len(fixtureCounts))
	for name := range fixtureCounts {
		rows, err := c.Query(ctx, "SELECT count(*) FROM "+name)
		if err != nil || len(rows) != 1 || len(rows[0]) != 1 {
			t.Logf("count %s: %v (rows=%v)", name, err, rows)
			out[name] = -1
			continue
		}
		n, err := strconv.Atoi(rows[0][0])
		if err != nil {
			t.Logf("count %s: unparsable %q: %v", name, rows[0][0], err)
			out[name] = -1
			continue
		}
		out[name] = n
	}
	return out
}
