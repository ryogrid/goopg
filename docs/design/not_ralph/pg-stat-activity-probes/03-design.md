# 03 — Detailed design: pg_stat_activity probes in goopg

## 1. Goals

1. Faithful `pg_stat_activity` parity with vanilla PG 18.3 for the waits a
   client can actually observe under load (state, wait_event_type,
   wait_event, query, timestamps).
2. Recording cost stays allocation-free and sweep-neutral on the statement
   hot path.
3. No change to the existing `internal/utils/activity` foundation beyond
   adding interned event names.

Non-goals: per-LWLock Go-mutex attribution (see §4), pg_stat_io changes,
sampling-based profiling inside the server.

## 2. PG → goopg probe mapping

| PG blocking primitive | PG wait event | goopg choke point | status at base |
|---|---|---|---|
| `ProcSleep` / heavyweight lock wait (`lock.c`) | `Lock:{relation,tuple,...}` | `executor/context.go` relation-lock paths | done |
| `XactLockTableWait` | `Lock:transactionid` | `access/transam/manager.go: WaitForXID` | **add (G1)** |
| row-lock conflict wait (UPDATE/DELETE/FK) | `Lock:transactionid` | same `WaitForXID` (callers incl. `operators_storage.go:273`, `operators_fk.go`) | **covered by G1 fix** |
| advisory lock wait | `Lock:advisory` | `executor/advisory.go` wait loop | **add (G3)** |
| `pg_sleep` (`misc.c`) | `Timeout:PgSleep` | `executor/expr.go: evalPgSleep` | **add (G2)** |
| LWLock acquisition (`lwlock.c`) | `LWLock:*` | Go mutex/cond parks | policy: not mapped (§4) |
| buffer pin wait | `BufferPin:BufferPin` | bufpool pin callback | done |
| `md.c`/`fd.c`/`slru.c`/AIO IO | `IO:*` | storage-manager + AIO callbacks (`initdb/open.go`) | done |
| WAL flush/write (`xlog.c`) | `IO:WALSync/WALWrite` | wal writer callbacks | done |
| hash spill tmp files | `IO:BuffileRead/Write` | `executor/spill.go` | done |
| `pqcomm.c` secure_read/write | `Client:ClientRead/Write` | `postmaster/server.go` conn loop | done |
| main-loop states (`postgres.c`) | state column | `postmaster/dispatch.go` active/idle wiring + session txn transitions | done |

## 3. New probe implementations

### 3.1 G1 — `WaitForXID` ⇒ `Lock:transactionid`

Instrument the choke point once (P2), not the seven callers:

```go
// manager.go WaitForXID — around the cond-wait loop only (not fast paths).
if m.activity != nil {
    m.activity.WaitEventStart(procNum, activity.WaitTypeLock, activity.WaitTransactionID)
}
... loop { commitCond.Wait() } ...
if m.activity != nil {
    m.activity.WaitEventEnd(procNum)
}
```

Details:
- **Identity resolution is per call, not per Manager.** One
  `*transam.Manager` is shared by all backends (`TxnMgr` at
  `internal/executor/context.go:79`, wired server-wide), so a procNum stored
  on the Manager would attribute every waiter's wait to a single slot.
  Instead, `WaitForXID` resolves registry + procNum per call via the existing
  package-level goroutine map:
  `activity.LookupCurrentGoroutine() (*ActivityRegistry, int32, bool)`
  (registry.go:874, populated by serveConn). `false`/nil = probing disabled —
  mirrors upstream `pgstat_track_activities == false`. This also keeps
  invariant I3 (single writer per slot) true by construction: the lookup and
  both stores run on the waiter's own executor goroutine.
- Scope: wrap only the actual blocking loop. Cancellation wakeups
  (`ctx.Done` broadcast goroutine) stay outside the window so a cancelled
  wait does not leave a stale event (balance is also protected by `defer`).
- The lock_timeout deadline path returns early — use one `defer`red
  `WaitEventEnd` keyed to the loop entry so every return path balances
  (`ctx.Err()`, `ErrLockTimeout`, normal). The deferred End runs before
  `waitMu.Unlock` but takes no locks — safe.

### 3.2 G2 — `evalPgSleep` ⇒ `Timeout:PgSleep`

Both sleep paths get the window (the nil-`ctx.Ctx` branch does a bare
`time.Sleep(d)` at expr.go:18064-18067, the normal branch selects on
`time.After` / ctx.Done). One defer-balanced Start/End pair around the
branch covers both:

```go
ctx.Activity.WaitEventStart(ctx.ProcNum, activity.WaitTypeTimeout, activity.WaitPgSleep)
defer ctx.Activity.WaitEventEnd(ctx.ProcNum)
// existing select / time.Sleep logic unchanged
```
(`ctx.Activity`/`ctx.ProcNum` are already wired on ExecContext; guard nil.)

### 3.3 G3 — advisory locks ⇒ `Lock:advisory`

Confirmed during review: a real park exists — the waiter registers itself
(advisory.go:220) and blocks on a `ready chan` (:47). Wrap that park with
the same defer-balanced pattern.

### 3.4 Interning

New event names (`transactionid` exists already) are added to the init-time
maps in `registry.go` only if absent. No string constants reach the hot path
at runtime; `packWaitStrings` keeps returning a packed uint32.

## 4. Policy: why LWLock stays unmapped

PG reports `LWLock:*` because those waits are long-lived kernel futex parks
inside shared-memory spinlock/lwlock code — observable and meaningful.
goopg's equivalents are Go mutex/channel parks scheduled by the runtime;
they are (a) typically sub-microsecond, and (b) not attributable to a named
resource without adding instrumentation cost to primitives used by every
query. Reporting them would produce noise classes with no upstream
counterpart. Decision: emit
**no** LWLock events; document that active backends between blocking probes
are "on CPU", matching PG's NULL wait_event. Revisit only if a specific
long-park site (e.g. bufpool eviction storm) needs visibility — it would be
added as its own named event, not as LWLock.

## 5. Invariants & testing

- I1 Balance: every `WaitEventStart` pairs with exactly one `WaitEventEnd`
  on all return paths (defer where control flow branches).
- I2 Zero allocations in Start/End (compile-time by signature; verified with
  a `testing.AllocsPerRun` unit test on the registry path).
- I3 Single writer: only the owning connection goroutine writes its slot;
  WaitForXID writes the *waiter's* slot from the waiter's own goroutine
  (true by construction — the call runs on the executor goroutine).
- I4 Snapshot parity: after forcing a block (test helper transactions),
  `Snapshot()` shows `state='active'`,
  `wait_event_type/wait_event = Lock/transactionid` (unit test in
  `internal/access/transam`), and `Timeout/PgSleep` for pg_sleep (executor
  test).
- Live validation: `scripts/pgbench-wait-sample.sh` (Phase 4 of the plan)
  samples `pg_stat_activity` client backends at 500 ms during
  `pgbench -N -c 50 -j 8 -T 60 -s 10` and aggregates wait events; expected
  distribution: idle≈ClientRead dominant remainder, active mostly NULL
  (CPU), occasional IO/Lock rows; pprof CPU top shows network read +
  executor work, no time attributed to the new probe sites themselves.

## 6. Risks

| risk | mitigation |
|---|---|
| Probe on WaitForXID adds latency to contended upserts | two atomic stores + map lookups (~50 ns) vs multi-ms waits; negligible |
| Stale wait event on panic between Start/End | defer-based End; server abort path clears slot state anyway |
| procNum unavailable inside Manager (shared object) | per-call `activity.LookupCurrentGoroutine()`; unregistered goroutines (unit tests, bg workers) = probing disabled |
