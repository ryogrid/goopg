# M-NIGHTLY-tuplelock-upgrade-reopen — the isolation runner asks whether a step waits on a lock

Status: landed 2026-10-10 (2544b2c57).

## Problem

`TestPort_IsolationTuplelockUpgradeNoDeadlock` failed in the 2026-10-03
nightly.

- **The symptom.** The schedule's tail was shifted by two lines (255 vs
  253), and `<... completed>` lines landed one step late. The run took
  15.55 s, against about 12.5 s normally.
- **It did not recur.** It passed in the seven nightlies since and 4/4
  fresh runs at HEAD.
- **The same signature elsewhere.** `TestPort_IsolationPreparedTransactions`
  had been demoted to non-strict for this cause. A slow 2PC step (WAL
  segment zero-fill, state-file write) was labelled `<waiting ...>`.

## Cause: the runner decided blocking by time

The runner in `internal/testport/framework/isolation_runner.go` used two
timing rules.

- **Launch.** A step still running after `blockDetectWait` (300 ms) was
  printed as `<waiting ...>`.
- **Drain.** After each step, every previously-waiting step got a fixed
  `postStepDrainWait` (200 ms) to complete before the next step ran.

On a loaded host, a step that is merely slow trips the first rule, and a
waiter that a later step released, but which needs more than 200 ms to
finish, trips the second. Both shift the output.

## PG behaviour

isolationtester decides neither by elapsed time (isolationtester.c
`try_complete_step`).

- **While a step runs,** it polls the socket and asks
  `pg_isolation_test_session_is_blocked(pid, interesting_pids)`
  (lockfuncs.c). Only a lock wait makes a step `<waiting ...>`.
- **After each step,** it re-checks every waiting step with
  `STEP_NONBLOCK | STEP_RETRY`. A step that is no longer blocked is waited
  for until it completes or blocks again.

## Change

### The probe

`sessionConns.blockedProbe` runs
`SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1` against the
session's backend.

- **Why this works.** goopg reports `Lock` waits for transactionid
  (`WaitForXID`), relation and advisory locks.
- **Why not the PG function.** `pg_isolation_test_session_is_blocked` is
  registered (OID 3378) but not implemented.
- **Simplification.** Unlike upstream, the probe does not restrict blockers
  to the spec's sessions; the test cluster is private to the spec.

### The wait rule

`awaitStepOrBlock(outCh, firstWait, probe)`:

1. waits `firstWait` for the step to complete;
2. then probes:
   - `Lock` means blocked;
   - otherwise it waits on, re-probing every `blockProbePoll` (50 ms);
3. treats the step as blocked when the probe cannot run, or once
   `blockProbeCap` (1.5 s) passes without a lock wait.

The fallback keeps a goopg wait path that reports no wait event working:
the step is still reported waiting, just later.

### Where it applies

- **Launch:** with `firstWait = blockDetectWait` (300 ms).
- **Post-step drain** (`drainWithTimeout`): with
  `firstWait = postStepDrainWait` (200 ms).

## Verification

- **Unit tests.**
  - `TestAwaitStepOrBlockAsksTheProbe` covers five cases: slow but not
    blocked, lock-blocked, no probe, hung past the cap, and fast.
  - `TestDrainWithTimeout_EmitsPendingStepNotices` now runs with a
    still-blocked probe.
- **Isolation family,** verbose, HEAD worktree vs candidate, same host.
  - The strict tests are unchanged. Only ReadWriteUnique4 and
    TemporalRangeIntegrity fail, both nightly-filed and failing at HEAD.
  - `TestPort_IsolationPreparedTransactions` goes SKIP → PASS (2/2
    standalone as well).
  - `TestPort_IsolationSuite` subtests: PASS goes 38 → 48.
  - Summed test time goes 571 → 655 s.
- **Targeted reruns.** TuplelockUpgradeNoDeadlock passes 2/2 with the
  probe.

## Follow-up evidence (2026-10-10, TestPort_IsolationEvalPlanQual)

EvalPlanQual failed in five nightlies between 2026-09-28 and 2026-10-09.
Every failure first diverged at a `COMMIT` step printed `<waiting ...>`. A
COMMIT slows down on a loaded host (WAL flush) but never takes a lock wait.

- **Load A/B.** Under synthetic load (`tmp/m155/load.sh`: 12 CPU burners
  plus a looping fsynced 2 GB writer), runs alternated between the two
  runners:
  - the pre-probe runner (`2544b2c57~1`) failed 2 of 3, both times with that
    signature;
  - the probe runner passed 3/3.
- **Same signature elsewhere.** `TestPort_IsolationAlterTable1` failed the
  same way (`step sc1: COMMIT; <waiting ...>`).

## Not covered (ledgered)

- **Waits that report no wait event.** These pay the 1.5 s cap per step
  instead of 300 ms, which accounts for most of the added time:
  - waits for older snapshots in DETACH PARTITION CONCURRENTLY, CREATE /
    DROP INDEX CONCURRENTLY and REINDEX CONCURRENTLY (PG reports these as
    `Lock: virtualxid`);
  - the in-place catalog update in intra-grant-inplace;
  - one wait in tuplelock-upgrade-no-deadlock (12.5 → 16.3 s).
- **`pg_isolation_test_session_is_blocked` itself** (and
  `pg_blocking_pids`, OID 2561) is still unimplemented.
- **PreparedTransactions is still non-strict.** Re-promoting it to strict
  and updating the inventory are left for the nightly to confirm.

## Follow-up (2026-10-20): snapshot waits report Lock:virtualxid

Trigger: `TestPort_IsolationAlterTable1` failed in the 2026-10-20 nightly with
`step sc1: COMMIT; <waiting ...>`. It passed alone on an idle host. A COMMIT
slowed by load ran past `blockProbeCap`, and past the cap the runner labels a
step waiting without the probe's agreement. The cap exists only for goopg
waits that report no wait event, so the fix is to make those waits report what
PG reports, then retire the cap.

**Slice 1.** PG's `WaitForOlderSnapshots` (indexcmds.c) sleeps in
`VirtualXactLock`, shown as Lock:virtualxid. goopg's counterparts now report
the same while they actually wait (`reportVirtualXIDWait`):

- `WaitForSlotsToCommit`: CREATE / DROP / REINDEX INDEX CONCURRENTLY;
- `WaitForPinnedSnapshotsReleased`: DETACH PARTITION CONCURRENTLY.

Pin: `TestSnapshotWaitsReportVirtualXIDLock`, which fails without the change.

**Measured.** I counted steps where the cap fired without a lock wait over a
full `TestPort_Isolation*` run
(`analysis/m-nightly/ai-20261020-013101-002/`). They fell from 61 to 31:

- detach-partition-concurrently-1 (9) went to 0;
- drop-index-concurrently-1 (5) went to 0;
- detach-partition-concurrently-4 (3) went to 0;
- vacuum-skip-locked went from 27 to 14.

**Still unreported** (the cap stays until these report):

| spec | cap fires |
|---|---|
| vacuum-skip-locked | 14 |
| intra-grant-inplace | 8 |
| tuplelock-upgrade-no-deadlock | 3 |
| reindex-schema | 2 |
| read-only-anomaly-3 | 2 |
| insert-conflict-specconflict | 2 |

## Follow-up (2026-10-20, slice 2): the remaining waits report, and the cap becomes a hang guard

Goroutine dumps were taken at the moment the cap fired, from the test
server's pprof endpoint (`GOOPG_PPROF_ADDR`). Each remaining wait gained PG's
event:

| spec | goopg wait site | PG wait | event |
|---|---|---|---|
| intra-grant-inplace | `waitPgClassRowMarkReleased` (5 ms poll) | heap_inplace_lock → XactLockTableWait | Lock:transactionid |
| tuplelock-upgrade-no-deadlock | `acquireTupleLock` (lock manager) | LockTuple → ProcSleep | Lock:tuple |
| reindex-schema | `acquireReindexLocks` (lock manager) | LockRelationOid | Lock:relation |
| insert-conflict-specconflict | `WaitForSpecToken` | SpeculativeInsertionWait | Lock:spectoken |
| read-only-anomaly-3 | `waitForSafeSnapshot` | GetSafeSnapshot → ProcWaitForSignal | IPC:SafeSnapshot |

Notes on the table:

- **Tuple and REINDEX locks:** `TryAcquire` goes first and the event is
  reported only on a real wait. TryAcquire keeps Acquire's FIFO rule, so the
  grant order is unchanged.
- **The probe:** `sessionWaitIsBlocking` now counts a step as blocked on a Lock
  wait *or* IPC:SafeSnapshot. That is pg_isolation_test_session_is_blocked's
  test (lockfuncs.c: the heavyweight lock graph plus
  GetSafeSnapshotBlockingPids).

**Cap fallbacks over the isolation family:** 31 → 6 after the reporting.
Instrumenting with the spec path showed that the earlier per-test attribution
had misassigned suite subtests: the "vacuum-skip-locked 14" belonged to
interleaved specs. None of the remaining six steps waits in PG's expected
output: they are COMMIT, ROLLBACK and CREATE steps that were merely slow, the
false positive behind the nightly AlterTable1 failure.

**The cap.** `blockProbeCap` is now a 60 s hang guard, a var so the unit test
can shorten it, not a blocking rule. isolationtester bounds a stuck step only
by max_step_wait (360 s). With the guard:

- the first full-family run exposed the spectoken wait (two 60 s hits), which
  was then reported;
- the family passes in 563 s, against 606 s with the 1.5 s cap, with no test
  slower by more than 5 s.

Pins:

- `TestSessionWaitIsBlockingMatchesIsolationTester` (the probe's verdict);
- `TestAwaitStepOrBlockAsksTheProbe` (the guard, at a shortened cap);
- `TestSnapshotWaitsReportVirtualXIDLock` (slice 1).

## Follow-up (2026-10-21, AI-20261020-013101-005): a pending step is waited for, not given 5 s

**Failure.** On the 2026-10-20 nightly, `TestPort_IsolationMultipleCic`
printed `step s1i: <... timed out waiting>` and the same for `s2i`. Its
teardown then failed with `driver: bad connection`, because the runner had
cancelled both CREATE INDEX CONCURRENTLY steps and reused their connections.
The spec passes alone in 2.0 s, and five runs at 2× CPU oversubscription took
2.6–3.6 s. The failure fell in the same 01:45–01:53 window as the two
cluster-start timeouts (`m-nightly-cluster-start-timeout-diagnostics.md`), so
the host or the testport unit was slowed then.

**Cause.** The runner gave each pending (reported `<waiting ...>`) step a 5 s
window, `drainWindow`, then printed a goopg-only `<... timed out waiting>`
line and cancelled the step. The window applied:

- before its session's next step;
- at the end of the permutation;
- to its notice blockers;
- to steps gated behind a blocker step.

**PG.** isolationtester sets no such window. In run_permutation and
try_complete_step, a waiting step is polled until it completes. Only after
max_step_wait (360 s, or 2 × PG\_TEST\_TIMEOUT\_DEFAULT) does it cancel the
step, and after twice that it exits.

**Change.** `drainWindow` became `pendingStepGuard`, a 60 s hang guard, the
same bound as `blockProbeCap`. It is a var so a test can shorten it. The
cancel-and-reuse path behind the guard is unchanged.

**Measured.** A temporary trace at every guard site over the full isolation
family at HEAD found one hit: step `drop` of drop-index-concurrently-1, inside
the shared-cluster `TestPort_IsolationSuite`. There, DROP INDEX CONCURRENTLY
waits for other specs' transactions. The dedicated pass-required tests never
reached the 5 s window. With the guard:

- the family passes, 122/122, in 561.7 s (557.7 s before);
- the suite subtest drop-index-concurrently-1 turns from a timed-out SKIP into
  a PASS;
- suite SKIPs fall from 84 to 79.

**Pin.** `TestIsolationRunnerWaitsForSlowPendingStep` runs a goopg-local spec,
`internal/testport/testdata/isolation/specs/slow-pending-step.spec`. Its
expected output was captured from PG 18.3's isolationtester. In the spec, s2u
waits on s1's row lock and completes 6 s later with a lock\_timeout error.
With a 5 s guard the test fails exactly as the nightly did.

## Follow-up (2026-10-22, AI-20261022-011400-001): global setup and teardown get a hang guard

**Symptom.** The 2026-10-22 nightly (sha f39f376e9) timed out the whole testport package after go test's 2 h
limit. Two specs had been blocked for 1h49m on the shared suite cluster:

- `alter-table-1` in its global teardown (`RunSpec` → `execConn`);
- `partial-index` in its global setup (`execConnSetupCapture`).

The host was idle the whole time, so this was a wait, not a spin. Nothing recorded what either block was
waiting on. Neither reproduces on demand:

- both specs pass alone in 17 s;
- `TestPort_IsolationSuite` passes in 127 s under the nightly's own cgroup (high=6G, max=8G, GOMEMLIMIT=5GiB);
- the whole `TestPort_Isolation*` family passed in the gates of three successive commits.

**Why the runner hung with it.** isolationtester runs global setup and teardown with a plain `PQexec` and no
timeout. Its only bounds are on steps: `max_step_wait` cancels a step, and twice that exits. Upstream runs one
spec at a time, so a stuck block hangs one spec. This suite runs every spec in parallel on one shared cluster,
so a setup or teardown waiting on a lock held by another spec's session stalls the whole package.

**Rule.**

- **Each global setup and teardown block runs under `IsolationRunner.SetupTeardownGuard`.** The default is
  `defaultSetupTeardownGuard`, 5 min. That is far above any real setup; upstream's die limit for a step is
  720 s.
- **On expiry, `runGuarded` snapshots the server first, then cancels the block.** `serverStateSnapshot` reads
  `pg_stat_activity` (non-idle backends) and `pg_locks` over a fresh connection. Taking it before the cancel
  means it shows the waiting block, and the spec's own sessions are still open, so a session holding the
  blocking lock shows too.
- **The spec fails with that snapshot in its error.** A teardown that merely errors is still ignored, as
  isolationtester ignores it; only a teardown still running at the guard fails the spec.

**Pin.** `TestIsolationRunnerGuardsBlockedTeardown`. The test's own connection holds an uncommitted UPDATE of
the row that `guard-blocked-teardown.spec`'s teardown updates. With a 3 s guard, `RunSpec` returns after
~3.2 s, and its error names the guarded teardown. The snapshot shows the teardown as `active | Lock |
transactionid`, and the holder as `idle in transaction`. Ignoring the guard's expiry fails the test.

**Found while pinning.** goopg's `pg_locks` lists no `transactionid` rows. PG would show the holder's
ExclusiveLock on its own xid, granted, and the waiter's ShareLock on it, not granted (`XactLockTableWait`);
goopg exposes the wait only through `pg_stat_activity.wait_event`. This is recorded in the deferral ledger. A
`LOCK TABLE … IN ACCESS EXCLUSIVE MODE` held in an open transaction did not block another session's `DROP
TABLE` in this test either, so the pin uses a row-lock wait.
