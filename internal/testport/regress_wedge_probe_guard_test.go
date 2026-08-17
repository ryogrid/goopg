package testport

// regress_wedge_probe_guard_test.go — guard for the regress suite's wedge probe.
//
// The probe (regress_wedge_probe_test.go) only ever fires on a nightly, on a
// case that hangs nondeterministically, on a host nobody is watching. If it
// silently captured nothing there, the next wedge would again cost a whole
// nightly cycle and produce one unactionable log line — which is exactly the
// history this instrumentation exists to end.
//
// So the guard runs the probe against a cluster that is deliberately holding a
// long statement and asserts it actually names that statement, reaches the
// server's goroutine dump, and reads its RSS — the three sections that
// discriminate the surviving wedge hypotheses. A companion pure-function test
// covers the "blocked >1 minute" filter, which a five-second guard cannot
// exercise live.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// wedgeProbeMarker is embedded in the guard's long-running statement so the
// assertion matches that exact query rather than any incidental activity.
const wedgeProbeMarker = "goopg_wedge_probe_marker"

func TestPort_RegressWedgeProbeNamesTheStuckStatement(t *testing.T) {
	root := repoRoot(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not in PATH or postgres/local_install/bin")
	}

	pprofAddr := reserveLoopbackPort(t)
	t.Setenv("GOOPG_PPROF_ADDR", pprofAddr)

	c := newCluster(t, "regress_wedge_probe")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	e := &ClusterRegressExecutor{
		Cluster:   c,
		PsqlBin:   psqlBin,
		LibDir:    filepath.Join(root, "postgres", "local_install", "lib"),
		RepoRoot:  root,
		PprofAddr: pprofAddr,
	}

	// Stand in for the wedged backend: a statement that will still be running
	// when the probe looks. pg_sleep parks in the executor, the same place a
	// real wedge is observed from.
	sleeper := exec.Command(psqlBin, e.psqlArgs("-X", "-q", "-c",
		"SELECT pg_sleep(30), '"+wedgeProbeMarker+"'")...)
	sleeper.Env = append(os.Environ(), e.psqlEnv()...)
	sleeper.Dir = root
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start background psql: %v", err)
	}
	defer func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	}()

	waitForMarkerInActivity(t, c)

	bundleDir := filepath.Join(t.TempDir(), "bundle")
	report := e.captureWedgeDiagnostics("probe_selftest", bundleDir, 61*time.Second)
	t.Logf("probe report:\n%s", report)

	// (1) The stuck statement is named. This is the datum the killed psql
	// client destroys and the whole probe exists to recover.
	if !strings.Contains(report, wedgeProbeMarker) {
		t.Errorf("probe report does not name the in-flight statement (%q missing):\n%s",
			wedgeProbeMarker, report)
	}
	// (2) The distinction between "one stuck backend" and "server gone".
	if !strings.Contains(report, "server responds to SELECT 1: true") {
		t.Errorf("probe did not record server liveness as true:\n%s", report)
	}
	// (3) The goroutine dump reached the real server: its stacks must contain
	// goopg server frames, not just the pprof handler's own.
	if strings.Contains(report, "goroutines: UNAVAILABLE") {
		t.Errorf("probe could not fetch a goroutine dump from %s:\n%s", pprofAddr, report)
	}
	dump := readBundleFile(t, bundleDir, "goroutines.txt")
	// Match the module import prefix rather than a specific package name: the
	// postmaster/connection code lives in internal/postmaster (there is no
	// internal/server package in this module), and matching the prefix keeps
	// this discriminating even if packages get renamed later. It still tells
	// a real goopg-process capture apart from a dump that is only the pprof
	// handler's own runtime/* frames, which is the whole point of check (3).
	if !strings.Contains(dump, "github.com/goopg/goopg/internal/") {
		t.Errorf("goroutine dump has no goopg server frames (wrong process?); first 400 bytes:\n%s",
			truncate(dump, 400))
	}
	// (4) RSS, which keeps or kills the "GC-thrashing server" hypothesis.
	if strings.Contains(report, "server RSS: UNAVAILABLE") {
		t.Errorf("probe could not read the server's RSS:\n%s", report)
	}
	// (5) The bundle exists on disk for a local repro.
	if got := readBundleFile(t, bundleDir, "pg_stat_activity.txt"); !strings.Contains(got, wedgeProbeMarker) {
		t.Errorf("pg_stat_activity.txt does not contain the in-flight statement:\n%s", got)
	}
}

// TestRegressWedgeProbeStuckFilter covers the "blocked >1 minute" selection on
// a synthetic dump. The live guard above cannot reach that state without
// running for over a minute, and an over-broad filter would bury the wedge's
// goroutine among the server's healthy ones.
func TestRegressWedgeProbeStuckFilter(t *testing.T) {
	dump := strings.Join([]string{
		"goroutine 1 [running]:\nmain.main()\n\t/x/main.go:1 +0x1",
		"goroutine 42 [semacquire, 7 minutes]:\ngoopg.stuck()\n\t/x/lock.go:9 +0x2",
		"goroutine 43 [chan receive]:\ngoopg.healthy()\n\t/x/chan.go:3 +0x3",
		"goroutine 44 [IO wait, 2 minutes]:\ngoopg.alsoStuck()\n\t/x/io.go:4 +0x4",
	}, "\n\n")

	total, stuck := stuckGoroutines(dump)
	if total != 4 {
		t.Errorf("total goroutines = %d, want 4", total)
	}
	if len(stuck) != 2 {
		t.Fatalf("stuck goroutines = %d, want 2 (the two with a minutes-blocked header)", len(stuck))
	}
	if !strings.Contains(stuck[0], "goroutine 42") || !strings.Contains(stuck[1], "goroutine 44") {
		t.Errorf("wrong goroutines selected: %q", stuck)
	}
	// A dump with nothing blocked must select nothing, so a report saying
	// "0 blocked >1 minute" is meaningful rather than a parse failure.
	if _, none := stuckGoroutines("goroutine 1 [running]:\nmain.main()"); len(none) != 0 {
		t.Errorf("healthy dump selected %d stuck goroutines, want 0", len(none))
	}
}

// waitForMarkerInActivity blocks until pg_stat_activity reports the guard's
// long-running statement, so the probe observes a genuinely in-flight query.
func waitForMarkerInActivity(t *testing.T, c *cluster.Cluster) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rows, err := c.Query(ctx, `SELECT query FROM pg_catalog.pg_stat_activity`)
		cancel()
		if err == nil {
			for _, r := range rows {
				if len(r) > 0 && strings.Contains(r[0], wedgeProbeMarker) {
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("background statement never appeared in pg_stat_activity within 30s")
}

func readBundleFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read bundle artefact %s: %v", name, err)
	}
	return string(data)
}
