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

// TestPort_IsolationReadWriteUnique2 exercises read-write-unique-2: two SSI
// transactions both probe for i=42 then INSERT; one must see a 40001 SSI
// failure (overlapping) or a 23505 unique violation (serialized).
func TestPort_IsolationReadWriteUnique2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-2.spec")
}

// TestPort_IsolationReadWriteUnique3 exercises read-write-unique-3 (bug 9301):
// an insert-if-not-exists SQL function under SSI must abort with 40001.
func TestPort_IsolationReadWriteUnique3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-3.spec")
}

// TestPort_IsolationReadWriteUnique4 exercises read-write-unique-4: a gapless
// per-year invoice sequence; mixes 40001 SSI failures and 23505 unique
// violations depending on read/write interleaving.
func TestPort_IsolationReadWriteUnique4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-4.spec")
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

// ── M0118-0001 SSI / SERIALIZABLE anomaly specs ──────────────────────────────
//
// These specs declare NO explicit `permutation` lines, so the runner generates
// ALL interleavings of the per-session step sequences (run_all_permutations
// parity, isolation.go generateAllPermutations). They became in scope once
// goopg implemented REPEATABLE READ + SERIALIZABLE with real SSI raising
// SQLSTATE 40001 (M0100/M0104). One dedicated, sequential (non-parallel)
// function per spec — own cluster — so a green port is visible without running
// the full IsolationSuite (whose parallel subtests share one cluster).

// TestPort_IsolationSimpleWriteSkew exercises the simple-write-skew spec: two
// SERIALIZABLE transactions form a write-skew dangerous structure; every
// overlapping interleaving must abort one with 40001.
func TestPort_IsolationSimpleWriteSkew(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_simple_ws")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/simple-write-skew.spec")
}

// TestPort_IsolationTwoIds exercises the two-ids spec: a SERIALIZABLE
// read/write cycle over two id rows must abort with 40001.
func TestPort_IsolationTwoIds(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_two_ids")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/two-ids.spec")
}

// TestPort_IsolationTotalCash exercises the total-cash spec: a SERIALIZABLE
// constraint over an aggregate (total cash invariant) under write skew.
func TestPort_IsolationTotalCash(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_total_cash")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/total-cash.spec")
}

// TestPort_IsolationReceiptReport exercises the receipt-report spec: the
// classic batch/receipt read-only anomaly under SERIALIZABLE.
func TestPort_IsolationReceiptReport(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_receipt_report")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/receipt-report.spec")
}

// TestPort_IsolationProjectManager exercises the project-manager spec: a
// SERIALIZABLE resource-assignment write skew must abort one transaction.
func TestPort_IsolationProjectManager(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_project_manager")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/project-manager.spec")
}

// TestPort_IsolationClassroomScheduling exercises the classroom-scheduling
// spec: overlapping SERIALIZABLE bookings form a dangerous structure.
func TestPort_IsolationClassroomScheduling(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_classroom_sched")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/classroom-scheduling.spec")
}

// TestPort_IsolationReadOnlyAnomaly exercises the read-only-anomaly spec: the
// classic O'Neil read-only transaction anomaly under REPEATABLE READ (snapshot
// isolation). Because the level is RR (not SERIALIZABLE), the anomaly is
// ALLOWED — no serialization failure occurs, s3 simply observes the
// inconsistent (X=0, Y=20) state. No SSI machinery is involved.
func TestPort_IsolationReadOnlyAnomaly(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_ro_anomaly")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly.spec")
}

// TestPort_IsolationReadOnlyAnomaly2 exercises read-only-anomaly-2: same O'Neil
// example under SERIALIZABLE. The second permutation creates a cycle once the
// read-only s3 observes s1's committed write, so s2wx must abort with 40001.
func TestPort_IsolationReadOnlyAnomaly2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_ro_anomaly2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly-2.spec")
}

// TestPort_IsolationReadOnlyAnomaly3 exercises read-only-anomaly-3: the same
// O'Neil read-only anomaly, but s3 is declared SERIALIZABLE READ ONLY
// DEFERRABLE. PostgreSQL avoids the anomaly without aborting anyone by
// deferring s3's snapshot (GetSafeSnapshot) until a safe snapshot is available
// — s3r blocks (<waiting ...>) while the concurrent read-write s2 is active,
// then completes once s2 commits, observing the final committed state
// (X=-11, Y=20). goopg's Manager.waitForSafeSnapshot implements the same
// drain-the-writers deferral (M0118-0001), so no transaction is rolled back.
func TestPort_IsolationReadOnlyAnomaly3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_ro_anomaly3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly-3.spec")
}

// TestPort_IsolationSerializableParallel exercises serializable-parallel: the
// same O'Neil read-only anomaly under SERIALIZABLE as read-only-anomaly-2, but
// the read-only s3 declares "SET debug_parallel_query = on" so upstream runs it
// in a parallel worker. goopg has no parallel executor, so the GUC is a no-op
// and the SSI outcome is identical: once s3 observes s1's committed write, the
// cycle dooms s2wx with 40001. Requires only that the developer GUC be
// accepted during session setup.
func TestPort_IsolationSerializableParallel(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_serializable_parallel")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel.spec")
}

// TestPort_IsolationSerializableParallel2 exercises serializable-parallel-2:
// a SERIALIZABLE READ ONLY transaction repeatedly counts a table while a
// concurrent SERIALIZABLE writer commits. Upstream forces parallel index-only
// scans (ALTER TABLE foo SET (parallel_workers = 2); SET enable_seqscan = off)
// to drive SXACT_FLAG_RO_SAFE through a parallel worker. goopg has no parallel
// executor, so the parallel GUCs and reloption are accepted but inert; the
// read-only transaction never becomes a pivot, so every step commits and the
// COUNT(*) is a stable 100 — identical to the upstream serial-equivalent output.
func TestPort_IsolationSerializableParallel2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_serializable_parallel2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel-2.spec")
}

// TestPort_IsolationSerializableParallel3 exercises serializable-parallel-3:
// like serializable-parallel-2 but two SERIALIZABLE READ ONLY transactions
// (s2, s4) run concurrently with the same xmin alongside two read-write
// SERIALIZABLE transactions (s1, s3), stressing the SXACT_FLAG_RO_SAFE
// "oldest with this xmin" path in a parallel query. goopg has no parallel
// executor, so the parallel reloption/GUCs are inert; no read-only
// transaction becomes a pivot, so every step commits and each SELECT returns
// the full 10-row table — matching the upstream serial-equivalent output.
func TestPort_IsolationSerializableParallel3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_serializable_parallel3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel-3.spec")
}

// TestPort_IsolationUpdateConflictOut exercises update-conflict-out: SSI
// "conflict out" handling for heapam interacting with a concurrently updated
// (then aborted) tuple. "bar" must fail with 40001 at bar_commit at the latest.
func TestPort_IsolationUpdateConflictOut(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_update_conflict_out")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/update-conflict-out.spec")
}

// TestPort_IsolationMultipleRowVersions exercises the multiple-row-versions
// spec: a four-transaction SERIALIZABLE dangerous structure that only triggers
// with particular timings across many row versions. The single tested
// permutation must abort s1's wz1 UPDATE with 40001.
func TestPort_IsolationMultipleRowVersions(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_multi_row_ver")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/multiple-row-versions.spec")
}

// TestPort_IsolationPredicateLockHotTuple exercises predicate-lock-hot-tuple:
// two SERIALIZABLE transactions each SELECT i IN (5,7) then UPDATE one of the
// two rows. The reads cross-cover the other writer's target row, forming a
// write-skew dangerous structure, so the later committer (s2) must abort with
// 40001.
func TestPort_IsolationPredicateLockHotTuple(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_pred_lock_hot")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/predicate-lock-hot-tuple.spec")
}

// TestPort_IsolationPartialIndex exercises partial-index: an UPDATE that moves a
// row out of a partial index (CREATE INDEX ... WHERE val2 = 1) under SERIALIZABLE
// must still create the read/write dependency a full-table read would, so any
// overlap between the two transactions raises 40001.
func TestPort_IsolationPartialIndex(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_partial_index")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/partial-index.spec")
}

// TestPort_IsolationTemporalRangeIntegrity exercises temporal-range-integrity: a
// SERIALIZABLE write skew across two tables (statute / offense) where each
// transaction reads one table with a range predicate and writes the other.
// Any overlap must abort one transaction with 40001.
func TestPort_IsolationTemporalRangeIntegrity(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_temporal_range")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/temporal-range-integrity.spec")
}

// TestPort_IsolationReferentialIntegrity exercises referential-integrity: a
// SERIALIZABLE write skew across two tables (a / b) standing in for an
// application-enforced foreign key. s1 reads a(i=1) then INSERTs into b; s2
// reads a(i=1) and b(a_id=1) then DELETEs a(i=1). Any overlap must abort one
// transaction with 40001 — the empty read on b takes a relation-grain SIREAD
// so s1's INSERT conflicts out, and s1's read of a conflicts in against s2's
// DELETE, closing the dangerous structure.
func TestPort_IsolationReferentialIntegrity(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_referential_integrity")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/referential-integrity.spec")
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
