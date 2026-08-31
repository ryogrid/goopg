# 0118-0111 — CREATE INDEX CONCURRENTLY wait honours `lock_timeout` (`prepared-transactions-cic` PROMOTED)

- **Milestone:** M0118-0009 (isolation output-parity, two-phase-commit group)
- **Status:** accepted
- **Spec promoted:** `postgres/src/test/isolation/specs/prepared-transactions-cic.spec`
- **Builds on:** [0118-0110](0118-0110-same-backend-two-phase-commit.md) (same-backend
  2PC enabler), [0118-0031](0118-0031-*) (`mvcc.WaitForSlotsToCommit` primitive),
  [0118-0107](0118-0107-*) (`lock_timeout`/`statement_timeout` lock-wait parity).

## Problem

`prepared-transactions-cic.spec` verifies that `CREATE INDEX CONCURRENTLY` interacts
correctly with a prepared transaction:

```
session s1:  w1 { BEGIN; INSERT INTO cic_test VALUES (1); }
             p1 { PREPARE TRANSACTION 's1'; }
             c1 { COMMIT PREPARED 's1'; }
session s2:  setup { SET lock_timeout = 10; }
             cic2 { CREATE INDEX CONCURRENTLY on cic_test(a); }
             r2   { SET enable_seqscan to off; SET enable_bitmapscan to off;
                    SELECT * FROM cic_test WHERE a = 1; }

permutation  w1 p1 cic2 c1 r2
```

Expected (PG 18.3): `cic2` parks waiting for the prepared transaction to drain, and
because `lock_timeout = 10ms` it is cancelled — `ERROR: canceling statement due to
lock timeout`. Then `c1` commits the prepared row and `r2` reads it back.

After the same-backend 2PC enabler (0118-0110), goopg keeps a prepared transaction's
MVCC slot **active** until `COMMIT/ROLLBACK PREPARED`. So `cic2` already captured s1's
slot in its start-time snapshot set and waited on it via
`mvcc.WaitForSlotsToCommit`. But that wait honoured only the statement context's
cancellation (`ctx.Done`) — **not** the session's `lock_timeout`. The spec's `cic2`
never timed out: it blocked forever (until the runner's own deadline), so the
permutation diverged at `cic2`.

This is the exact mismatch that upstream's comment in the spec calls out: a prepared
transaction's locks have no pid, so the isolation tester never detects the block — the
**only** thing that ends `cic2` is `lock_timeout`.

## Fix

`lock_timeout` already travels down the request context as a duration via the
`lockwait` package (`lockwait.WithTimeout` set in `dispatch.go`, read by
`lockmgr.ProcSleep`). The CIC drain wait must arm that budget the same way the
heavyweight lock manager does.

### `internal/mvcc/manager.go` — `WaitForSlotsToCommit`

Arm a `lock_timeout` timer from `lockwait.Timeout(ctx)` when present. The existing
helper goroutine (which broadcasts the commit cond on `ctx.Done`) gains a third
`select` arm: when the timer fires it closes a `timedOut` channel and broadcasts the
cond. The wait loop checks `timedOut` (non-blocking) on each wakeup and returns
`lockwait.ErrLockTimeout`. The timer is re-armed from the moment **this** wait begins,
mirroring upstream's `enable_timeout_after(LOCK_TIMEOUT)` at the top of `ProcSleep`
(`d` is a duration, not an absolute deadline). A nil `lockTimeoutCh` (no budget)
blocks forever in the `select`, so the no-`lock_timeout` path is byte-unchanged.

### `internal/executor/operators_ddl.go` — CIC drain site

The wait error is mapped through the existing `lockWaitTimeoutError` helper: a
`lockwait.ErrLockTimeout` (or a `statement_timeout` `context.DeadlineExceeded`)
surfaces the matching `canceling statement due to {lock,statement} timeout` message;
any other cancellation keeps the prior generic `57014 CREATE INDEX CONCURRENTLY
cancelled`. This reuses the same lock-wait → SQLSTATE mapping every other lock-wait
site uses (no sibling cancellation path).

## Why this is safe (blast radius)

- `WaitForSlotsToCommit` only changes behaviour when `lockwait.Timeout(ctx)` is set —
  i.e. the session has a non-zero `lock_timeout`. With the default `lock_timeout = 0`
  the wait is byte-identical to before.
- The only caller is the CIC drain in `createIndex`. No other code path is touched.
- The `lock_timeout` plumbing (`dispatch.go` → `lockwait.WithTimeout`) already existed
  for `timeouts.spec` (0118-0107); this only consumes it at one more wait site.

## Oracle

Upstream: `src/backend/storage/lmgr/proc.c` (`ProcSleep` arms `LOCK_TIMEOUT` via
`enable_timeout_after`), `src/backend/tcop/postgres.c` (`ProcessInterrupts` raises
`ERRCODE_LOCK_NOT_AVAILABLE` "canceling statement due to lock timeout" on
`LockTimeoutPending`). goopg keeps its established internal convention of mapping a
lock-wait timeout to SQLSTATE `57014` with the lock-timeout message
(`lockWaitTimeoutError`); the isolation framework compares only the message, which is
byte-identical to PG 18.3.

## Verification

- `TestPort_IsolationPreparedTransactionsCIC` (new, `runIsoSpecStrict`) — the full spec
  matches PG 18.3 byte-for-byte (`status=pass`).
- `go test -race ./internal/mvcc/...` — green (concurrency change to the condvar wait).
- `go build` + `go vet` clean. pgbench smoke = pre-commit hook.

## Remaining in the two-phase-commit group

- `prepared-transactions` — full 1500-permutation SERIALIZABLE SSI verification across
  held prepared xacts (mechanism in place; validate byte-for-byte).
- `stats` — needs the cumulative `pg_stat_*` subsystem on top of 2PC.
