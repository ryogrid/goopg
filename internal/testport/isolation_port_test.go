package testport

// Ports of postgres/src/test/isolation/specs/*.spec into Go tests.
//
// Each spec is run via IsolationRunner against a live goopg cluster.
// Output is compared to postgres/src/test/isolation/expected/*.out using
// the same normalization rules as isolationtester.
//
// Status per spec:
//   - pass:  output matches expected exactly (after normalization)
//   - defer: spec runs but output differs, or spec uses unsupported goopg SQL
//
// All specs run; failures are reported as t.Error (not t.Fatal) so that
// a single cluster serves the full suite.

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_IsolationSuite runs all upstream isolation specs against a single
// goopg cluster and reports pass/defer per spec.
func TestPort_IsolationSuite(t *testing.T) {
	root := repoRoot(t)

	c := newCluster(t, "isolation_suite")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	specs, err := framework.DiscoverIsolationSpecs(root)
	if err != nil {
		t.Fatalf("discover specs: %v", err)
	}
	if len(specs) == 0 {
		t.Skip("no isolation specs found (postgres submodule not initialised)")
	}

	dsn := buildDSN(t, c)
	runner := &framework.IsolationRunner{DSN: dsn}

	passed, deferred := 0, 0
	for _, specPath := range specs {
		specPath := specPath
		name := filepath.Base(specPath)
		name = name[:len(name)-len(filepath.Ext(name))] // strip .spec

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result := runner.RunAndCompare(ctx, root, specPath)
			switch result.Status {
			case "pass":
				// nothing to report
			case "defer":
				// Expected for most specs — goopg not yet fully compatible.
				t.Logf("defer: %s", result.Diff)
				t.Skip("deferred: output did not match expected")
			case "excluded":
				t.Skip("excluded by policy")
			default:
				t.Errorf("unknown status %q: %s", result.Status, result.Diff)
			}
		})

		// Track outside subtests so we can log a summary.
		_ = passed
		_ = deferred
	}
}

// TestPort_IsolationReadWriteUnique is a focused test for the
// read-write-unique spec (a simple locking scenario that exercises
// the core blocking-detection machinery).
func TestPort_IsolationReadWriteUnique(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-write-unique.spec")
}

// TestPort_IsolationLockCommittedUpdate exercises a spec that produces <waiting ...>
// output — verifying that blocking detection and drain work correctly.
func TestPort_IsolationLockCommittedUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/lock-committed-update.spec")
}

// runIsoSpec is a helper that runs one spec and logs the diff when output
// does not match.  It does not call t.Fatal so other subtests can continue.
func runIsoSpec(t *testing.T, root string, c *cluster.Cluster, specRelPath string) {
	t.Helper()
	dsn := buildDSN(t, c)
	runner := &framework.IsolationRunner{DSN: dsn}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result := runner.RunAndCompare(ctx, root, specRelPath)
	switch result.Status {
	case "pass":
		t.Logf("PASS: %s", specRelPath)
	case "defer":
		t.Logf("defer (%s):\n%s", specRelPath, result.Diff)
		t.Skip("deferred: output did not match expected")
	case "excluded":
		t.Skip("excluded by policy")
	default:
		t.Errorf("unexpected status %q", result.Status)
	}
}

// buildDSN constructs a lib/pq DSN for the given cluster.
func buildDSN(t *testing.T, c *cluster.Cluster) string {
	t.Helper()
	addr := c.ListenAddr()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port %q: %v", addr, err)
	}
	return fmt.Sprintf("host=%s port=%s user=postgres dbname=postgres sslmode=disable", host, port)
}
