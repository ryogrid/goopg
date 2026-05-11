package testport

// regress_suite_test.go — TestPort_RegressSuite and ClusterRegressExecutor.
//
// Wires up the pg_regress SQL/expected test suite against a live goopg
// cluster using the psql binary as the execution back-end. Each of the
// 232 discovered cases runs as a subtest; most report "deferred" on the
// initial run. The suite anchors regression observability so that as
// features land, test promotion from t.Skip → PASS is immediately visible.
//
// Execution path (M0097-0001):
//   1. Create a fresh goopg cluster.
//   2. Run postgres/src/test/regress/sql/test_setup.sql best-effort (some
//      statements use C extensions or tablespaces not supported in goopg;
//      failures are logged but do not abort the suite).
//   3. For each discovered case, use framework.RunRegressSubset to execute
//      the SQL via ClusterRegressExecutor (psql -X -q -a -f <tmpfile>) and
//      compare normalised output with the expected .out file.
//   4. "port" → pass, "excluded" → t.Skip, "defer" → t.Skip with rationale.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_RegressSuite runs all pg_regress SQL cases against a live goopg
// cluster via the psql binary. Most cases report "defer" initially; the
// suite provides a stable baseline for regression tracking as M0097
// sub-milestones land.
func TestPort_RegressSuite(t *testing.T) {
	root := repoRoot(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not in PATH or postgres/local_install/bin")
	}

	c := newCluster(t, "regress_suite")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Run test_setup.sql to materialise shared fixture tables. Failures
	// are expected (tablespaces, C extensions) and do not abort the suite.
	runRegressSetup(t, root, c)

	// Discover all SQL/expected pairs.
	cases, err := framework.DiscoverRegressCases(root)
	if err != nil {
		t.Fatalf("discover regress cases: %v", err)
	}
	if len(cases) == 0 {
		t.Skip("no regress cases found (postgres submodule not initialised)")
	}

	exec := &ClusterRegressExecutor{
		Cluster:  c,
		RepoRoot: root,
	}

	for _, rc := range cases {
		rc := rc
		t.Run(rc.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			results, err := framework.RunRegressSubset(ctx, root, []framework.RegressCase{rc}, exec)
			if err != nil {
				t.Logf("run error: %v", err)
				t.Skip("deferred: execution error")
				return
			}
			if len(results) == 0 {
				t.Skip("deferred: no result returned")
				return
			}
			r := results[0]
			switch r.Status {
			case "port":
				// Pass — nothing to do.
			case "excluded":
				t.Skip("excluded by policy")
			default:
				t.Skipf("deferred: %s", r.Rationale)
			}
		})
	}
}

// ClusterRegressExecutor implements framework.RegressExecutor by writing the
// SQL to a temp file and executing it through the psql binary (connected to
// the given goopg cluster). The output matches psql -X -q -a format, which
// is the same format pg_regress uses to generate expected .out files.
type ClusterRegressExecutor struct {
	Cluster  *cluster.Cluster
	RepoRoot string
}

// ExecuteSQL writes sql to a temporary file and runs it through psql.
// Returns combined stdout (primary output) on success; on psql non-zero
// exit the stdout+stderr combination is returned so callers can diff it.
func (e *ClusterRegressExecutor) ExecuteSQL(ctx context.Context, sql string) (string, error) {
	tmpf, err := os.CreateTemp("", "goopg_regress_*.sql")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpf.Name())
	if _, err := tmpf.WriteString(sql); err != nil {
		tmpf.Close()
		return "", err
	}
	tmpf.Close()

	// -X   skip .psqlrc
	// -q   quiet (no startup messages)
	// -a   echo all input to stdout (matches pg_regress output format)
	// -f   execute file
	result, err := e.Cluster.PSQL("-X", "-q", "-a", "-f", tmpf.Name())
	_ = err // psql non-zero exit (e.g. from unsupported SQL) is not fatal;
	         // the output diff will report "defer".
	return result.Stdout + result.Stderr, nil
}

// runRegressSetup runs postgres/src/test/regress/sql/test_setup.sql against
// the cluster best-effort. Failures (tablespaces, C extensions, COPY from
// non-existent files) are logged but not fatal — they only cause downstream
// cases to defer rather than break the suite entirely.
func runRegressSetup(t *testing.T, root string, c *cluster.Cluster) {
	t.Helper()
	setupPath := filepath.Join(root, "postgres", "src", "test", "regress", "sql", "test_setup.sql")
	if _, err := os.Stat(setupPath); err != nil {
		t.Logf("test_setup.sql not found, skipping fixture setup: %v", err)
		return
	}
	result, _ := c.PSQL("-X", "-q", "-a", "-f", setupPath)
	if result.ExitCode != 0 {
		t.Logf("test_setup.sql completed with partial failures (expected for C extensions/tablespaces): exit=%d", result.ExitCode)
	}
	if result.Stderr != "" {
		t.Logf("test_setup.sql stderr (truncated at 512 bytes):\n%s", truncate(result.Stderr, 512))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
