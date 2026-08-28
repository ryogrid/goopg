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
| buffer pin wait | `BufferPin:BufferPin` | bufpool pin callback | done; emission decoupled from track_io_timing (probe-audit fix) |
| `md.c`/`fd.c`/`slru.c`/AIO IO | `IO:*` | storage-manager + AIO callbacks (`initdb/open.go`) | done; windows now fire regardless of track_io_timing, only *_time stays gated (probe-audit fix); WAL flush attributed per committer |
| WAL write drain (`XLogWrite` pwrite) | `IO:WALWrite` | `Writer.flushWindows` stage 1 | done (split from the old whole-flush window) |
| WAL fdatasync barrier (`XLogFlush`) | `IO:WALSync` | `Writer.flushWindows` stage 2, per COMMITTER | done (attribution fix) |
| committers parked on the WAL write lock | `LWLock:WALWriteLock` | `flushUpToBackend` park (`acquireOrWait`) | done — first LWLock-class emitters; named tranches with real upstream identity, per the §4 amendment |
| committers waiting for in-flight stripe inserts to publish | `LWLock:WALInsert` | `flushUpToBackend` (`waitInsertionsToFinish`) | done |
| WAL-record insertion into WAL buffers (stripe lock) | `LWLock:WALInsert` | `lockStripeWithEvent` around each per-stripe mutex (`stripe_append.go`; TryLock fast path — fires only on genuine contention) | done |
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

## 4. Policy: why there is no blanket LWLock mapping

What upstream actually does (three tiers):

- **Spinlocks** (`src/include/storage/s_lock.h`): no wait event on
  acquisition — they are designed to bound the protected section to a few
  instructions and busy-wait otherwise. Only the backoff sleep inside the
  spin loop surfaces, as `Timeout:SpinDelay`
  (`storage/lmgr/s_lock.c:148`).
- **LWLocks**: reported, yes — from ONE choke point inside the primitive
  itself (`storage/lmgr/lwlock.c:739`,
  `pgstat_report_wait_start(PG_WAIT_LWLOCK | lock->tranche)`), so every
  caller inherits per-resource names for free. Note that LWLocks are not
  inherently short: `WALWriteLock` is held across an entire WAL flush, which
  is precisely why reporting them matters upstream. "Shared-memory lock" is
  therefore not synonymous with "short wait".
- **Heavyweight locks**: `ProcSleep` → class `Lock` (see 01 §3).

Why goopg does not mirror the LWLock tier 1:1:

1. There is no central analog to instrument once. Upstream's value comes
   from lwlock.c being a single function that knows each lock's tranche
   identity. goopg's corresponding critical sections are anonymous
   `sync.Mutex`/`RWMutex`/channel parks on individual structs, carrying no
   resource name. Reproducing tranche-style reporting would mean either
   hand-wrapping every site (instrumentation on primitives every query
   touches) or introducing a central instrumented lock type (hot-path
   refactor of storage internals) — significant cost for signal whose
   duration distribution is unproven.
2. The potentially-long waits goopg actually has are already individually
   named at their natural choke points: storage/AIO/WAL IO callbacks,
   relation locks, `WaitForXID`, advisory, client read/write, buffer pin,
   hash spill. These are the analogs of upstream's long-held tiers
   (IO-bound and heavyweight-lock classes).
3. What remains under raw mutexes is bounded bookkeeping (buffer-table
   lookups, proc-array scans, registry maps) — the working analog of
   upstream's spinlock tier, which upstream also deliberately keeps out of
   pg_stat_activity.

Amendment (probe-audit follow-up): the "no LWLock events" decision applies
to ANONYMOUS struct mutexes. Where a Go lock is the direct analog of a named
upstream tranche — the WAL write lock (`writeMu` ≈ `WALWriteLock`) and the
insert-stripe wait (≈ `WALInsert`) — reporting `LWLock:<tranche>` at that one
choke point is exactly the lwlock.c pattern (report at the primitive with
known identity), not the noise case this policy was written against.

Residual risk this policy accepts, and its coverage: nothing prevents a
future holder from parking many goroutines for a long time under a plain
struct mutex (the WALWriteLock lesson applies — long holds are possible).
The policy handles this empirically instead of preventively: set
`GOOPG_MUTEX_PROFILE_RATE` / `GOOPG_BLOCK_PROFILE_RATE` and read the pprof
mutex/block profiles to detect unexpectedly long or contended parks; a site
proven significant is promoted to its own named event AT THAT SITE (exactly
the way `WaitForXID` and advisory were wired in this work), never folded
into a synthetic "LWLock" class that would have no upstream name
counterpart. Decision: emit **no** LWLock events today; active backends
between blocking probes show NULL wait_event (= on-CPU), matching PG.

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
