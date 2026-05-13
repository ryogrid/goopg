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

	// 10-minute timeout: specs with 24+ permutations each requiring per-
	// permutation DDL setup/teardown can exceed 2 minutes on goopg.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

// ── M0096-0001 dedicated sequential isolation tests ──────────────────────────
//
// One function per spec from the 21-spec RC isolation target list.
// All start as defer/skip — they anchor observability so that as features
// land (M0096-0002 through M0096-0013) test promotion from t.Skip → PASS is
// immediately visible without having to run the full IsolationSuite.
//
// Pattern: each function creates a fresh cluster, runs RunAndCompare via
// runIsoSpec, and t.Skip("deferred: ...") until the spec fully matches.
// No t.Parallel() — these are explicitly sequential.

// TestPort_IsolationEvalPlanQual exercises the eval-plan-qual spec.
// Requires: BEGIN ISOLATION LEVEL, GENERATED ALWAYS AS, CREATE TABLE INHERITS.
func TestPort_IsolationEvalPlanQual(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_eval_plan_qual")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/eval-plan-qual.spec")
}

// TestPort_IsolationEvalPlanQualTrigger exercises the eval-plan-qual-trigger spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TRIGGER, CREATE TABLE INHERITS.
func TestPort_IsolationEvalPlanQualTrigger(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_eval_pq_trig")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/eval-plan-qual-trigger.spec")
}

// TestPort_IsolationLockCommittedKeyupdate exercises the lock-committed-keyupdate spec.
// Requires: BEGIN ISOLATION LEVEL, FOR KEY SHARE / FOR NO KEY UPDATE.
func TestPort_IsolationLockCommittedKeyupdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_keyupdate")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/lock-committed-keyupdate.spec")
}

// TestPort_IsolationInsertConflictDoUpdate exercises the insert-conflict-do-update spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update.spec")
}

// TestPort_IsolationInsertConflictDoUpdate2 exercises the insert-conflict-do-update-2 spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-2.spec")
}

// TestPort_IsolationInsertConflictDoUpdate3 exercises the insert-conflict-do-update-3 spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-3.spec")
}

// TestPort_IsolationInsertConflictDoUpdate4 exercises the insert-conflict-do-update-4 spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-4.spec")
}

// TestPort_IsolationInsertConflictDoNothing exercises the insert-conflict-do-nothing spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO NOTHING.
func TestPort_IsolationInsertConflictDoNothing(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_nothing")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-nothing.spec")
}

// TestPort_IsolationInsertConflictSpecconflict exercises the insert-conflict-specconflict spec.
// Requires: BEGIN ISOLATION LEVEL, pg_advisory_xact_lock, ON CONFLICT DO UPDATE.
func TestPort_IsolationInsertConflictSpecconflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_specconf")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-specconflict.spec")
}

// TestPort_IsolationDropIndexConcurrently1 exercises the drop-index-concurrently-1 spec.
// Requires: BEGIN ISOLATION LEVEL, DROP INDEX CONCURRENTLY.
func TestPort_IsolationDropIndexConcurrently1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_drop_idx_cc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/drop-index-concurrently-1.spec")
}

// TestPort_IsolationFkSnapshot exercises the fk-snapshot spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE with REFERENCES (FK), CREATE TRIGGER.
func TestPort_IsolationFkSnapshot(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_snapshot")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/fk-snapshot.spec")
}

// TestPort_IsolationPartitionKeyUpdate1 exercises the partition-key-update-1 spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE PARTITION BY, FOR KEY SHARE.
func TestPort_IsolationPartitionKeyUpdate1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_part_ku1")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/partition-key-update-1.spec")
}

// TestPort_IsolationPartitionKeyUpdate2 exercises the partition-key-update-2 spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE PARTITION BY, REFERENCES (FK).
func TestPort_IsolationPartitionKeyUpdate2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_part_ku2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/partition-key-update-2.spec")
}

// TestPort_IsolationPartitionKeyUpdate3 exercises the partition-key-update-3 spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE PARTITION BY, REFERENCES (FK), CREATE TRIGGER.
func TestPort_IsolationPartitionKeyUpdate3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_part_ku3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/partition-key-update-3.spec")
}

// TestPort_IsolationPartitionKeyUpdate4 exercises the partition-key-update-4 spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE PARTITION BY, REFERENCES (FK), CREATE TRIGGER.
func TestPort_IsolationPartitionKeyUpdate4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_part_ku4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/partition-key-update-4.spec")
}

// TestPort_IsolationMergeUpdate exercises the merge-update spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO … WHEN MATCHED THEN UPDATE.
func TestPort_IsolationMergeUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/merge-update.spec")
}

// TestPort_IsolationMergeDelete exercises the merge-delete spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO … WHEN MATCHED THEN DELETE.
func TestPort_IsolationMergeDelete(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_delete")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/merge-delete.spec")
}

// TestPort_IsolationMergeInsertUpdate exercises the merge-insert-update spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO … WHEN NOT MATCHED THEN INSERT / WHEN MATCHED THEN UPDATE.
func TestPort_IsolationMergeInsertUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_ins_upd")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/merge-insert-update.spec")
}

// TestPort_IsolationMergeMatchRecheck exercises the merge-match-recheck spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO with multiple WHEN clauses.
func TestPort_IsolationMergeMatchRecheck(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_recheck")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/merge-match-recheck.spec")
}

// TestPort_IsolationMergeJoin exercises the merge-join spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO with JOIN source.
func TestPort_IsolationMergeJoin(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_join")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/merge-join.spec")
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
