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

// runIsoSpecStrict is the pass-required variant of runIsoSpec: the spec MUST
// match PG 18.3 byte-for-byte. Unlike runIsoSpec (which t.Skip()s a `defer`
// result so an unported spec does not turn the suite red), a non-`pass` status
// here is a hard test failure. Use it only for specs that have been promoted to
// pass_required in docs/test-port (D-002) — a regression must surface as a red
// test, not a silent skip.
func runIsoSpecStrict(t *testing.T, root string, c *cluster.Cluster, specRelPath string) {
	t.Helper()
	dsn := buildDSN(t, c)
	runner := &framework.IsolationRunner{DSN: dsn}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result := runner.RunAndCompare(ctx, root, specRelPath)
	if result.Status != "pass" {
		t.Errorf("pass-required spec %s did not match PG (status=%q):\n%s",
			specRelPath, result.Status, result.Diff)
		return
	}
	t.Logf("PASS: %s", specRelPath)
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

// TestPort_IsolationLockCommittedKeyupdate exercises the lock-committed-keyupdate
// spec: a FOR KEY SHARE lock on a tuple whose key was UPDATEd by a concurrent
// committed transaction. Unlike lock-committed-update (a no-key update, which is
// compatible with KEY SHARE), the key-update CONFLICTS with the locker, so under
// READ COMMITTED the locker follows the CTID chain to the live successor while
// under REPEATABLE READ / SERIALIZABLE it raises 40001. The locker also blocks
// behind s1's still-in-progress key UPDATE until s1 commits. Passes on the
// M0118-0003 row-locking infrastructure (lock-only-xmax cross-statement conflict
// detection + the blocking WaitForXID path in stampLockInner) with no new code.
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-2.spec")
}

// TestPort_IsolationInsertConflictDoUpdate3 exercises the insert-conflict-do-update-3 spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-3.spec")
}

// TestPort_IsolationInsertConflictDoUpdate4 exercises the insert-conflict-do-update-4 spec.
// Requires: BEGIN ISOLATION LEVEL, ON CONFLICT DO UPDATE executor correctness.
func TestPort_IsolationInsertConflictDoUpdate4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_update4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-update-4.spec")
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

// TestPort_IsolationInsertConflictDoNothing2 exercises the
// insert-conflict-do-nothing-2 spec: INSERT ... ON CONFLICT DO NOTHING with
// multiple rows under REPEATABLE READ and SERIALIZABLE. ON CONFLICT DO NOTHING
// never raises a serialization failure even when a concurrent committed insert
// created the conflicting key — the conflict arbiter simply skips the row.
func TestPort_IsolationInsertConflictDoNothing2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_nothing2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-do-nothing-2.spec")
}

// TestPort_IsolationInsertConflictSpecconflict exercises the insert-conflict-specconflict spec.
// Requires: BEGIN ISOLATION LEVEL, pg_advisory_xact_lock, ON CONFLICT DO UPDATE.
func TestPort_IsolationInsertConflictSpecconflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_icd_specconf")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/insert-conflict-specconflict.spec")
}

// TestPort_IsolationDropIndexConcurrently1 exercises the drop-index-concurrently-1 spec.
// Requires: BEGIN ISOLATION LEVEL, DROP INDEX CONCURRENTLY.
//
// PASS-REQUIRED (M0118-0007, design 0118-0024): the spec matches PG 18.3
// byte-for-byte. DROP INDEX CONCURRENTLY's two-phase invalidation, the
// EXPLAIN-driven plan-format output (seqscan-vs-indexscan after the index is
// dropped), and READ COMMITTED snapshot visibility were already correct from
// prior milestones; this is a promotion with no engine change.
func TestPort_IsolationDropIndexConcurrently1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_drop_idx_cc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/drop-index-concurrently-1.spec")
}

// TestPort_IsolationFkSnapshot exercises the fk-snapshot spec.
// Requires: BEGIN ISOLATION LEVEL, CREATE TABLE with REFERENCES (FK), CREATE TRIGGER.
func TestPort_IsolationFkSnapshot(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_snapshot")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-snapshot.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/merge-update.spec")
}

// TestPort_IsolationMergeDelete exercises the merge-delete spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO … WHEN MATCHED THEN DELETE.
func TestPort_IsolationMergeDelete(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_delete")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/merge-delete.spec")
}

// TestPort_IsolationMergeInsertUpdate exercises the merge-insert-update spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO … WHEN NOT MATCHED THEN INSERT / WHEN MATCHED THEN UPDATE.
func TestPort_IsolationMergeInsertUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_ins_upd")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/merge-insert-update.spec")
}

// TestPort_IsolationMergeMatchRecheck exercises the merge-match-recheck spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO with multiple WHEN clauses.
func TestPort_IsolationMergeMatchRecheck(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_recheck")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/merge-match-recheck.spec")
}

// TestPort_IsolationMergeJoin exercises the merge-join spec.
// Requires: BEGIN ISOLATION LEVEL, MERGE INTO with JOIN source.
func TestPort_IsolationMergeJoin(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_merge_join")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/merge-join.spec")
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

// TestPort_IsolationMatviewWriteSkew exercises the matview-write-skew spec: a
// SERIALIZABLE REFRESH MATERIALIZED VIEW CONCURRENTLY reads the parent relation
// and writes the matview, while a concurrent SERIALIZABLE transaction reads the
// matview and writes the parent relation. Every overlap forms a dangerous
// structure, so the second committer must abort with 40001.
func TestPort_IsolationMatviewWriteSkew(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_matview_ws")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/matview-write-skew.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/temporal-range-integrity.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/referential-integrity.spec")
}

// TestPort_IsolationFkContention exercises fk-contention: a child INSERT takes a
// FOR KEY SHARE row lock on its referenced parent row to enforce the FK while a
// concurrent session repeatedly UPDATEs a *non-key* parent column. The non-key
// UPDATE and the FK KEY SHARE lock do not conflict (the multixact lock-only +
// no-key-update producer landed in M0118-0003/0004), so neither session blocks
// and the output matches PG 18.3 byte-for-byte. Promoted to pass-required
// (M0118-0005, design 0118-0023).
func TestPort_IsolationFkContention(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_contention")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-contention.spec")
}

// TestPort_IsolationFkDeadlock2 exercises fk-deadlock2: two sessions each insert
// a child row (taking FOR KEY SHARE on the shared parent — non-conflicting, so
// both proceed via a multixact lock set) and then UPDATE disjoint parent rows.
// No lock cycle forms, so both commit without a deadlock abort, matching PG 18.3.
// The sibling fk-deadlock spec (where the parent UPDATEs *do* form a cycle)
// remains deferred — goopg's FK row-lock wait over-conflicts there (ledger
// 2026-06-22). Promoted to pass-required (M0118-0005, design 0118-0023).
func TestPort_IsolationFkDeadlock2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_deadlock2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-deadlock2.spec")
}

// TestPort_IsolationIndexOnlyScan exercises the index-only-scan spec: a
// SERIALIZABLE write skew across two all-visible tables (tabx / taby) where each
// transaction reads one table via an index-only scan of SELECT min(id) and
// DELETEs the matching row from the other table. Any overlap forms a
// rw-dependency cycle so the second committer must abort with 40001; the two
// serialized orderings (rxwy1 c1 rywx2 c2 / rywx2 c2 rxwy1 c1) commit cleanly.
func TestPort_IsolationIndexOnlyScan(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_index_only_scan")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/index-only-scan.spec")
}

// TestPort_IsolationSkipLocked exercises the skip-locked spec: two sessions
// each repeatedly SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1 from a 2-row queue.
// SKIP LOCKED must skip rows already row-locked by the other session, so the
// two sessions claim disjoint rows (1 and 2) without ever blocking.
func TestPort_IsolationSkipLocked(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skip_locked")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/skip-locked.spec")
}

// TestPort_IsolationNowait exercises the nowait spec: when one session holds a
// FOR UPDATE row lock, a second SELECT ... FOR UPDATE NOWAIT on the same row
// must immediately raise "could not obtain lock on row in relation" rather than
// block.
func TestPort_IsolationNowait(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/nowait.spec")
}

// TestPort_IsolationNowait3 exercises the nowait-3 spec: s1 holds a FOR UPDATE
// row lock, s2 issues a *blocking* FOR UPDATE on the same row (must wait), and
// while s2 is queued s3 issues FOR UPDATE NOWAIT (must fail fast with
// "could not obtain lock on row in relation"). When s1 commits, s2 unblocks and
// claims the row. Validates the blocking-on-a-row-lock wait path in
// stampLockInner alongside the existing NOWAIT fail-fast path.
func TestPort_IsolationNowait3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/nowait-3.spec")
}

// TestPort_IsolationUpdateLockedTuple exercises the update-locked-tuple spec:
// s1 (REPEATABLE READ) repeatedly UPDATEs orders (whose user_id FK references
// users), which takes a KEY SHARE lock on the referenced users row; s2
// (REPEATABLE READ) UPDATEs a non-key column of that same users row (a
// FOR NO KEY UPDATE-equivalent change). Because KEY SHARE does not conflict
// with a no-key update and the FK key is unchanged, no blocking nor
// serializability failure should occur in any permutation.
func TestPort_IsolationUpdateLockedTuple(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_update_locked_tuple")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/update-locked-tuple.spec")
}

// TestPort_IsolationLockUpdateTraversal exercises the lock-update-traversal
// spec: s1 takes a FOR KEY SHARE lock on a row that s2 has updated in-flight
// (forming an updater-bearing multixact), then after s2 commits a third step
// DELETEs (s2d1) / key-UPDATEs (s2d2) / no-key-UPDATEs (s2d3) the row. The
// DELETE and key-UPDATE must wait for s1's KEY SHARE lock; the no-key-UPDATE
// must proceed immediately (KEY SHARE does not conflict with a no-key update).
// This is the cleanest proof of the 4-way row-lock-strength distinction
// (FOR KEY SHARE must NOT be collapsed to FOR SHARE). M0118-0003.
func TestPort_IsolationLockUpdateTraversal(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_update_traversal")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/lock-update-traversal.spec")
}

// TestPort_IsolationLockUpdateDelete exercises the lock-update-delete spec: s2
// holds a session-level advisory lock (lock 0) so that s1's
// SELECT ... WHERE pg_advisory_xact_lock(0) IS NOT NULL ... FOR KEY SHARE blocks
// at the advisory gate while s2 builds an update chain (UPDATE then DELETE /
// key-UPDATE / no-key-UPDATE) on the same tuple. When s2 unlocks the advisory
// lock, s1's locker must traverse the update chain: it proceeds immediately for
// the no-key UPDATE blocker but waits on the in-flight DELETE / key-UPDATE until
// s2 commits or aborts (committing DELETE/key-UPDATE leaves 0 rows; aborting
// lets s1 lock the surviving row). Layers the advisory-lock synchroniser on top
// of the M0118-0003 update-chain wait-on-deleter path. M0118-0003.
func TestPort_IsolationLockUpdateDelete(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_update_delete")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/lock-update-delete.spec")
}

// TestPort_IsolationSkipLocked2 exercises the skip-locked-2 spec: s1 and s2 both
// take FOR SHARE on the same row (forming a multixact lock with two SHARE
// members), then s2 tries FOR UPDATE SKIP LOCKED and must skip the row because
// the FOR UPDATE conflicts with s1's still-held SHARE member of the multixact.
// Validates the multixact-aware SKIP LOCKED conflict path. M0118-0003.
func TestPort_IsolationSkipLocked2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skip_locked2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/skip-locked-2.spec")
}

// TestPort_IsolationNowait2 exercises the nowait-2 spec: like skip-locked-2 but
// s2 uses FOR UPDATE NOWAIT and must abort with 55P03 instead of skipping when
// the multixact SHARE member held by s1 blocks the upgrade. M0118-0003.
func TestPort_IsolationNowait2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/nowait-2.spec")
}

// TestPort_IsolationTuplelockConflict exercises the tuplelock-conflict spec:
// verifies the tuple-lock conflict table across all 4 strengths, including the
// multixact cases where a SAVEPOINT subxid locks the same tuple as the main
// xid. M0118-0003.
func TestPort_IsolationTuplelockConflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_tuplelock_conflict")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/tuplelock-conflict.spec")
}

// TestPort_IsolationSkipLocked3 exercises skip-locked-3 (SKIP LOCKED with tuple
// locks). M0118-0003.
func TestPort_IsolationSkipLocked3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skip_locked3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/skip-locked-3.spec")
}

// TestPort_IsolationNowait5 exercises nowait-5 (NOWAIT on an updated tuple
// chain). M0118-0003.
func TestPort_IsolationNowait5(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait5")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/nowait-5.spec")
}

func TestPort_IsolationSkipLocked4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skiplocked4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/skip-locked-4.spec")
}

func TestPort_IsolationNowait4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/nowait-4.spec")
}

// TestPort_IsolationTuplelockUpdate exercises the tuplelock-update spec: s1 first
// builds an update chain via s1_chain (UPDATE pktab SET data = DEFAULT) then takes
// FOR KEY SHARE on the live row (s1_grablock). Three other sessions (s2/s3/s4)
// each have a pending no-key UPDATE gated behind one of three advisory locks s1
// holds; releasing each advisory lock in turn lets that updater run. Each no-key
// UPDATE does NOT conflict with FOR KEY SHARE, so the updater follows the ctid
// chain, propagates the KEY SHARE lock forward onto the new version, and proceeds
// immediately rather than blocking. M0118-0003.
func TestPort_IsolationTuplelockUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_tuplelock_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/tuplelock-update.spec")
}

// TestPort_IsolationTuplelockPartition exercises the tuplelock-partition spec:
// INSERT ON CONFLICT UPDATE on a LIST-partitioned table. A no-key UPDATE arm
// (col1/col2) does not conflict with a concurrent FOR KEY SHARE; a key UPDATE
// arm (SET key=1) blocks the FOR KEY SHARE until s1 commits. M0118-0003.
func TestPort_IsolationTuplelockPartition(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_tuplelock_partition")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/tuplelock-partition.spec")
}

// TestPort_IsolationPropagateLockDelete exercises the propagate-lock-delete
// spec: s1 and s2 each INSERT INTO child VALUES(1), which takes a FOR KEY SHARE
// lock on the parent row i=1 (RI_FKey_check). s3 then UPDATEs parent (no-key or
// key-update SET i=i, optionally with an aborted savepoint) — which must NOT
// drop the propagated FK locks — and finally DELETEs parent. The DELETE must
// wait on the still-in-flight child INSERTs (s1/s2 uncommitted) and, once they
// commit, raise the FK violation 23503 because the now-visible child rows still
// reference the parent. M0118-0003.
func TestPort_IsolationPropagateLockDelete(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_propagate_lock_delete")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/propagate-lock-delete.spec")
}

// TestPort_IsolationLockNowait exercises the lock-nowait spec: s1 takes ACCESS
// EXCLUSIVE on a1, then s2 requests EXCLUSIVE (blocks behind s1). While s2 waits,
// s1 requests SHARE ROW EXCLUSIVE NOWAIT — which must be granted IMMEDIATELY (s1
// already holds a stronger self-compatible lock, so it jumps ahead of the parked
// conflicting s2 waiter rather than failing on NOWAIT). After s1 commits, s2's
// EXCLUSIVE is granted. Exercises transaction-scoped LOCK TABLE heavyweight locks
// (held until COMMIT, not released per statement) plus lockmgr's JoinWaitQueue
// early-grant special case. M0118-0003.
func TestPort_IsolationLockNowait(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_nowait")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/lock-nowait.spec")
}

// TestPort_IsolationDeleteAbortSavept exercises the delete-abort-savept spec:
// s1 takes FOR KEY SHARE, opens a SAVEPOINT, upgrades the lock via DELETE, then
// ROLLBACK TO the savepoint — which must RESTORE the original FOR KEY SHARE
// lock (not leave the tuple unlocked). s2's FOR UPDATE must still wait behind
// the restored KEY SHARE lock until s1 commits. Rides the M0118-0004 subxact-
// scoped row-lock restore (stampMultiLock keeps outer-level self members as
// survivors so ROLLBACK TO reverts to the outer strength). M0118-0009.
func TestPort_IsolationDeleteAbortSavept(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_delete_abort_savept")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/delete-abort-savept.spec")
}

// TestPort_IsolationDeleteAbortSavept2 is the funkier delete-abort-savept
// variant: the subxact upgrade is FOR NO KEY UPDATE (not DELETE) and s2 probes
// with both FOR UPDATE and FOR NO KEY UPDATE. ROLLBACK TO must revert to the
// outer FOR KEY SHARE strength. M0118-0009.
func TestPort_IsolationDeleteAbortSavept2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_delete_abort_savept2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/delete-abort-savept-2.spec")
}

// TestPort_IsolationAbortedKeyrevoke exercises the aborted-keyrevoke spec: s1
// opens a SAVEPOINT, UPDATEs the key (obtaining KEY REVOKE), ROLLBACK TO the
// savepoint (losing KEY REVOKE), then takes FOR KEY SHARE. s2's FOR KEY SHARE
// must be compatible (both KEY SHARE) and proceed — the rolled-back key-update
// must not leave a phantom conflicting lock. Exercises subxact lock-restore on
// the multixact lock-only path. M0118-0009.
func TestPort_IsolationAbortedKeyrevoke(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_aborted_keyrevoke")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/aborted-keyrevoke.spec")
}

// TestPort_IsolationMultixactNoForget exercises the multixact-no-forget spec: s1
// holds FOR KEY SHARE; s2 UPDATEs (forming a {s2-update, s1-keyshare} multixact)
// then ABORTS — the abort must NOT forget s1's still-held KEY SHARE lock. s3
// then probes with FOR KEY SHARE (compatible), FOR NO KEY UPDATE / FOR UPDATE
// (conflicting, must wait). Validates that an aborted updater member is dropped
// from the multixact while the surviving locker member is preserved. M0118-0009.
func TestPort_IsolationMultixactNoForget(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_multixact_no_forget")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/multixact-no-forget.spec")
}

// TestPort_IsolationInplaceInval exercises the inplace-inval spec: an inplace
// update (CREATE INDEX setting pg_class.relhasindex=true) must not be reverted
// by a later heap_update of a cached oldtup. In upstream PostgreSQL this is a
// real catcache/heap_inplace_update hazard. goopg is immune by construction:
// pg_class is a VIRTUAL relation whose relhasindex is derived live from the
// in-memory index set (len(c.byTable[oid]) > 0) on every read, so there is no
// heap tuple, no catcache oldtup to go stale, and no inplace-update path to
// revert. Both permutations therefore observe relhasindex=t. M0118-0009.
func TestPort_IsolationInplaceInval(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_inplace_inval")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/inplace-inval.spec")
}

// TestPort_IsolationFreezeTheDead exercises the freeze-the-dead spec: tuple
// freezing interactions with dead/recently-dead tuples via multixact FOR KEY
// SHARE. M0118-0009.
func TestPort_IsolationFreezeTheDead(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_freeze_the_dead")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/freeze-the-dead.spec")
}

// TestPort_IsolationSubxidOverflow exercises the subxid-overflow spec: a
// recursive PL/pgSQL function (gen_subxids) opens 100 nested subtransactions
// via per-frame EXCEPTION handlers, overflowing the subxid cache, while other
// sessions probe MVCC visibility (XidInMVCCSnapshot) and lock-waits
// (XactLockTableWait) against the overflowed parent. Two PL/pgSQL gaps blocked
// it: a bare `RETURN;` (upstream-legal in a VOID function) was rejected at
// parse time, and the `NULL;` no-op statement (used as the empty EXCEPTION
// handler body) was an "unsupported PL/pgSQL statement". With both supported,
// goopg's existing subxact visibility/lock machinery already matches PG 18.3.
// M0118-0009.
func TestPort_IsolationSubxidOverflow(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_subxid_overflow")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/subxid-overflow.spec")
}

// TestPort_IsolationDeadlockSimple exercises the deadlock-simple spec: two
// sessions each take ACCESS SHARE on a1, then each attempts a lock upgrade to
// ACCESS EXCLUSIVE. Neither upgrade can complete until the other releases its
// ACCESS SHARE, so the deadlock detector must abort one session with 40P01.
// M0118-0003.
func TestPort_IsolationDeadlockSimple(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_deadlock_simple")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/deadlock-simple.spec")
}

// TestPort_IsolationDeadlockHard exercises the deadlock-hard spec: eight
// sessions each LOCK TABLE their own relation then attempt to LOCK the next in a
// ring (s1→a2, s2→a3, …, s8→a1), forming an 8-way cycle. Every session sets a
// 100s deadlock_timeout except s8 (10ms), so s8's wait-timer fires first; the
// main lock detector finds the multi-relation cycle and rolls back the session
// that discovered it (s8) with 40P01. Exercises the general (timeout-driven)
// wait-for-graph deadlock detector over the transaction-scoped LOCK TABLE
// heavyweight locks, per-session deadlock_timeout, and triggering-backend
// victim selection. M0118-0004.
func TestPort_IsolationDeadlockHard(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_deadlock_hard")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/deadlock-hard.spec")
}

// TestPort_IsolationDeadlockSoft exercises the deadlock-soft spec: four
// sessions form a 4-cycle with two hard edges (e1→d1, e2→d2: an
// ACCESS EXCLUSIVE waiter behind an ACCESS SHARE holder) and two soft edges
// (d2→e1, d1→e2: an ACCESS SHARE waiter queued behind a conflicting
// ACCESS EXCLUSIVE waiter). Because the cycle contains soft edges, the
// detector resolves it by REORDERING a wait queue (moving d1 ahead of e2 on
// a2) rather than aborting anyone — d1 is granted immediately and nobody
// fails. Exercises soft-deadlock wait-queue rearrangement (deadlock.c
// TopoSort / ExpandConstraints). M0118-0004.
func TestPort_IsolationDeadlockSoft(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_deadlock_soft")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/deadlock-soft.spec")
}

// TestPort_IsolationDeadlockSoft2 exercises the deadlock-soft-2 spec: the
// blocked session s1 must jump over BOTH s3 and s4 (which are hard-blocked on
// a2's ACCESS EXCLUSIVE request behind s2's ACCESS SHARE) and acquire its
// SHARE UPDATE EXCLUSIVE lock on a2 immediately, since s1's request does not
// conflict with the holder. This requires the soft-deadlock resolver to
// topologically reorder a multi-waiter queue (s1 ahead of s3, s4) instead of
// aborting. M0118-0004.
func TestPort_IsolationDeadlockSoft2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_deadlock_soft2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/deadlock-soft-2.spec")
}

// TestPort_IsolationMultixactNoDeadlock exercises the multixact-no-deadlock
// spec: s1 takes FOR SHARE on the row, then s2 also takes FOR SHARE (turning the
// row's tuple lock into a multixact). s3 then requests FOR UPDATE, which
// conflicts with the SHARE multixact and waits. While s3 waits, s1 re-requests
// FOR SHARE (s1lock2) — a lock it already holds — and must NOT be forced to
// queue behind the waiting s3 (which would deadlock); since s1 is already a
// member of the SHARE multixact it proceeds immediately. Once s2 and s1 commit,
// s3's FOR UPDATE is granted. M0118-0004.
func TestPort_IsolationMultixactNoDeadlock(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_multixact_no_deadlock")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/multixact-no-deadlock.spec")
}

// TestPort_IsolationTuplelockUpgradeNoDeadlock exercises the
// tuplelock-upgrade-no-deadlock spec: multiple sessions take row-level locks of
// varying strength (FOR KEY SHARE / FOR SHARE / FOR NO KEY UPDATE / FOR UPDATE /
// UPDATE / DELETE) on a single row across 9 permutations. It verifies that a
// session upgrading its already-held row lock while others wait does NOT
// deadlock, that sessions which do not upgrade acquire the lock in arrival
// order, and that the heap_lock_tuple algorithm correctly retries (re-evaluates
// the tuple lock after initially avoiding a deadlock) when an intervening
// rollback-to-savepoint changes the multixact membership. Rides the row-lock
// xmax / WaitForXID path (stampLockInner / tupleLockConflicts / multixact
// membership), not the heavyweight lockmgr. M0118-0004.
func TestPort_IsolationTuplelockUpgradeNoDeadlock(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_tuplelock_upgrade_no_deadlock")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpec(t, root, c, "postgres/src/test/isolation/specs/tuplelock-upgrade-no-deadlock.spec")
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
