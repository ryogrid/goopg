package testport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/control"
)

// The regress "suite wedge": one case in an otherwise healthy run sits at the
// 120 s per-case deadline while every other case runs at normal speed.
//
// What the nightlies establish (run 20260806-191958, and 20260802/03/04 before
// it):
//
//   - The wedge MOVES between nights — aggregates/jsonb/misc on 20260802-04,
//     multirangetypes on 20260806 — so it is not a defect of any one case.
//   - It is NOT host overload or a GC-thrashing server, the two hypotheses the
//     action item names. In 20260806-191958 the 232 cases summed to 298.5 s
//     *including* the 120 s wedge, i.e. 178.5 s for the other 231 — faster than
//     the 302 s the same suite takes on this workstation with no wedge at all.
//     The server was healthy immediately before and after.
//   - The wedged case is fast in isolation: multirangetypes completes in 0.18 s
//     standalone at HEAD.
//
// So a single statement hangs indefinitely — past its own 5 s
// `statement_timeout` (ExecuteSQL sets one), which is itself the finding: the
// wait is on a path that never observes the statement deadline.
//
// The blocker to naming it is that the suite kept NO evidence. psql's partial
// output dies with the killed client (framework.RegressResult carries only a
// rationale string), and nothing looked at the server while it was stuck; the
// nightly log holds exactly one line per wedge. The probe below closes that:
// when a case crosses regressWedgeProbeAfter it captures, while the backend is
// STILL stuck, the two things that discriminate the remaining hypotheses —
// pg_stat_activity (which statement, and whether the server still answers at
// all) and a full goroutine dump (where in the server that statement is
// parked: lock wait, channel, mutex, or a spin that never checks ctx).
//
// Design: ci/design/02 §A "Wedge-probe rule".

// regressWedgeProbeAfter is how long one regress case may run before the probe
// captures live diagnostics. Half of regressCaseTimeout: late enough that no
// healthy case reaches it (the slowest non-wedged case in 20260806-191958 was
// `join` at 20.7 s) and early enough that the wedged backend is still parked
// where it hung, with a full minute left before psql is killed.
const regressWedgeProbeAfter = 60 * time.Second

// wedgeProbeQueryTimeout bounds each individual diagnostic query. A wedge that
// has taken the whole server down must not turn the probe itself into a second
// hang — an unanswered probe is evidence too, and is recorded as such.
const wedgeProbeQueryTimeout = 15 * time.Second

// wedgeBundleRoot is where full (unbounded) diagnostic bundles are written.
// The nightly collects only testport/go-test.log, so the probe additionally
// logs a bounded summary through t.Log; the bundle is for a local repro.
func wedgeBundleRoot(repoRoot string) string {
	if v := os.Getenv("GOOPG_REGRESS_WEDGE_DIR"); v != "" {
		return v
	}
	return filepath.Join(repoRoot, "tmp", "regress-wedge")
}

// armWedgeProbe starts a watchdog for one regress case and returns a stop
// function that MUST be called before the subtest returns.
//
// The stop function blocks until an in-flight capture has finished, so every
// t.Log the probe emits is ordered before the subtest completes (logging into a
// finished *testing.T panics).
func (e *ClusterRegressExecutor) armWedgeProbe(t *testing.T, caseName string) func() {
	t.Helper()
	bundleDir := filepath.Join(wedgeBundleRoot(e.RepoRoot), caseName)
	// ExecuteSQL drops the killed client's partial output here; cases run
	// sequentially (no t.Parallel in the suite), so a plain field is safe.
	e.wedgeDir = bundleDir

	done := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(finished)
		select {
		case <-done:
			return
		case <-time.After(regressWedgeProbeAfter):
		}
		t.Logf("WEDGE PROBE: case %q has been running for %s; capturing live diagnostics",
			caseName, time.Since(start).Round(time.Second))
		t.Log(e.captureWedgeDiagnostics(caseName, bundleDir, time.Since(start)))
	}()
	return func() {
		close(done)
		<-finished
	}
}

// captureWedgeDiagnostics collects the live state of a wedged cluster and
// returns a bounded summary suitable for the nightly log. The unbounded
// artefacts are written under bundleDir.
func (e *ClusterRegressExecutor) captureWedgeDiagnostics(caseName, bundleDir string, elapsed time.Duration) string {
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return fmt.Sprintf("WEDGE PROBE: cannot create bundle dir %s: %v", bundleDir, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "WEDGE PROBE REPORT case=%s elapsed=%s bundle=%s\n",
		caseName, elapsed.Round(time.Second), bundleDir)

	// (1) Does the server still answer at all? A wedged *case* with a
	// responsive server means one stuck backend; an unresponsive server means
	// the whole postmaster is gone or saturated. The two need different fixes,
	// and no nightly so far can tell them apart.
	fmt.Fprintf(&b, "server responds to SELECT 1: %v\n", e.isAlive())

	// (2) Which statement is stuck, and what is it waiting on. This is the
	// datum psql's killed client throws away.
	//
	// Every column is cast to text on purpose. goopg's pg_stat_activity emits
	// an EMPTY value for `pid` on its internal sessions (3 of 4 rows on an idle
	// cluster; real PG always reports a pid per row), and an int4 column
	// carrying "" makes any Go driver fail the whole query —
	// `pq: strconv.ParseInt: parsing "": invalid syntax`. psql hides this by
	// rendering the blank, which is why it survived. The probe must not lose
	// its most important section to a defect it is trying to help diagnose;
	// the gap itself is a deferral-ledger row (2026-08-06).
	b.WriteString(e.wedgeSection(bundleDir, "pg_stat_activity", `SELECT pid::text, state::text, wait_event_type::text, wait_event::text, query::text FROM pg_catalog.pg_stat_activity`, 20))
	b.WriteString(e.wedgeSection(bundleDir, "pg_locks", `SELECT locktype::text, relation::text, mode::text, granted::text, pid::text FROM pg_catalog.pg_locks`, 20))

	// (3) Where in the server it is parked. Go prints "N minutes" in a
	// goroutine header only once it has been blocked for over a minute, which
	// after a 60 s probe delay is exactly the wedge's own goroutine(s).
	b.WriteString(e.wedgeGoroutineSection(bundleDir))

	// (4) Resident size, to keep or kill the "GC-thrashing server" hypothesis
	// the action item names (already weakened: the rest of the run is fast).
	b.WriteString(e.wedgeMemorySection())

	// (5) Server log tail — a panic/restart leaves its trace only here.
	b.WriteString(e.wedgeServerLogSection(bundleDir))
	return b.String()
}

// wedgeSection runs one catalog query and renders at most maxRows rows.
func (e *ClusterRegressExecutor) wedgeSection(bundleDir, name, sqlText string, maxRows int) string {
	ctx, cancel := context.WithTimeout(context.Background(), wedgeProbeQueryTimeout)
	defer cancel()
	rows, err := e.Cluster.Query(ctx, sqlText)
	if err != nil {
		// Not a failure of the probe: a catalog query that cannot complete
		// while a case is wedged is itself a strong signal.
		return fmt.Sprintf("%s: UNAVAILABLE (%v)\n", name, err)
	}
	var full strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&full, "%s\n", strings.Join(r, " | "))
	}
	writeWedgeArtifact(bundleDir, name+".txt", full.String())

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d rows):\n", name, len(rows))
	for i, r := range rows {
		if i >= maxRows {
			fmt.Fprintf(&b, "  … %d more rows in %s.txt\n", len(rows)-maxRows, name)
			break
		}
		fmt.Fprintf(&b, "  %s\n", strings.Join(r, " | "))
	}
	return b.String()
}

// wedgeGoroutineSection fetches a full goroutine dump from the server's pprof
// endpoint and summarises the goroutines that have been blocked long enough to
// be the wedge.
func (e *ClusterRegressExecutor) wedgeGoroutineSection(bundleDir string) string {
	if e.PprofAddr == "" {
		return "goroutines: UNAVAILABLE (no pprof address configured for this cluster)\n"
	}
	dump, err := fetchGoroutineDump(e.PprofAddr)
	if err != nil {
		return fmt.Sprintf("goroutines: UNAVAILABLE (%v)\n", err)
	}
	writeWedgeArtifact(bundleDir, "goroutines.txt", dump)

	total, stuck := stuckGoroutines(dump)
	var b strings.Builder
	fmt.Fprintf(&b, "goroutines: %d total, %d blocked >1 minute (full dump in goroutines.txt)\n",
		total, len(stuck))
	for i, s := range stuck {
		if i >= 8 {
			fmt.Fprintf(&b, "  … %d more long-blocked goroutines in goroutines.txt\n", len(stuck)-8)
			break
		}
		b.WriteString("  --- long-blocked goroutine ---\n")
		for j, line := range strings.Split(s, "\n") {
			if j >= 14 {
				b.WriteString("    …\n")
				break
			}
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	return b.String()
}

// stuckGoroutines splits a `debug=2` goroutine dump into stacks and returns the
// total count together with the ones that have been blocked long enough to be
// the wedge.
//
// The Go runtime appends ", N minutes" to a goroutine's header only once it has
// been in the same wait for over a minute. After the probe's 60 s delay that
// selects exactly the goroutine(s) that stopped making progress when the case
// hung, and excludes the server's many healthy long-lived-but-active workers.
func stuckGoroutines(dump string) (total int, stuck []string) {
	for _, s := range strings.Split(dump, "\n\n") {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "goroutine ") {
			continue
		}
		total++
		header, _, _ := strings.Cut(s, "\n")
		if strings.Contains(header, "minutes]") {
			stuck = append(stuck, s)
		}
	}
	return total, stuck
}

// wedgeMemorySection reports the server's resident size from /proc.
func (e *ClusterRegressExecutor) wedgeMemorySection() string {
	pf, err := control.ParsePIDFile(e.Cluster.DataDir())
	if err != nil {
		return fmt.Sprintf("server RSS: UNAVAILABLE (pid file: %v)\n", err)
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pf.PID))
	if err != nil {
		return fmt.Sprintf("server RSS: UNAVAILABLE (proc: %v)\n", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") || strings.HasPrefix(line, "VmSwap:") {
			return fmt.Sprintf("server pid=%d %s\n", pf.PID, strings.Join(strings.Fields(line), " "))
		}
	}
	return fmt.Sprintf("server pid=%d RSS: not reported by /proc\n", pf.PID)
}

// wedgeServerLogSection copies the tail of the cluster log into the bundle and
// summarises its last lines.
func (e *ClusterRegressExecutor) wedgeServerLogSection(bundleDir string) string {
	const tailBytes = 64 << 10
	data, err := os.ReadFile(e.Cluster.LogPath())
	if err != nil {
		return fmt.Sprintf("server log: UNAVAILABLE (%v)\n", err)
	}
	if len(data) > tailBytes {
		data = data[len(data)-tailBytes:]
	}
	writeWedgeArtifact(bundleDir, "server-log-tail.txt", string(data))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return "server log (last 10 lines, tail in server-log-tail.txt):\n  " + strings.Join(lines, "\n  ") + "\n"
}

// fetchGoroutineDump pulls debug=2 (full stacks, not aggregated) from the
// server's pprof endpoint.
func fetchGoroutineDump(addr string) (string, error) {
	client := &http.Client{Timeout: wedgeProbeQueryTimeout}
	resp, err := client.Get("http://" + addr + "/debug/pprof/goroutine?debug=2")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pprof returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func writeWedgeArtifact(dir, name, content string) {
	if dir == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// reserveLoopbackPort returns a currently-free loopback port for the cluster's
// pprof endpoint. The server's default is a fixed 127.0.0.1:6060, which any
// concurrently running goopg (a bench cluster, a peer test) may already hold —
// in which case the server logs at Debug and silently has no endpoint, and the
// probe would have no goroutine dump exactly when it is needed.
func reserveLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
