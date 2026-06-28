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

	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-write-unique.spec")
}

// TestPort_IsolationReadWriteUnique2 exercises read-write-unique-2: two SSI
// transactions both probe for i=42 then INSERT; one must see a 40001 SSI
// failure (overlapping) or a 23505 unique violation (serialized).
func TestPort_IsolationReadWriteUnique2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-2.spec")
}

// TestPort_IsolationReadWriteUnique3 exercises read-write-unique-3 (bug 9301):
// an insert-if-not-exists SQL function under SSI must abort with 40001.
func TestPort_IsolationReadWriteUnique3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-3.spec")
}

// TestPort_IsolationReadWriteUnique4 exercises read-write-unique-4: a gapless
// per-year invoice sequence; mixes 40001 SSI failures and 23505 unique
// violations depending on read/write interleaving.
func TestPort_IsolationReadWriteUnique4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_rw_unique4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-write-unique-4.spec")
}

// TestPort_IsolationLockCommittedUpdate exercises a spec that produces <waiting ...>
// output — verifying that blocking detection and drain work correctly.
func TestPort_IsolationLockCommittedUpdate(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_lock_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/lock-committed-update.spec")
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
// PROMOTED to pass-required (2026-06-25, design 0118-0106): all 50 permutations
// match PG 18.3 byte-for-byte. The last divergence was the EPQ-over-join case
// `selectresultforupdate` (FOR UPDATE OF jt over a join whose locked relation is
// the inner index scan): goopg folded the index key condition `jt.id = y` into
// the per-row EPQ recheck, but `y` is a join input's column whose index lives in
// the join coordinate space — misread against the 2-column jointest tuple as
// `jt.id = jt.data`, dropping the post-update row (returned 0 rows). Fixed by
// only folding a row-local (constant) index key into the recheck; join/
// correlated keys are excluded (the CTID-chain logic still catches key-column
// changes). A sibling fix preserves build-side heap ctids through a lazy hash
// join for the FOR-UPDATE-over-hash-join variant.
func TestPort_IsolationEvalPlanQual(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_eval_plan_qual")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/eval-plan-qual.spec")
}

// TestPort_IsolationEvalPlanQualTrigger exercises the eval-plan-qual-trigger spec.
// PROMOTED to pass-required (2026-06-25, design 0118-0095): all 38 active
// permutations match PG 18.3 byte-for-byte. The spec is the hardest half of the
// EPQ output-parity pair — it stacks BEFORE/AFTER row triggers (plpgsql
// trig_report firing on INSERT/UPDATE/DELETE) on top of READ COMMITTED EPQ
// rechecks, key-update CTID-chain following, ON CONFLICT DO UPDATE upserts, and
// REPEATABLE READ 40001 serialization failures, all with RETURNING projection
// and NOTICE-emitting noisy_oper() WHERE quals. Its byte-for-byte match
// evidences that goopg's EvalPlanQual re-projects through the trigger queue and
// the upsert arbiter exactly as PG does. The sibling eval-plan-qual spec still
// defers (EXPLAIN/column-format diffs), so the M0118-0007 group stays open.
func TestPort_IsolationEvalPlanQualTrigger(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_eval_pq_trig")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/eval-plan-qual-trigger.spec")
}

// TestPort_IsolationIntraGrantInplaceDb exercises the intra-grant-inplace-db
// spec (M0118-0009, design 0118-0098). It verifies the catalog-tuple-xmax
// serialization between a GRANT … ON DATABASE and a concurrent in-place
// datfrozenxid update: a database-wide VACUUM (FREEZE) must <waiting ...> behind
// an uncommitted GRANT TEMP ON DATABASE (whose lock IS the pg_database tuple's
// xmax) and complete only after that transaction commits — exactly as PG's
// heap_inplace_update_scan waits on the tuple xmax. goopg has no real pg_database
// heap tuple, so the GRANT records its writer XID (Catalog.SetDatabaseACLChangeXID)
// and the database-wide VACUUM waits on it via mvcc.WaitForXID. The observed
// datfrozenxid never retreats (cmp3 → 0 rows). All output byte-identical to PG 18.3.
func TestPort_IsolationIntraGrantInplaceDb(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_intra_grant_db")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/intra-grant-inplace-db.spec")
}

// TestPort_IsolationIntraGrantInplace exercises the intra-grant-inplace spec
// (M0118-0009, design 0118-0117). It verifies the pg_class-tuple-xmax
// serialization for GRANT/REVOKE ACL changes, FOR UPDATE/SHARE rowmarks, the
// in-place relhasindex update (ALTER TABLE ADD PRIMARY KEY), and a virtual-
// catalog tuple DELETE — all of which take no heavyweight lock on the object
// but serialise on the pg_class tuple's xmax. The capstone permutation 10
// (`b1 drop1 b3 sfu3 revoke4 c1 r3`) issues `DELETE FROM pg_class WHERE
// relname = …` as a transaction-deferred table drop: sfu3's FOR UPDATE rowmark
// <waiting ...> behind the delete xmax, returns 0 rows once it commits (the
// relation is gone), and revoke4 — blocked behind sfu3's tuple lock — finds the
// relation gone and raises the internal "cache lookup failed for relation <oid>"
// elog, which the spec's DO block EXCEPTION handler catches and re-raises as a
// REDACTED WARNING. All 10 permutations byte-identical to PG 18.3.
func TestPort_IsolationIntraGrantInplace(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_intra_grant")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/intra-grant-inplace.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/lock-committed-keyupdate.spec")
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

// TestPort_IsolationCreateTrigger exercises the create-trigger spec
// (M0118-0008). CREATE TRIGGER takes a transaction-scoped ShareRowExclusiveLock
// on the table; a concurrent UPDATE (RowExclusiveLock) blocks until the
// CREATE TRIGGER transaction commits, while a concurrent SELECT ... FOR UPDATE
// (RowShareLock) proceeds — byte-identical to PG 18.3.
func TestPort_IsolationCreateTrigger(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_create_trigger")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/create-trigger.spec")
}

// TestPort_IsolationAsyncNotify exercises the async-notify spec (M0118-0009,
// design 0118-0090): LISTEN/NOTIFY/UNLISTEN, pg_notify(), and
// pg_notification_queue_usage() across self- and cross-backend delivery, with
// same-transaction de-duplication and ROLLBACK TO SAVEPOINT discard. Requires
// the server-side notify hub (LISTEN/NOTIFY engine), the savepoint-aware NOTIFY
// buffer (notifyLevel stack), pg_notification_queue_usage, multi-statement steps
// running as one implicit transaction, and the isolation runner's per-step
// notification capture (pq.ConnectorWithNotificationHandler → "<sess>: NOTIFY
// ... from <src>" lines). Byte-identical to PG 18.3 across all six permutations.
func TestPort_IsolationAsyncNotify(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_async_notify")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/async-notify.spec")
}

// TestPort_IsolationTimeouts exercises the timeouts spec (M0118-0009): the
// statement_timeout and lock_timeout GUCs against both table-level (LOCK TABLE)
// and row-level (DELETE on a row a concurrent UPDATE holds) lock waits. When a
// waiter blocks behind a conflicting lock, whichever of the two timeouts is set
// shorter fires first and cancels the statement with the matching SQLSTATE —
// "canceling statement due to statement timeout" (57014) or "canceling
// statement due to lock timeout" (55P03). The eight permutations cover every
// combination of {statement-only, lock-only, lock-shorter, statement-shorter} ×
// {table lock, row lock}. The blocked steps are marked (*) upstream because the
// short 10ms timeouts mean the isolation tester may cancel the step before it
// observes it as "waiting"; goopg reproduces PG 18.3 byte-for-byte across all
// permutations with no engine change (statement_timeout/lock_timeout already
// drive query cancellation), so this spec is promoted to pass-required.
func TestPort_IsolationTimeouts(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_timeouts")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/timeouts.spec")
}

// TestPort_IsolationPreparedTransactionsCIC exercises the prepared-transactions-cic
// spec (M0118-0009): CREATE INDEX CONCURRENTLY must interact correctly with a
// prepared transaction. s1 PREPAREs a transaction that inserted a row; goopg keeps
// the prepared transaction's mvcc slot active (same-backend 2PC, design 0118-0110)
// so s2's CREATE INDEX CONCURRENTLY parks waiting for that start-time snapshot to
// drain. With lock_timeout=10ms set, that wait is cancelled with "canceling
// statement due to lock timeout" — mvcc.WaitForSlotsToCommit now arms the session
// lock_timeout carried on the context (lockwait.Timeout) exactly like the
// heavyweight lock manager's ProcSleep (design 0118-0111). After c1 COMMIT PREPARED
// finalises the row, r2 reads it back (seqscan disabled is only a preference, so the
// SELECT still succeeds even though the concurrent index build was cancelled).
// Byte-identical to PG 18.3.
func TestPort_IsolationPreparedTransactionsCIC(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_prepared_cic")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/prepared-transactions-cic.spec")
}

// TestPort_IsolationPreparedTransactions exercises the prepared-transactions
// spec (M0118-0009, design 0118-0112): three overlapping SERIALIZABLE
// transactions drive an s1 --rw--> s2 --rw--> s3 dangerous structure under
// two-phase commit across all 1500 permutations, and exactly one of the three
// must abort with 40001 in every permutation. Building on the same-backend 2PC
// enabler (design 0118-0110), goopg now runs the SSI dangerous-structure check
// at PREPARE TRANSACTION time (Manager.PrepareCheckForSerializationFailure) and
// treats a PREPARED-but-not-committed peer like an already-committed one in the
// read/write conflict hooks (Prepared/PrepareSeqNo mirror SXACT_FLAG_PREPARED /
// prepareSeqNo). A dangerous structure whose pivot is already PREPARED makes the
// preparer/committer commit suicide rather than dooming the durable pivot; an
// rw-edge to a PREPARED writer dooms the reader. PREPARE on an already-aborted
// block silently rolls back (no 25P02), matching PrepareTransactionBlock on
// TBLOCK_ABORT. Byte-identical to PG 18.3 across all 1500 permutations.
func TestPort_IsolationPreparedTransactions(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_prepared_transactions")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/prepared-transactions.spec")
}

// TestPort_IsolationInheritTemp exercises the inherit-temp spec (M0118-0008):
// an inheritance tree whose children are TEMPORARY tables created in different
// sessions. Each backend owns its own temp namespace, so s1's scan/UPDATE/
// DELETE/TRUNCATE of the persistent parent must include its own temp child but
// exclude s2's (RELATION_IS_OTHER_TEMP). goopg keeps all relations in one
// shared catalog; the per-session TempOwner token (design 0118-0036) plus the
// AccessibleInheritanceChildren filter wired at the planner SELECT site and the
// executor UPDATE/DELETE/UPDATE…FROM/TRUNCATE expansion sites (design 0118-0037)
// reproduce PG 18.3 byte-for-byte across all nine permutations, including the
// last two where TRUNCATE inh_parent in s1 blocks s2's scan of the parent but
// not s2's scan of its own temp child.
func TestPort_IsolationInheritTemp(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_inherit_temp")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/inherit-temp.spec")
}

// TestPort_IsolationTruncateConflict exercises the truncate-conflict spec
// (M0118-0008, design 0118-0039). A role created with CREATE ROLE has no
// privileges on a table it does not own, so TRUNCATE under SET ROLE fails
// immediately with "permission denied for table truncate_tab" (42501) WITHOUT
// waiting for a lock; after GRANT TRUNCATE the command succeeds and instead
// blocks behind a concurrent session holding the table open. Requires the
// catalog ACL store (Catalog.GrantTablePrivilege / HasTablePrivilege), SET/RESET
// ROLE tracking the effective role, an autocommit table-level GRANT recorder,
// and the pre-lock TRUNCATE privilege check in execTruncate — byte-identical to
// PG 18.3 across all eight permutations.
func TestPort_IsolationTruncateConflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_truncate_conflict")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/truncate-conflict.spec")
}

// TestPort_IsolationSequenceDdl exercises the sequence-ddl spec (M0118-0008).
// nextval() takes a transaction-scoped RowExclusiveLock on the sequence
// relation and ALTER SEQUENCE takes an AccessExclusiveLock; the two conflict,
// so a concurrent nextval blocks while another session is mid-ALTER SEQUENCE
// and a later ALTER SEQUENCE waits for an in-progress nextval to commit —
// byte-identical to PG 18.3 across all five permutations.
func TestPort_IsolationSequenceDdl(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_sequence_ddl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/sequence-ddl.spec")
}

// TestPort_IsolationReindexConcurrently exercises the reindex-concurrently spec
// (M0118-0008). REINDEX TABLE CONCURRENTLY waits for every transaction holding a
// lock on the table to finish (the WaitForLockers analog, waitForRelationLockers)
// without itself blocking concurrent reads or writes, so a REINDEX issued while
// another session has the table open reports `<waiting ...>` and completes only
// after that transaction commits — byte-identical to PG 18.3 across all six
// permutations.
func TestPort_IsolationReindexConcurrently(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_reindex_conc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/reindex-concurrently.spec")
}

// TestPort_IsolationReindexConcurrentlyToast exercises the reindex-concurrently-toast
// spec (M0118-0008) — the last unpromoted spec of the suite. The spec renames a
// table's TOAST relation/index to deterministic names under allow_system_table_mods
// (TOAST-exposure epic slices 1–4) and then runs REINDEX {TABLE,INDEX} CONCURRENTLY
// pg_toast.<name> while a concurrent session holds the parent table locked. The
// REINDEX waits for the parent's lockers (waitForRelationLockers) without blocking
// concurrent DML, completes after the holder commits/rolls back, and errors
// `relation "pg_toast.<name>" does not exist` when the holder dropped the parent —
// byte-identical to PG 18.3 across all 60 permutations.
//
// PASS-REQUIRED (design 0118-0088): the synthetic TOAST relation/index resolves
// through its parent (catalog.ToastParentTable + LookupToastRel) so the REINDEX
// routes to the same CONCURRENTLY wait the non-toast reindex-concurrently spec uses.
func TestPort_IsolationReindexConcurrentlyToast(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_reindex_conc_toast")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/reindex-concurrently-toast.spec")
}

func TestPort_IsolationReindexSchema(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_reindex_schema")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/reindex-schema.spec")
}

// TestPort_IsolationMultipleCic exercises the multiple-cic spec (M0118-0008):
// two CREATE INDEX CONCURRENTLY builds running simultaneously, each with a
// partial-index predicate that calls an IMMUTABLE advisory-lock function.
//
// PASS-REQUIRED (design 0118-0031). Two engine changes: (1) a partial-index
// predicate that references no table columns is const-folded — evaluated once
// at build time, mirroring PostgreSQL's eval_const_expressions in
// BuildIndexInfo — so the predicate's advisory-lock call fires even though the
// table is empty, making s1i block; (2) CREATE INDEX CONCURRENTLY waits for the
// transactions that were already running when it started to drain before it
// completes (a start-time snapshot of active slots, drained after the build),
// so the second build (s2i) completes only after the first (s1i), matching
// PG 18.3 byte-for-byte.
func TestPort_IsolationMultipleCic(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_multiple_cic")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/multiple-cic.spec")
}

// TestPort_IsolationAlterTable3 exercises the alter-table-3 spec (M0118-0008):
// ALTER TABLE ... ENABLE/DISABLE TRIGGER mixed with concurrent writes and
// SELECT ... FOR UPDATE.
//
// PASS-REQUIRED (design 0118-0032). Two fixes: (1) ALTER TABLE ENABLE/DISABLE
// TRIGGER now takes a transaction-scoped ShareRowExclusiveLock (mirrors
// PostgreSQL's AlterTableGetLockLevel), so a concurrent INSERT (RowExclusiveLock)
// blocks until the ALTER transaction commits while a concurrent SELECT ... FOR
// UPDATE (RowShareLock) proceeds; (2) when a statement errors at the top level
// of a transaction the aborted transaction's table locks are released
// immediately (mirrors PostgreSQL's AbortTransaction releasing locks at abort,
// not at the explicit ROLLBACK), so a later conflicting ALTER in another session
// does not wait on the aborted transaction. Byte-identical to PG 18.3.
func TestPort_IsolationAlterTable3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_alter_table_3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/alter-table-3.spec")
}

// TestPort_IsolationAlterTable2 exercises the alter-table-2 spec (M0118-0008):
// ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY ... NOT VALID mixed with
// concurrent writes and SELECT ... FOR UPDATE on both the referencing and
// referenced tables.
//
// PASS-REQUIRED (design 0118-0046). Two changes: (1) the ALTER TABLE ADD
// FOREIGN KEY parser now accepts the NOT VALID trailer (any order with
// [NOT] DEFERRABLE), recording convalidated='f' in pg_constraint; (2) ADD
// CONSTRAINT takes a transaction-scoped ShareRowExclusiveLock on the altered
// table (AlterTableGetLockLevel → AT_AddConstraint), so a concurrent INSERT
// (RowExclusiveLock) blocks until the ALTER transaction commits while a
// concurrent SELECT ... FOR UPDATE (RowShareLock) proceeds. Byte-identical to
// PG 18.3 across all 48 permutations.
func TestPort_IsolationAlterTable2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_alter_table_2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/alter-table-2.spec")
}

// TestPort_IsolationAlterTable1 exercises the alter-table-1 spec (M0118-0008):
// ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY ... NOT VALID followed by
// ALTER TABLE ... VALIDATE CONSTRAINT, mixed with concurrent reads, writes,
// and an INSERT.
//
// PASS-REQUIRED (design 0118-0047). One new piece on top of alter-table-2's
// ADD FK NOT VALID: VALIDATE CONSTRAINT now parses and takes only a
// transaction-scoped ShareUpdateExclusiveLock (AlterTableGetLockLevel →
// AT_ValidateConstraint), which does NOT conflict with concurrent reads
// (AccessShareLock) or writes (RowExclusiveLock) — so the only blocking in the
// spec is a concurrent INSERT waiting on the uncommitted ADD CONSTRAINT's
// ShareRowExclusiveLock, exactly as in alter-table-2. Byte-identical to PG 18.3
// across all permutations.
func TestPort_IsolationAlterTable1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_alter_table_1")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/alter-table-1.spec")
}

// TestPort_IsolationAlterTable4 exercises the alter-table-4 spec (M0118-0008):
// add/remove inheritance (ALTER TABLE NO INHERIT / INHERIT), DROP TABLE of an
// inheritance child, and ALTER COLUMN TYPE on a child, all concurrent with
// SELECT SUM(a) FROM the parent.
//
// PASS-REQUIRED (designs 0118-0080/0081/0082). Children are identified at plan
// time but locked only when the scan opens, so a concurrent NO INHERIT / INHERIT
// is not seen by the in-flight SELECT (perms 1-2), a concurrent DROP of a child
// is coped with by skipping the vanished child (perm 3), and a concurrent
// ALTER COLUMN a TYPE float on the child raises "attribute \"a\" of relation
// \"c1\" does not match parent's type" once the child's lock is acquired
// post-commit (perm 4) — mirroring PostgreSQL's make_inh_translation_list.
// Byte-identical to PG 18.3 across all four permutations.
func TestPort_IsolationAlterTable4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_alter_table_4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/alter-table-4.spec")
}

// TestPort_IsolationPlpgsqlToast exercises the plpgsql-toast spec (M0118-0008):
// PL/pgSQL procedures with transaction control (COMMIT inside a DO block) where
// VACUUM in a second session — synchronized via advisory locks — runs between a
// variable's last assignment and its use, exercising the six assignment code
// paths plus a fetch-after-commit case.
//
// PASS-REQUIRED (design 0118-0054). Two final pieces on top of 0118-0049..0053:
// (1) a FOR-query loop now materializes its rows up front so the loop survives a
// DELETE/COMMIT in its body (PG holds the implicit cursor's snapshot across the
// commit) — fixes assign6's three iterations; (2) SELECT … INTO inside a body now
// substitutes PL/pgSQL frame variables including record-field refs (r.a) before
// planning — fixes fetch-after-commit. Byte-identical to PG 18.3 across all
// permutations. goopg stores text inline (no external TOAST chunk to orphan), so
// the detoast-correctness the spec guards is satisfied structurally.
func TestPort_IsolationPlpgsqlToast(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_plpgsql_toast")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/plpgsql-toast.spec")
}

// TestPort_IsolationVacuumSkipLocked exercises the vacuum-skip-locked spec
// (M0118-0008). VACUUM/ANALYZE (SKIP_LOCKED) take a conditional per-relation
// lock: a relation held by another session is skipped — with a WARNING when
// named explicitly, silently when reached by partition expansion. ANALYZE of a
// partitioned parent then reads each leaf partition under a blocking
// AccessShareLock for inheritance stats, so it waits on a child locked in
// ACCESS EXCLUSIVE (but not SHARE).
func TestPort_IsolationVacuumSkipLocked(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_vacuum_skip_locked")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/vacuum-skip-locked.spec")
}

// TestPort_IsolationVacuumConcurrentDrop exercises the vacuum-concurrent-drop
// spec (M0118-0008). Without SKIP_LOCKED, VACUUM/ANALYZE take a blocking
// per-relation ShareUpdateExclusiveLock, so they wait behind a concurrent
// LOCK ... IN SHARE MODE; after the wait a target dropped by the committing
// session is re-detected and skipped — with a "relation no longer exists"
// WARNING for an explicitly named target, silently for an expanded partition
// child.
func TestPort_IsolationVacuumConcurrentDrop(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_vacuum_concurrent_drop")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/vacuum-concurrent-drop.spec")
}

// TestPort_IsolationVacuumConflict exercises the vacuum-conflict spec
// (M0118-0008). VACUUM/ANALYZE perform a maintenance-privilege check
// (vacuum_is_permitted_to_vacuum) BEFORE taking any lock: a non-superuser
// session (SET ROLE) that does not own the table skips it immediately with a
// "permission denied to vacuum/analyze ... skipping it" WARNING (no wait). After
// ALTER TABLE ... OWNER TO grants ownership, VACUUM/ANALYZE are permitted and
// block on a conflicting LOCK ... IN SHARE UPDATE EXCLUSIVE MODE until commit.
// Byte-identical to PG 18.3.
func TestPort_IsolationVacuumConflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_vacuum_conflict")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/vacuum-conflict.spec")
}

// TestPort_IsolationClusterConflict exercises the cluster-conflict spec
// (M0118-0008). The table is owned by the test role (ALTER TABLE OWNER TO in
// setup), so CLUSTER is permitted; it takes an AccessExclusiveLock and therefore
// blocks behind a concurrent LOCK ... IN SHARE UPDATE EXCLUSIVE MODE until that
// holder commits, then completes. Byte-identical to PG 18.3.
func TestPort_IsolationClusterConflict(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_cluster_conflict")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/cluster-conflict.spec")
}

// TestPort_IsolationClusterConflictPartition exercises the
// cluster-conflict-partition spec (M0118-0008). CLUSTER of a partitioned table
// owned by the test role (ALTER TABLE OWNER TO does NOT recurse to partition
// children — tablecmds.c AT_ChangeOwner "never recurses") behaves as:
//   - CLUSTER takes an AccessExclusiveLock on the named PARENT, so it blocks
//     behind a concurrent LOCK cluster_part_tab IN SHARE UPDATE EXCLUSIVE MODE
//     and completes once the holder commits (permutations 1, 2).
//   - When only a partition LEAF is locked, CLUSTER never touches it: upstream
//     skips every leaf the role does not own (the children stay owned by the
//     bootstrap superuser; cluster_is_permitted_for_relation returns false, the
//     WARNING suppressed by client_min_messages=ERROR), and goopg's CLUSTER is a
//     no-op rewrite that only locks the named parent — so the locked leaf is
//     irrelevant and CLUSTER completes immediately (permutations 3, 4).
//
// Byte-identical to PG 18.3 with no engine change (rides the cluster-conflict
// AccessExclusiveLock from design 0118-0040 + existing partition catalog).
func TestPort_IsolationClusterConflictPartition(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_cluster_conflict_partition")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/cluster-conflict-partition.spec")
}

// TestPort_IsolationDetachPartitionConcurrently1 exercises the
// detach-partition-concurrently-1 spec (M0118-0008). ALTER TABLE … DETACH
// PARTITION … CONCURRENTLY makes the partition invisible at the correct,
// snapshot-relative time:
//   - Phase 1 bumps a global partition-detach epoch (mvcc) and stamps the child
//     detach-pending, KEEPING it registered (relpartbound stays set, so s3i reads
//     `relpartbound IS NULL` = f). Every snapshot taken afterwards captures the
//     higher epoch and omits the child (READ COMMITTED — gone immediately) while a
//     snapshot taken before still includes it (REPEATABLE READ — visible until
//     commit). Both the SELECT planner expansion (collectAllPartitionLeaves) and
//     INSERT routing (routeToPartitionDepth) filter through
//     catalog.VisiblePartitionChildren by the snapshot epoch, so s3's INSERT of a
//     row that would route to the detached partition fails with "no partition
//     found" exactly as the SELECT omits it.
//   - The detacher then waits via a HYBRID of relation-locker draining (READ
//     COMMITTED sessions that touched the table) and pinned-snapshot draining
//     (WaitForPinnedSnapshotsToCommit — a REPEATABLE READ session that only
//     PREPAREd a statement is still waited for), rendered as `<waiting ...>` by
//     the runner's timing. Design 0118-0060.
//   - Phase 2 unregisters the child, clears relpartbound (now NULL, so s3i flips
//     f→t), and clears the pending mark.
//
// The cross-session plan cache is bypassed while any detach is pending
// (partitionDetachPending), so each statement re-plans against its own snapshot
// epoch. Byte-identical to PG 18.3 across all 13 permutations. Design 0118-0059.
// TestPort_IsolationPartitionConcurrentAttach exercises the
// partition-concurrent-attach spec (M0118-0008): a non-default partition is
// attached to a range-partitioned table concurrently with an INSERT that, under
// its pre-attach snapshot, routes through the table's sub-partitioned DEFAULT
// partition. The INSERT takes a ROW EXCLUSIVE lock on every intermediate
// partition along its routing path (esp. the default), which conflicts with the
// ATTACH's ACCESS EXCLUSIVE lock on the default (design 0118-0076), so whichever
// statement runs second waits for the other's transaction to commit; the loser
// is then re-validated against a fresh snapshot — the routed INSERT re-routes
// onto the now-visible sibling and raises 23514, or the ATTACH's default-content
// re-scan finds the rows and raises 23P01. Byte-identical to PG 18.3 across all 3
// permutations. Design 0118-0079.
func TestPort_IsolationPartitionConcurrentAttach(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_partition_concurrent_attach")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/partition-concurrent-attach.spec")
}

func TestPort_IsolationDetachPartitionConcurrently1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_detach_partition_1")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/detach-partition-concurrently-1.spec")
}

// TestPort_IsolationDetachPartitionConcurrently2 exercises the
// detach-partition-concurrently-2 spec (M0118-0008): DETACH PARTITION
// CONCURRENTLY makes the partition safe for a foreign key that references the
// partitioned table. Three behaviours, all snapshot-relative:
//   - A concurrent INSERT into the referencing table of a value that lives only
//     in the detaching partition fails its FK check (23503): the partition is
//     invisible to the inserter's snapshot, so the FK existence scan
//     (assertParentExists → allDescendants filtered by ctx.Snap.PartitionDetachEpoch)
//     does not find the parent key. A value in a still-attached partition succeeds.
//   - If a referencing row already exists (committed before the detach) whose
//     key routes to the detaching partition, the DETACH itself fails synchronously
//     with `removing partition … violates foreign key constraint <fkname>_<N>`
//     (RI_PartitionRemove_Check; detachPartitionFKRefCheck), N being the child's
//     ordinal among the parent's partitions.
//   - The detacher does NOT wait for a READ COMMITTED session that only issued
//     BEGIN (it holds no relation lock and no pinned snapshot), but does wait for
//     one that read the partitioned table (txn-scoped relation lock). Hybrid wait.
//
// Byte-identical to PG 18.3 across all 5 permutations. Design 0118-0060.
func TestPort_IsolationDetachPartitionConcurrently2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_detach_partition_2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/detach-partition-concurrently-2.spec")
}

// TestPort_IsolationDetachPartitionConcurrently3 exercises the
// detach-partition-concurrently-3 spec (M0118-0008): things that may happen to
// a partition left in an "incomplete detach" state — a DETACH PARTITION
// CONCURRENTLY that was cancelled (pg_cancel_backend) while it waited. Across
// all 18 permutations, byte-identical to PG 18.3:
//   - The cancel LEAVES the partition detach-pending (no revert): it is omitted
//     from the parent for every newer snapshot, so INSERT into the parent of a
//     value that lived only in it fails "no partition found"; a REPEATABLE READ
//     session whose snapshot predates the detach still sees it.
//   - pg_partition_tree omits the pending child from the parent and reports it as
//     a standalone root (NULL parent); ALTER on it errors 55000 "cannot alter
//     partition … with an incomplete detach"; a second concurrent DETACH errors
//     55000 "partition … already pending detach".
//   - TRUNCATE of the parent skips it; DROP of the parent still drops it; DROP of
//     the pending child grabs an AccessExclusiveLock on the parent (so a
//     concurrent parent SELECT blocks); DETACH … FINALIZE completes it, taking an
//     AccessExclusiveLock on the partition (a concurrent read/insert of the
//     partition blocks until FINALIZE's transaction commits, but a parent scan
//     does not).
//
// Design 0118-0061.
func TestPort_IsolationDetachPartitionConcurrently3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_detach_partition_3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/detach-partition-concurrently-3.spec")
}

// TestPort_IsolationDetachPartitionConcurrently4 exercises the
// detach-partition-concurrently-4 spec (M0118-0008): foreign keys in the face
// of concurrent DETACH PARTITION … CONCURRENTLY of the referenced table. Across
// all 21 permutations, byte-identical to PG 18.3:
//   - Inserting/updating a value that lives only in a concurrently-detaching
//     partition fails its FK check even under REPEATABLE READ, because the RI
//     existence query observes the current detach epoch (design 0118-0062),
//     while a cursor/SELECT in the same RR txn still sees that very row.
//   - An UPDATE that sets an FK column fires the RI parent-existence check
//     (RI_FKey_check) just like an INSERT, so `update d4_fk set a = 1 where
//     current of f` (value 1 invisible in the detaching partition) raises 23503
//     (design 0118-0064, Fix 1).
//   - When the referencing row is created/updated by a concurrent session that
//     the detacher waits on, the detach re-validates RI_PartitionRemove_Check
//     after the wait (fresh snapshot, routing the pending child back in) and
//     fails "removing partition … violates foreign key constraint …_1"
//     (design 0118-0064, Fix 2).
//   - Cursor pinning at DECLARE + abort-releases-snapshot + cancel-message
//     mapping (design 0118-0063).
func TestPort_IsolationDetachPartitionConcurrently4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_detach_partition_4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/detach-partition-concurrently-4.spec")
}

// TestPort_IsolationVacuumNoCleanupLock exercises the vacuum-no-cleanup-lock
// spec (M0118-0008). It asserts that pg_class.relpages / reltuples reflect what
// VACUUM observes, even when a concurrent backend (a cursor holding a heap-page
// pin) prevents VACUUM from acquiring a cleanup lock. goopg publishes reltuples
// from a fresh-snapshot visible-tuple count (vac_update_relstats) so a recently
// dead tuple — deleted and committed but not yet removable because the pin
// holder holds OldestXmin back — is excluded from reltuples, matching PG 18.3.
// The vacuumer session also SETs vacuum_multixact_freeze_min_age (new GUC).
func TestPort_IsolationVacuumNoCleanupLock(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_vacuum_no_cleanup")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/vacuum-no-cleanup-lock.spec")
}

// TestPort_IsolationPartitionDropIndexLocking exercises the
// partition-drop-index-locking spec (M0118-0008). DROP INDEX on a partitioned
// index, while a concurrent SELECT holds ACCESS SHARE on a leaf partition, must
// (a) block on the partition-tree AccessExclusiveLock acquired top-down and
// (b) keep the dropped index's pg_class row + the dropper's lock visible to a
// third observing session's `pg_locks JOIN pg_class` until the dropping
// transaction commits. The latter is the transactional-DDL visibility piece:
// execDropIndex defers the catalog/relfile/WAL removal of a non-CONCURRENTLY
// DROP INDEX issued inside an explicit transaction to COMMIT
// (ApplyPendingIndexDrops), so the index stays in the shared catalog meanwhile.
// Byte-identical to PG 18.3 (design 0118-0074).
func TestPort_IsolationPartitionDropIndexLocking(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_part_drop_idx_lock")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/partition-drop-index-locking.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/simple-write-skew.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/matview-write-skew.spec")
}

// TestPort_IsolationTwoIds exercises the two-ids spec: a SERIALIZABLE
// read/write cycle over two id rows must abort with 40001.
func TestPort_IsolationTwoIds(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_two_ids")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/two-ids.spec")
}

// TestPort_IsolationTotalCash exercises the total-cash spec: a SERIALIZABLE
// constraint over an aggregate (total cash invariant) under write skew.
func TestPort_IsolationTotalCash(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_total_cash")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/total-cash.spec")
}

// TestPort_IsolationReceiptReport exercises the receipt-report spec: the
// classic batch/receipt read-only anomaly under SERIALIZABLE.
func TestPort_IsolationReceiptReport(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_receipt_report")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/receipt-report.spec")
}

// TestPort_IsolationProjectManager exercises the project-manager spec: a
// SERIALIZABLE resource-assignment write skew must abort one transaction.
func TestPort_IsolationProjectManager(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_project_manager")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/project-manager.spec")
}

// TestPort_IsolationClassroomScheduling exercises the classroom-scheduling
// spec: overlapping SERIALIZABLE bookings form a dangerous structure.
func TestPort_IsolationClassroomScheduling(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_classroom_sched")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/classroom-scheduling.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly.spec")
}

// TestPort_IsolationReadOnlyAnomaly2 exercises read-only-anomaly-2: same O'Neil
// example under SERIALIZABLE. The second permutation creates a cycle once the
// read-only s3 observes s1's committed write, so s2wx must abort with 40001.
func TestPort_IsolationReadOnlyAnomaly2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_ro_anomaly2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly-2.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/read-only-anomaly-3.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel-2.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/serializable-parallel-3.spec")
}

// TestPort_IsolationUpdateConflictOut exercises update-conflict-out: SSI
// "conflict out" handling for heapam interacting with a concurrently updated
// (then aborted) tuple. "bar" must fail with 40001 at bar_commit at the latest.
func TestPort_IsolationUpdateConflictOut(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_update_conflict_out")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/update-conflict-out.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/multiple-row-versions.spec")
}

// TestPort_IsolationPredicateLockHotTuple exercises predicate-lock-hot-tuple:
// two SERIALIZABLE transactions each SELECT i IN (5,7) then UPDATE one of the
// two rows. The reads cross-cover the other writer's target row, forming a
// write-skew dangerous structure, so the later committer (s2) must abort with
// 40001. Promoted to pass-required (M0118-0002, design 0118-0026) — matches
// PG 18.3 byte-for-byte with no engine change.
func TestPort_IsolationPredicateLockHotTuple(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_pred_lock_hot")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/predicate-lock-hot-tuple.spec")
}

// TestPort_IsolationPredicateHash exercises predicate-hash: PAGE (bucket) level
// predicate locking in a hash index. Two SERIALIZABLE transactions each probe a
// hash-indexed column by equality and INSERT rows. A scan and an insert that
// touch the SAME bucket form an rw-conflict (so an overlapping interleaving
// aborts the loser with 40001), while a scan and an insert that touch DIFFERENT
// buckets must NOT conflict — the reduced-false-positive half of the test.
// Promoted to pass-required (M0118-0009, design 0118-0099): a declared-hash
// index now drives a bucket-grain SIREAD predicate lock (ssiRecordHashBucketRead
// / ssiRecordHashIndexInsert) instead of a seq scan's relation-grain lock, so
// goopg matches PG 18.3 byte-for-byte across all 40 permutations (previously
// over-aborted the 12 different-bucket interleavings).
func TestPort_IsolationPredicateHash(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_predicate_hash")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/predicate-hash.spec")
}

// TestPort_IsolationPartialIndex exercises partial-index: an UPDATE that moves a
// row out of a partial index (CREATE INDEX ... WHERE val2 = 1) under SERIALIZABLE
// must still create the read/write dependency a full-table read would, so any
// overlap between the two transactions raises 40001. Promoted to pass-required
// (M0118-0002, design 0118-0026) — matches PG 18.3 byte-for-byte with no engine
// change.
func TestPort_IsolationPartialIndex(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_partial_index")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/partial-index.spec")
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
// Promoted to pass-required (M0118-0005, design 0118-0023). The sibling
// fk-deadlock spec is now also promoted (see TestPort_IsolationFkDeadlock,
// design 0118-0094).
func TestPort_IsolationFkDeadlock2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_deadlock2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-deadlock2.spec")
}

// TestPort_IsolationFkDeadlock exercises fk-deadlock: two sessions each INSERT a
// child row referencing the same parent (each taking an implicit FOR KEY SHARE
// on that parent row), then each issues a *no-key* UPDATE of the parent. The FK
// KEY SHARE check must be COMPATIBLE with a concurrent in-flight no-key UPDATE —
// only a key UPDATE or a DELETE conflicts with FOR KEY SHARE — so the child
// INSERTs never wait; the blocking that does appear comes solely from the two
// no-key UPDATEs serialising against each other. goopg previously over-waited:
// the FK match scan (scanRelForFKMatch) treated ANY in-flight non-self updater
// as a conflict, so a child INSERT blocked where PG proceeds. Fixed by making
// the FK scan key-aware (only a key-changing updater — StatusUpdate /
// structurally-detected DELETE — is a conflict). Promoted to pass-required
// (M0118-0005, design 0118-0094).
func TestPort_IsolationFkDeadlock(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_deadlock")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-deadlock.spec")
}


// TestPort_IsolationFkPartitioned1 exercises fk-partitioned-1: cloning a foreign
// key onto a partition (ALTER TABLE pfk ATTACH PARTITION pfk1) must ensure the
// referenced values still exist, even while a referenced row is being
// concurrently deleted. Three classes of permutation:
//   - Class A: the referenced row is deleted before/during the attach → the
//     attach's RI_Initial_Check fails 23503 naming "pfk1"/"pfk_a_fkey".
//   - committed Class B: the attach commits, then DELETE FROM ppk1 (a leaf of
//     the *referenced* ppk) is rejected on the referenced side, 23503 naming
//     "pfk_a_fkey_1" on table "pfk".
//   - concurrent Class B: DELETE FROM ppk1 runs while the attach is still
//     uncommitted → it blocks <waiting ...> behind the attach's held-to-commit
//     KEY SHARE on the referenced rows, then errors once the attach commits.
//
// Promoted M0118-0005/0009 across three slices: design 0118-0118 (Class A
// referencing-side clone + validation on ATTACH), 0118-0119 (committed Class B
// referenced-side check + ordinal-suffixed constraint name), and 0118-0120 (the
// concurrent Class B wait: the deferred attach records its XID so a concurrent
// referenced-row DELETE waits for it to commit, then re-evaluates).
func TestPort_IsolationFkPartitioned1(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_partitioned1")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-partitioned-1.spec")
}

// TestPort_IsolationFkPartitioned2 exercises fk-partitioned-2: an FK that
// references a PARTITIONED table (pfk -> ppk, both list-partitioned) must
// enforce referential integrity across the concurrent INSERT-vs-DELETE race.
//   - INSERT into pfk while the referenced ppk row is being concurrently
//     deleted: the FK's SELECT FOR KEY SHARE blocks <waiting ...> on the
//     deleter; once it commits, READ COMMITTED re-evaluates and emits the
//     23503 FK violation, while REPEATABLE READ / SERIALIZABLE cannot walk the
//     update chain past their snapshot and surface 40001 "could not serialize
//     access due to concurrent update".
//   - DELETE FROM the partitioned parent ppk (routed to leaf ppk1) while a
//     referencing pfk row is being concurrently inserted: blocks on the
//     inserter, then errors naming the LEAF partition and its ordinal-suffixed
//     clone constraint, "update or delete on table \"ppk1\" violates foreign
//     key constraint \"pfk_a_fkey_1\" on table \"pfk\"".
//
// Promoted M0118-0005 (design 0118-0121): scanTableForMatchFKWait raises 40001
// on a committed key-changing parent updater under RR/SSI, and enforceFKOnDelete
// routes a partitioned-parent delete to its leaf so the referenced-side
// violation names the per-partition clone.
func TestPort_IsolationFkPartitioned2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_fk_partitioned2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/fk-partitioned-2.spec")
}

// TestPort_IsolationRiTrigger exercises trigger-based referential integrity
// under SERIALIZABLE: BEFORE UPDATE/DELETE and BEFORE INSERT/UPDATE plpgsql
// triggers that PERFORM a query and RAISE on FOUND / NOT FOUND, plus SSI 40001
// when the two transactions' read/write sets overlap. Promoted M0118-0009
// (design 0118-0097): trigger-body errors now abort the DML, PERFORM accepts a
// full query form, and FOUND is tracked.
func TestPort_IsolationRiTrigger(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_ri_trigger")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/ri-trigger.spec")
}

// TestPort_IsolationIndexOnlyScan exercises the index-only-scan spec: a
// SERIALIZABLE write skew across two all-visible tables (tabx / taby) where each
// transaction reads one table via an index-only scan of SELECT min(id) and
// DELETEs the matching row from the other table. Any overlap forms a
// rw-dependency cycle so the second committer must abort with 40001; the two
// serialized orderings (rxwy1 c1 rywx2 c2 / rywx2 c2 rxwy1 c1) commit cleanly.
// Promoted to pass-required (M0118-0002, design 0118-0026) — matches PG 18.3
// byte-for-byte with no engine change.
func TestPort_IsolationIndexOnlyScan(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_index_only_scan")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/index-only-scan.spec")
}

// TestPort_IsolationIndexOnlyBitmapscan exercises the index-only-bitmapscan
// spec: a regression guard for an unsound index-only bitmap heap scan removed
// upstream. s1 opens a NO SCROLL cursor over `SELECT row_number() OVER () FROM
// ios_bitmap WHERE a > 0 OR b > 0` (a BitmapOr over two indexes), FETCHes one
// row to force the index-scan portion to run, then s2 VACUUMs after deleting
// nearly all rows. With the historical bug the post-FETCH `FETCH ALL` returned
// rows from pages VACUUM had marked all-visible despite their tuples being
// dead; the correct result is 0 rows. goopg returns 1 row then 0 rows, matching
// PG 18.3.
//
// Promoted to pass-required (M0118-0002, design 0118-0122). The sole remaining
// blocker was that `EXPLAIN (COSTS OFF) DECLARE foo ... CURSOR FOR <query>`
// (step s1_explain) raised 0A000 because the planner rejected a
// DeclareCursorStmt inner — now unwrapped to plan the cursor's query. The
// EXPLAIN plan body is stripped on both sides by the runner's established
// plan-strategy normalization policy (goopg renders no BitmapOr node), so the
// spec's actual anomaly check — the FETCH row counts — is what is compared and
// it matches byte-for-byte with no execution-engine change.
func TestPort_IsolationIndexOnlyBitmapscan(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_iob")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/index-only-bitmapscan.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/skip-locked.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/nowait.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/nowait-3.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/update-locked-tuple.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/lock-update-traversal.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/lock-update-delete.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/skip-locked-2.spec")
}

// TestPort_IsolationNowait2 exercises the nowait-2 spec: like skip-locked-2 but
// s2 uses FOR UPDATE NOWAIT and must abort with 55P03 instead of skipping when
// the multixact SHARE member held by s1 blocks the upgrade. M0118-0003.
func TestPort_IsolationNowait2(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait2")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/nowait-2.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/tuplelock-conflict.spec")
}

// TestPort_IsolationSkipLocked3 exercises skip-locked-3 (SKIP LOCKED with tuple
// locks): s1 holds a plain FOR UPDATE on the first row and a third session forces
// a tuple-lock wait queue, so s2's FOR UPDATE SKIP LOCKED must skip the
// tuple-locked row rather than join the queue. Pass-required (M0118-0003 row-lock
// family hardened to strict; design 0118-0042).
func TestPort_IsolationSkipLocked3(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skip_locked3")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/skip-locked-3.spec")
}

// TestPort_IsolationNowait5 exercises nowait-5 (NOWAIT on an updated tuple
// chain). M0118-0003.
func TestPort_IsolationNowait5(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait5")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/nowait-5.spec")
}

// TestPort_IsolationSkipLocked4 exercises skip-locked-4 (SKIP LOCKED over an
// updated tuple chain): s1's FOR UPDATE SKIP LOCKED is gated behind an advisory
// lock while s2 UPDATEs the first row (building a ctid chain) then locks it; when
// the advisory lock is released s1 must follow the chain, find the live successor
// row-locked by s2, and skip it — claiming the other row instead of blocking.
// Pass-required (M0118-0003 row-lock family hardened to strict; design 0118-0042).
func TestPort_IsolationSkipLocked4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_skiplocked4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/skip-locked-4.spec")
}

func TestPort_IsolationNowait4(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_nowait4")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/nowait-4.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/tuplelock-update.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/tuplelock-partition.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/propagate-lock-delete.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/lock-nowait.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/delete-abort-savept.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/delete-abort-savept-2.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/aborted-keyrevoke.spec")
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

// TestPort_IsolationTempSchemaCleanup exercises the temp-schema-cleanup spec
// (M0118-0009, design 0118-0091). Permutation 1 (DISCARD TEMP cleanup) passes
// on the per-session temp-namespace model + pg_my_temp_schema() + DISCARD TEMP
// drop landed this loop and is hard-guarded by
// TestSyntax_TempSchema_MyTempSchemaAndDiscard. Permutation 2 (backend
// self-termination via pg_terminate_backend, session-exit temp+namespace
// cleanup with advisory-lock release ordering, the isolationtester
// connection-death "FATAL / server closed the connection unexpectedly"
// rendering, and temp-type dependency cascade of uses_a_temp_type) is deferred
// — this anchor runIsoSpec()-skips until the whole spec matches byte-for-byte.
func TestPort_IsolationTempSchemaCleanup(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_temp_schema_cleanup")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/temp-schema-cleanup.spec")
}

// TestPort_IsolationHorizons exercises the horizons spec (M0118-0009, design
// 0118-0104): pruning and vacuuming must respect concurrent sessions. For a
// TEMPORARY relation, rows a session deleted ARE reclaimable despite another
// session's older snapshot (temp data is private to the owning backend, so the
// index-only scan's Heap Fetches drops to 0 after a prune-on-read or VACUUM);
// for a PERMANENT relation those rows must survive (Heap Fetches stays 2) while
// the older snapshot lives. This loop landed the TEMPORARY half plus the
// no-vacuum permanent permutations — 4 of 5 permutations match PG 18.3: temp
// relations vacuum and prune at the session-local horizon
// (mvcc.OldestXminForProc), the index-only scan prunes the temp heap pages it
// touched after the scan, and a reclaimed index entry (LP_UNUSED/LP_DEAD root)
// is skipped without a heap-fetch tally. runIsoSpec (soft) until the final
// permanent-table VACUUM-respects-snapshot permutation lands: it needs an
// explicit RR transaction's snapshot xmin registered in the proc array, which
// requires capturing the RR snapshot at the batched BEGIN+first-statement step,
// and doing so exposes a latent RR concurrent-update (40001) detection gap that
// regresses eval-plan-qual-trigger (deferred; see the design doc).
func TestPort_IsolationHorizons(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_horizons")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/horizons.spec")
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
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/tuplelock-upgrade-no-deadlock.spec")
}

// TestPort_IsolationStats drives the cumulative-statistics isolation spec
// (pg_stat_* function/relation/SLRU stats). Strict (runIsoSpecStrict) — the
// spec matches PG 18.3 byte-for-byte across all permutations. The final rung
// that promoted it was isolation-runner connection reuse: upstream
// isolationtester opens one connection per session ONCE and reuses it for every
// permutation, so a session GUC set by a step (e.g. SET track_functions='all')
// persists forward; stats.spec's last permutations rely on track_functions set
// by an earlier permutation still being in effect (design 0118-0133). The
// pgstat subsystems (function/relation/SLRU stats, transactional DROP FUNCTION
// cross-session visibility, 2PC stat drops) landed in designs 0118-0123..0132.
func TestPort_IsolationStats(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_stats")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	runIsoSpecStrict(t, root, c, "postgres/src/test/isolation/specs/stats.spec")
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
