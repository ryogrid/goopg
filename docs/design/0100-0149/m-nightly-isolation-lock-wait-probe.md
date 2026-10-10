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
