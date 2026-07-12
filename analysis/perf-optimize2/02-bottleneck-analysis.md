# 02 — Bottleneck analysis (goopg, c=50 simple-update)

All profile references are to `runs/20260712_114859/profiles/` analyzed
against the archived `goopg.bin` (`go tool pprof`). CPU profile window:
120 s inside the 180 s run; total samples 352.6 s (≈2.9 cores busy of 16 —
goopg is *not* CPU-saturated; it is overhead-bound and serialization-bound).

## Ranked bottlenecks

### #1 — `runtime.Stack` on every WAL append: **57 % of all CPU** (NEW)

The single largest consumer in the entire profile is goroutine-ID lookup:

```
runtime.Stack                                    cum 199.21s = 56.5 %
  └─ 100 % from internal/activity.goroutineID
       └─ 100 % from activity.LookupCurrentGoroutine
            └─ 100 % from internal/wal.(*state).stripeNum   (writer.go:1870)
```

`stripeNum()` — which selects the per-backend WAL-buffer stripe — calls
`activity.LookupCurrentGoroutine()` on **every WAL append**, and that helper
derives the goroutine ID by calling `runtime.Stack(buf[:64], false)`
(`internal/activity/activity.go:186-207`). Two compounding problems:

1. It runs on the hottest path in the system (every heap-update record,
   every commit record — a simple-update txn appends several records).
2. `runtime.Stack` is far more expensive than "read 20 bytes": even with a
   64-byte buffer, the runtime **formats the entire call stack** (symbol
   lookup + file/line for every frame — `runtime.pcvalue`/`step`/
   `printFuncName`/`printArgs`/`fileLine` account for the bulk of the 57 %),
   and executor stacks are deep. The write side (`gwrite`) also funnels every
   formatted byte through `recordForPanic`.

The downstream `LookupCurrentGoroutine` then does a `map[string]` lookup
under `sync.RWMutex` per call (0.5 % more CPU, and 28.5 % of the mutex-wait
profile rides this path).

This explains the paradox in the aux runs: GC frames are tiny in the CPU
profile (mallocgc cum 4.8 %, no gcBgMarkWorker in the top 60) yet
`GOGC=400` gains +43 % — the profile denominator is inflated by stack
formatting, and much of the futex/runtime-lock churn (runtime.futex flat
20.2 %) is secondary traffic from this storm plus profiling.

PostgreSQL equivalent: a backend knows its own identity as `MyProcNumber`
(a global set once at backend start) and picks its WAL insert lock as
`MyProcNumber % NUM_XLOGINSERT_LOCKS` — zero lookup cost. See
`03-postgres-mechanisms.md` §1. Fix design:
`04-improvement-designs/fix-01-wal-stripe-backend-id.md`.

Amdahl bound: removing 57 % of CPU alone caps at ≈2.3× *CPU* headroom; the
observed A5+A2 deltas suggest ≥1.5–2× TPS realistically once the commit
pipeline (below) can absorb it.

### #2 — Profiling-instrumentation artifact: +38 % TPS when disabled (methodology)

`GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1` (kept for parity
with the 2026-05 run) makes the Go runtime capture a stack for **every**
contended mutex event: `runtime.(*mLockProfile).captureStack` feeds 98.9 %
of `tracebackPCs` (13.4 % of CPU), and every unlock-with-waiters pays a
futex wake plus bookkeeping. Aux A5 (rates 0, and — attribution caveat —
also without the concurrent 120 s CPU-profile/30 s trace/heap collection
that ran only during the headline): 1,754 vs 1,269 TPS; the +38 % is the
cost of all instrumentation combined, not the mutex/block rates alone.
Recommendation: future headline runs should use rate 0 (or sampled rates,
e.g. mutex fraction 100) and capture contention in a separate diagnostic
run; recorded in the fix index as a benchmarking-practice item, not a code
fix.

### #3 — Commit pipeline: correct shape, expensive per-transaction cost

Group commit **works**: 143 fdatasync barriers/s at 1,269 TPS ≈ 8.9
txns/flush (c=1 degenerates to 1 flush/txn as expected). The block profile
shows the expected shape — 72.9 % of block time is under
`executor.(*Context).CommitTransaction` (`runtime.selectgo` 40.1 % +
`chanrecv` 26.9 %: backends parked on the group-flush `done` channel and the
writer's ops channel), mirroring PG's 77.8 % `LWLock:WALWrite` wait share at
15.5 k TPS (01 §4). The difference is what each transaction *costs* around
that wait:

- **Two commit records per commit.** The xact-marker hook appends a legacy
  commit record *and* a canonical `XLOG_XACT_COMMIT` record
  (`internal/initdb/open.go:923` and `:942`), each a separate append (and
  channel round-trip where the striped fast path is not taken), before
  `FlushUpTo` (`open.go:967`). PG writes exactly one record
  (`RecordTransactionCommit`, xact.c).
- **Per-flush structured log line.** `flushUpTo` emits
  `l.Info("walwriter flush", …)` on every barrier
  (`internal/wal/writer.go:1962`) — 143 slog lines/s on the commit critical
  path (25,784 lines in the headline server.log), each formatted while
  committers wait.
- **Channel round-trips per append** on the non-striped path
  (`Writer.send()`), and the flush queue handoff per commit
  (`FlushUpTo` → `groupFlushReq` → signal channel → `done` close).
- **No pre-enqueue already-flushed fast exit.** goopg's background
  walwriter *does* pre-flush — `internal/initdb/open.go:2126` calls
  `FlushUpTo(WrittenLSN())` on a 200 ms ticker (note: the `^uint64(0)`
  sentinel mentioned in `docs/design/wal_fsync_flow_primary.md` and the
  comment at `open.go:188` is stale — the code was fixed; both documents
  should be updated). But a committer whose LSN is already durable still
  pays the full handoff: `Writer.FlushUpTo` (`writer.go:859`) enqueues a
  `groupFlushReq`, pokes the signal channel, and blocks on `done` before
  the *writer goroutine* discovers the LSN is already flushed (the fast
  exit at `writer.go:1919` is post-dequeue). PG's `XLogFlush` checks
  `record <= LogwrtResult.Flush` **before** taking any lock and returns
  with zero coordination.

Fix designs: `fix-02-single-commit-record.md`,
`fix-03-commit-pipeline-streamline.md`.

### #4 — GC / allocation pressure (still real, now second-order)

`GOGC=400` gained +43 % (A2). The per-query allocators of the 2026-05 report
(`planner.Plan`, `updateOp.Next`) are still present but no longer dominate
the CPU profile. A per-statement memory-context mechanism exists and **is**
engaged on the OLTP path (`internal/mctx/mctx.go`, M0107-0001;
`dispatchSimpleQueryViaExecutor` acquires a statement mctx at
`internal/server/dispatch.go:288`) — but the hot allocators do not route
their allocations through it, so per-statement garbage still reaches the GC.
This item stays on the backlog behind #1/#3 because its measured ceiling
(+43 % with a blunt GOGC knob) is smaller than #1's, and #1's fix removes
allocation too (every `goroutineID()` call allocates a 64 B buffer plus a
string).

Note: the earlier `mvcc.Manager.mu` bottleneck (92 % of mutex delay in the
May report) is **resolved** — `captureSnapshot` is 1.5 % cum CPU and no mvcc
lock appears in the mutex-profile top; ProcArray slots are atomic.
Remaining snapshot cost (O(slots) scan + sort per RC statement) is a
candidate for PG14-style `xactCompletionCount` snapshot reuse
(`fix-07-snapshot-reuse.md`, low priority).

### #5 — Startup: DDL recovery re-reads the whole WAL ~20 times (NEW)

Opening the scale-100 data directory takes ~28 s. The allocation profile
(cumulative from process start) shows **200 GB allocated in
`wal.readStreamFrom` / `os.ReadFile`, 81 % of all allocation**, called from
`wal.ReadAll(walDir, 0)` — and `ReadAll` is invoked by **each** of the
**26** `internal/initdb/*_ddl_recovery.go` modules (view, index, domain,
matview, role-config, cast, aggregate, tablespace, operator, foreign-server,
access-method, transform, …): every one of them scans the *entire* WAL from
LSN 0 at startup, each reading every segment file into memory
(`os.ReadFile` cum ≈34 GB ≈ 20 full passes over the 1.7 GB WAL). PG replays
WAL **once** from the checkpoint redo pointer and dispatches each record to
its resource manager (`03-postgres-mechanisms.md` §9). Fix:
`fix-05-startup-single-pass-recovery.md`. (Also the reason the driver's
20 s readiness window had to be raised — an operational cost, not just a
benchmark artifact.)

## Decision questions answered (Q1–Q5 from the plan)

| question | verdict | evidence |
|---|---|---|
| Q1 fsync-bound? | **No** (relative to PG). | 143 fsync/s @1,269 TPS (batch ≈8.9); with `synchronous_commit=off` both sides, gap *widens* to 16.1× (A3). c=1 is fsync-bound on both (1 flush/txn). Background pre-flush verified active (`open.go:2126`). |
| Q2 GC-bound? | Partially — second order. | A2 +43 %; but GC frames <7 % CPU; #1 is upstream of much of the pressure. |
| Q3 lock/serialization-bound? | Yes, but same *shape* as PG. | Block profile 73 % in commit wait vs PG 78 % WALWrite wait; goopg additionally loses ~4.2× scalability (A4: ×7.3 vs ×30.4 scaling 1→50). |
| Q4 parse/plan-bound? | **No.** | A1 `-M prepared` −8 %; planner allocs no longer dominant. |
| Q5 protocol/syscall-bound? | No single syscall storm in the OLTP window. | `Syscall6` 10.9 % flat (writes+futex); ClientRead-equivalent idle is dwarfed by #1. |

## §7 COPY / bulk-load attribution (pgbench -i, user-requested)

Diagnostic: `pgbench -i -s 20` (2 M rows) against a throwaway goopg cluster,
60 s CPU profile mid-COPY (`copydiag/`). Result: 91.3 s generate phase —
same ~22 k rows/s as the scale-100 run, so the diagnostic is representative.

CPU during COPY:

| cost | share | reading |
|---|---:|---|
| `runtime.Stack` from `wal.stripeNum` (#1 above; cum, incl. `pcvalue`/`step`/print machinery) | **≈60 %** | same per-append goroutine-ID storm — COPY appends WAL per row |
| `syscall.pwrite` | 16.6 % | many small `pwrite`s draining WAL/data pages |
| `runtime.memmove` + `memclr` | ≈11 % | per-row buffer copying |
| WAL flush barriers | 688 over 93 s = 7.4/s | **not** fsync-bound |

Structural gaps vs PG (see `03-postgres-mechanisms.md` §7 for the PG side):

1. goopg ingests COPY **row-at-a-time** through the insert operator — one
   heap insert + one WAL record (+ stripe lookup + append handoff) per row.
   PG buffers ~1000 tuples (`CopyMultiInsertInfo`) and calls
   `heap_multi_insert`, which fills whole pages and emits **one
   `XLOG_HEAP2_MULTI_INSERT` record per page batch**.
2. No `BulkInsertState`/ring-buffer analogue: PG keeps the current target
   page pinned across the batch instead of re-finding a free page per tuple.
3. pgbench 18 runs `COPY … WITH (FREEZE)` where possible; PG then writes
   pre-frozen tuples, which also makes the subsequent vacuum phase read
   almost nothing (goopg vacuum 2.4 s vs 0.53 s) — the freeze/VM interplay
   compounds the gap beyond the COPY phase itself.
4. The periodic 4–8 s stalls every ~400 k rows in the scale-100 log align
   with WAL-buffer drain/segment-rollover bursts (small pwrites above);
   PG paces these through the walwriter and page-granular WAL.

Fix design: `fix-04-copy-multi-insert.md`. Note the #1 fix (stripe
goroutine-ID) removes ~60 % of COPY CPU for free — comparable to its OLTP
share, since COPY is per-row WAL appends.

## Per-transaction cost model (order-of-magnitude)

At 1,269 TPS on ~2.9 busy cores, goopg spends ≈ 2.3 ms CPU per transaction;
PG at 15,556 TPS with (from wait-event shares) ≈ 1.6 busy cores spends
≈ 0.10 ms CPU per transaction — a ~22× per-txn CPU gap, of which the #1
stack storm alone is ~1.3 ms/txn. Removing #1 (−57 %), the profiling
artifact (headline-only), and halving commit-record work brings the model to
≈ 0.4–0.5 ms/txn ≈ 4–5× of PG — consistent with the ~8.9× uninstrumented
gap shrinking toward ≈3–4× before deeper work (arena, snapshot reuse,
protocol) is needed.
