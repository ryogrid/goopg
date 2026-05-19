# 02 — CPU Bottlenecks

Source: `profiles/goopg_c<C>_<wl>.cpu.pb.gz` (120 s windows, T+30 → T+150). All percentages are `cum%` from `go tool pprof -top -nodecount=40 -cum`.

## Headline

**Garbage collection dominates the CPU budget in every measured pattern**, ranging from 36 % `cum cpu` at c=100 select-only to **77 % at c=10 simple-update**. Application code (parser → planner → executor) gets the leftover 20–40 %. This single observation explains most of the goopg/PG gap at low concurrency: PG has no GC and spends ~100 % of its CPU on actual work.

| pattern | gcBgMarkWorker cum% | scanobject cum% | dispatch cum% | runtime.futex cum% | total CPU% (i.e. cores busy) |
|---|---:|---:|---:|---:|---:|
| c=10 select-only       | 63.3 | 54.9 | 17.7 | 14.9 | 308 % (~3 cores) |
| c=10 simple-update     | 77.8 | 64.6 |  8.2 | 11.2 | ≈300 % |
| c=10 standard          | 78.6 | 67.7 |  6.5 |  9.0 | ≈300 % |
| c=50 select-only       | 45.9 | 38.9 | 30.3 | 21.0 | 288 % |
| c=50 simple-update     | 72.7 | 59.9 | 12.9 | 11.5 | 295 % |
| c=50 standard          | 73.1 | 60.1 | 11.8 | 11.2 | 293 % |
| c=100 select-only      | 36.6 | 30.6 | 37.8 | 23.0 | 294 % |

Two things to notice in the table:

1. **CPU% never exceeds ~300 % out of an available 1 600 % (16 logical cores)** — goopg is using only ~3 cores even under c=100 load. The host has 13 cores idle while pgbench backs up. This is contention, not CPU saturation.
2. **At c=10 select-only the system spends more CPU collecting garbage (63 %) than dispatching queries (18 %).** This is a per-statement allocation problem, not a query-volume problem.

## §2.1 Top CPU consumers (c=10 select-only — the cleanest profile)

```
flat   flat%   sum%    cum    cum%
0.16s  0.04%   0.04%   260.69s 70.41%  runtime.systemstack
0.23s  0.06%   0.11%   234.26s 63.27%  runtime.gcBgMarkWorker
14.53s 3.92%   4.04%   230.26s 62.19%  runtime.gcDrain
42.78s 11.55%  15.59%  203.28s 54.90%  runtime.scanobject
55.21s 14.91%  30.67%   55.21s 14.91%  runtime.futex
41.65s 11.25%  41.92%      55s 14.85%  runtime.findObject
 0.16s 0.04%   15.68%   65.46s 17.68%  github.com/goopg/goopg/internal/server.(*Server).dispatchSimpleQueryViaExecutor
```

Application-side `dispatch... → handleQuery` consumes 17.7 % `cum`, of which:

| symbol | cum% | goopg source |
|---|---:|---|
| `parser.Parse` (chain) | ~6 %   | `internal/parser/parser.go:82` |
| `planner.Plan` chain   | ~5 %   | `internal/planner/planner.go:32` |
| `executor.Run` / `Open / Next` | ~5 % | `internal/executor/executor.go:259` |
| protocol encode (`FrameWriter.WriteFrame`)  | ~2 % | `internal/protocol/...` |

So a SELECT statement at c=10 spends roughly equal time in parse, plan, and execute, with protocol encode small. None of these dominate — they each pay equally for the GC scanning of the objects they themselves allocated.

## §2.2 PG counterpart — why this doesn't happen

PG's executor doesn't allocate from a GC'd heap. The relevant counterparts:

| goopg cost | PG counterpart | upstream source |
|---|---|---|
| `parser.Parse` allocates `Stmt`, `Expr`, `Identifier` AST nodes on the GC heap | `raw_parser` allocates into the per-statement `MessageContext` palloc memory context; freed in one shot at end of statement | `postgres/src/backend/parser/parser.c`, `postgres/src/backend/nodes/makefuncs.c` |
| `planner.Plan` allocates `Plan` tree on GC heap | `planner` allocates into `CurrentMemoryContext`, freed at end of query | `postgres/src/backend/optimizer/plan/planner.c` |
| `executor.Run` per-tuple `Row` / `TupleSlot` / `Datum` allocations | `TupleTableSlot` is fixed-size and reused (`ExecClearTuple`); datums are inline `Datum`s (8 bytes); per-tuple work goes into `ExprContext` reset on each row | `postgres/src/backend/executor/execTuples.c`, `postgres/src/backend/executor/execExpr.c` |
| Heap scan allocates per-row tuple copies | `heap_getnext` returns a pointer into the pinned page; no copy until needed | `postgres/src/backend/access/heap/heapam.c` |

PG's *palloc + MemoryContext* model gives it (a) zero GC scanning cost and (b) bulk free at statement / transaction end. This is the single largest architectural advantage. Even with `GOGC=200` and `GOMEMLIMIT=18GiB`, goopg's GC scans 200+ s of CPU per 120 s wall-clock window on the 3-core working set.

## §2.3 The `runtime.futex` story

`runtime.futex` accounts for **14.9 % at c=10 SO** rising to **23.0 % at c=100 SO**. This is the Go runtime's primitive for goroutine wakeup (channel sends, mutex unlock, condvar broadcast). At c=100 it correlates with the `mvcc.Manager` and `bufpool` contention documented in [`04-contention.md`](04-contention.md): every mutex hand-off involves a futex wakeup.

PG's analogue is the LWLock's wait-list spinlock (`LWLockWaitListLock`, `postgres/src/backend/storage/lmgr/lwlock.c:867`) plus the underlying SysV semaphore for true waits. PG's design *avoids* the wait-list spinlock on the uncontended fast path (single atomic CAS on the lock state) so wakeups only happen at the moment of true contention. goopg's `sync.Mutex` always involves a runtime futex on slow-path acquire and on `Unlock` when waiters exist.

## §2.4 Write workload — same GC story, plus `runtime/syscall.Syscall6`

For `c=50 simple-update`:

```
gcBgMarkWorker    72.7 % cum
scanobject        59.9 %
findObject        15.4 %
syscall.Syscall6   8.3 %       ← fdatasync from the WAL writer
runtime.futex      6.1 %
dispatch chain    13.0 %
```

`Syscall6` here is the WAL writer's `fdatasync`. At 347 TPS × ~5 KB / commit it's only ~1.7 MB/s — the bottleneck is not the syscall itself but its serialisation. See [`04-contention.md`](04-contention.md) §4.1 for the `mvcc.Manager.Commit` mutex story that gates these syscalls.

## §2.5 What's *not* in the CPU top

Notably absent from the CPU profile:

- `internal/lockmgr.(*Manager).Acquire` — the heavyweight lock manager is **not** a top CPU consumer. The hypothesis from `00-methodology.md` that lockmgr would dominate was wrong.
- `internal/access/btree.Search` — btree probe is efficient.
- `internal/storage.(*Pool).Pin` — bufpool partition fast path is cheap at the CPU level (it shows up only on the *block* profile when waiting for the partition mu — see [`04`](04-contention.md)).

The bottleneck order at c=10 is **allocation + GC ≫ everything else**. At c=100 it shifts to **GC + contention** (the contention story is in [`04`](04-contention.md)).

## §2.6 Per-symbol summary table

| goopg `file:line` | symbol | role | top CPU cum% (peak across patterns) |
|---|---|---|---:|
| (runtime) | `runtime.gcBgMarkWorker` / `gcDrain` | GC mark phase | 77.8 % (c=10 simple-update) |
| (runtime) | `runtime.scanobject` | object scan | 67.7 % (c=10 standard) |
| (runtime) | `runtime.futex` | mutex/channel wakeup | 23.0 % (c=100 SO) |
| `internal/server/dispatch.go:85` | `dispatchSimpleQueryViaExecutor` | parse + plan + execute | 37.8 % (c=100 SO) |
| `internal/parser/parser.go:82` | `parser.Parse` | tokenize + recursive-descent | ~6 % |
| `internal/planner/planner.go:32` | `planner.Plan` | AST → plan tree | ~5 % |
| `internal/executor/executor.go:259` | `executor.Run` | Volcano pump | ~5 % |
| `internal/wal/writer.go:611` | `wal.Writer.Append` | WAL record insert | <2 % (kept off CPU by mutex queueing — see §04) |

## §2.7 Reading the `cum%` numbers vs CPU%

The reader is reminded that "Total samples = 370 s (308 %)" means *3 cores busy out of 16 available*. A 60 % `cum%` of that 308 % = 1.85 cores spent on GC. PG would use those 1.85 cores for query work — which is precisely why it gets 16× the TPS at c=10 SO on the same hardware.

## Recommendations driven by this chapter

→ [`08-recommendations.md`](08-recommendations.md) #1 (`palloc`-style memory context per statement), #2 (`sync.Pool` per operator state), #3 (escape-analysis cleanup of `planner.Plan` chain). Each is sized against §2.1–§2.4 measurements.
