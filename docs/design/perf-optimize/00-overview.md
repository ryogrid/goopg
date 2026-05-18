# 00 — Overview: goopg Performance Refactor

> *Pointer density of the live heap, not allocation rate, drives goopg's GC mark
> cost. The refactor moves every per-statement object lifetime off the GC heap
> into a hierarchical `MemoryContext` (`mctx`) owned by the backend goroutine.
> Within `mctx`-allocated regions, replace pointer references with
> `(arenaID, offset, length)` triples or dense-array indices. Replace
> interface dispatch in hot paths with concrete sum-types. Replace
> shared-state mutexes with PG-style per-backend slot arrays and lock-free
> reads. On-disk formats stay PG-compatible.*

This chapter is the entry point for the `docs/design/perf-optimize/` series.
It compresses the empirical findings from `analysis/perf-optimize/`
(commit `ab1b955`), states the constraints, lists the modules being retired
or rewritten, and indexes the nine subsequent design chapters.

## 1. Why this refactor

The pgbench analysis report ran goopg side-by-side with PostgreSQL 18.3 across
three workloads (TPC-B-like / simple-update / select-only) and three client
counts (10 / 50 / 100). Three structural findings:

1. **GC dominates CPU at 54–77 %** in every measured pattern.
   `runtime.gcBgMarkWorker` + `runtime.scanobject` consume 63 % cum CPU at
   c=10 select-only and rise to 77 % at c=10 simple-update. Application
   code (parse + plan + execute + protocol) shares the remaining
   ~20–40 %. See `analysis/perf-optimize/02-cpu-bottlenecks.md` §2.1.

   The driver of mark cost is not allocation rate alone (~19 KB per
   SELECT × 2 307 q/s = 44 MB/s of churn) but **pointer density of the
   live heap**. `practice/go_gc_optimized_programming.md` §12 states the
   underlying principle:

   > Reducing pointer-rich live object graphs is often more important than
   > reducing allocation rate.

   Two concrete contributors: each `Datum` (`internal/executor/datum.go:101-116`)
   carries three GC-traced fields (`Buf []byte` slice header, `Big *big.Int`,
   `arena *Arena`) and is replicated ~50× per row; the 128 buffer-pool
   partitions (`internal/storage/bufpool.go:180`) each hold a
   `map[BufferTag]int` whose internal bucket pointers are scanned every
   mark cycle.

2. **Two single-mutex bottlenecks** gate the write and read paths.
   `internal/mvcc/manager.go:73`'s `Manager.mu` accounts for **92 % of
   write-side mutex delay** because it gates `Begin`, `SnapshotFor`,
   `Commit`, `OldestXmin`, and `finish` under one lock. `internal/activity/
   activity.go:123`'s `Registry.mu` accounts for **95 % of c=100 select-
   only mutex delay** because every protocol frame takes it for the
   `WaitEventStart` / `WaitEventEnd` pair. Evidence:
   `analysis/perf-optimize/04-contention.md` §4.2–§4.3.

3. **A hot-page livelock at c=100 simple-update**. `pgbench_history`
   inserts deterministically target the relation's tail page; one of the
   128 `bufferPartition.mu` covers that page; 19 goroutines pile up on
   it; combined with the GC stop-the-world windows the system stalls
   for ≥23 minutes. Evidence: `analysis/perf-optimize/04-contention.md`
   §4.4 plus the captured deadlock snapshots under
   `runs/20260518_115032/profiles/goopg_c100_simple-update.deadlock_*`.

The recommendations in `analysis/perf-optimize/08-recommendations.md`
size the lift potential per fix (3–6× on individual workloads). The
present refactor goes further than those incremental recommendations:
it replaces structurally weak components rather than tuning them, on
the user's explicit directive to be aggressive.

## 2. Constraints

These are invariants. No design proposed here may violate them.

1. **PG on-disk format compatibility is non-negotiable.** Heap pages,
   tuples, WAL records, control files, CLOG bank format, FSM bytes,
   btree pages — all stay PG-compatible. Every change is in-memory or
   internal-Go-API only. Any change that requires `pg_upgrade` or
   prevents binary log-shipping to a vanilla PG replica is out of
   scope.
2. **No dependency on Go's experimental `arena` standard-library
   package** (`golang.org/x/exp/arena` / `arena` proposal). It is
   unlikely to be officially adopted; we build our own allocator
   (`internal/mctx`, see [[01-memory-context]]). Note: goopg's existing
   `internal/executor/Arena` is *not* the Go stdlib arena; it is custom
   code (M0072-0004) that the refactor subsumes.
3. **Minimize interface-based polymorphism in hot paths.** The per-row
   pipeline uses concrete sum-types (tagged unions). Interfaces are
   permitted only at session boundaries (network I/O, the storage
   manager driver, the catalog).
4. **Internal Go API breakage is permitted.** `internal/` has no
   external consumers; tests and callers may be rewritten as needed.
5. **`//go:linkname` into Go runtime internals is permitted** for
   the small set of patterns the practice doc identifies as
   high-value (per-P sharding via `runtime_procPin`, monotonic
   timestamps via `runtime.nanotime`). Every site is gated by a
   per-Go-minor-version build tag and ships with a public-API
   fallback. See [[08-runtime-internals]] for the contract.

The five points above bound the design space; every chapter validates
its proposal against them.

## 3. Replacement scope (modules retired or rewritten)

The refactor is aggressive: where a structurally better approach
exists, the existing implementation is retired rather than layered
over. The table below enumerates the changes.

| Module / Symbol                                      | Source citation                                    | Disposition                                                   | Chapter                  |
|------------------------------------------------------|----------------------------------------------------|---------------------------------------------------------------|--------------------------|
| `internal/executor.Arena` + `arena_registry`         | `internal/executor/arena.go`, `arena_registry.go`  | **DELETED**; absorbed into new `internal/mctx`                | [[01-memory-context]]    |
| `Datum.Buf`, `Datum.Big`, `Datum.arena` fields       | `internal/executor/datum.go:101-116`               | **DELETED**; Datum becomes pointer-free                       | [[02-datum-pointer-free]]|
| `executor.Operator` interface (per-row dispatch)     | `internal/executor/operator.go:24-29`              | **DELETED** from hot paths; concrete `OpNode` sum-type        | [[03-executor-concrete]] |
| `executor.TupleSlot` interface                       | `internal/executor/slot.go:18-37`                  | **DELETED**; merged into concrete `Slot` struct               | [[03-executor-concrete]] |
| `executor.cloneRowOwned` per-`Materialize` deep copy | `internal/executor/datum.go:310+`                  | **DELETED**; slot is a view into mctx; copy only at boundary  | [[03-executor-concrete]] |
| `parser.tokenSlicePool`, `parserPool`                | `internal/parser/parser.go:13,22`                  | **DELETED**; AST nodes allocated from mctx                    | [[03-executor-concrete]] |
| `mvcc.Manager.mu` + `active map[TxnHandle]*txState`  | `internal/mvcc/manager.go:71-104`                  | **DELETED**; replaced by ProcArray slot array                 | [[04-mvcc-procarray]]    |
| `mvcc.CLog.mu sync.RWMutex` (single lock)            | `internal/mvcc/clog.go:27-31`                      | **REPLACED** by per-bank RWMutex (SLRU bank locks)            | [[04-mvcc-procarray]]    |
| `activity.Registry.mu` + `backends map`              | `internal/activity/activity.go:121-247`            | **DELETED**; replaced by per-backend slot array               | [[05-activity-perbackend]]|
| `activity.WaitEventType/WaitEvent` string fields     | `internal/activity/activity.go:98-118`             | **REPLACED** by `atomic.Uint32` packed `(type, event)`        | [[05-activity-perbackend]]|
| `bufferPartition.byTag map[BufferTag]int` × 128      | `internal/storage/bufpool.go:78-83,180`            | **DELETED**; replaced by single lock-free hash table          | [[06-bufpool-lockfree]]  |
| `bufferPartition.mu sync.Mutex` × 128 (M0098-0003)   | `internal/storage/bufpool.go:79`                   | **DELETED**; pin path becomes lock-free CAS                   | [[06-bufpool-lockfree]]  |
| Slot's `pinCount` / `usageCount` separate atomics (M0099-0002) | `internal/storage/bufpool.go:32-40`     | **REPLACED** by packed 64-bit `slotState` CAS                 | [[06-bufpool-lockfree]]  |
| `bufferPartition.ioByTag map` + `ioCond *sync.Cond`  | `internal/storage/bufpool.go:78-83`                | **DELETED**; per-slot atomic `inflight` flag                  | [[06-bufpool-lockfree]]  |
| `wal.Writer.appendMu sync.Mutex` (single insert lock)| `internal/wal/writer.go:355`                       | **REPLACED** by 8-stripe `appendLocks [8]paddedMutex`         | [[07-wal-fsm-insert]]    |
| `heapExtendLocks.LoadOrStore` (per-rel single lock)  | `internal/executor/operators_storage.go:2820+`     | **REPLACED** by 8-stripe per-relation extend locks            | [[07-wal-fsm-insert]]    |
| Tail-page-preferred insert (FSM under-utilised)      | `internal/executor/operators_storage.go:2778-2831` | **REWRITTEN**; FSM-first with pin-count avoidance + batch extend | [[07-wal-fsm-insert]]    |
| Global stats counters (`internal/activity`)          | (various)                                          | **REPLACED** by `[GOMAXPROCS]paddedCounter` per-P sharding    | [[08-runtime-internals]] |

The retirement of M0098-0003 (128-partition mutexes), M0099-0002
(atomic pinCount/usageCount), and the existing executor Arena is
explicit: these milestones do address contention or churn, but the
post-refactor architecture obsoletes them. Their design docs
(`docs/design/0098-0003-*`, `docs/design/0099-0002-*`,
`docs/design/0068-0003-batch-string-arena.md`,
`docs/design/0073-0001-datum-arena-field.md`,
`docs/design/0074-0003-arena-registry-forward-compat.md`) are marked
**SUPERSEDED** in their respective frontmatter as part of the rollout
(see [[09-migration-and-rollout]]).

## 4. Chapter index

The nine subsequent chapters are organised so that earlier ones lay
substrates that later ones build on. Cross-references use double-
square-bracket anchors (e.g., `[[01-memory-context]]`).

| #  | File                                            | Purpose                                                            | Sized against                            |
|----|--------------------------------------------------|--------------------------------------------------------------------|------------------------------------------|
| 01 | `01-memory-context.md`                          | `mctx` package — hierarchical palloc-style allocator               | `02-cpu-bottlenecks.md` §2.1 (60 % GC)   |
| 02 | `02-datum-pointer-free.md`                      | Datum becomes 24 B, zero GC-traced fields                          | `03-memory-and-allocs.md` §3.4           |
| 03 | `03-executor-concrete.md`                       | Concrete-type Volcano; Slot/Plan/AST move to mctx                  | `02-cpu-bottlenecks.md` §2.1 (dispatch)  |
| 04 | `04-mvcc-procarray.md`                          | ProcArray + atomic XidGen + CLOG bank locks                        | `04-contention.md` §4.2 (92 % delay)     |
| 05 | `05-activity-perbackend.md`                     | Per-backend `wait_event_info` `atomic.Uint32`                      | `04-contention.md` §4.3 (95 % delay)     |
| 06 | `06-bufpool-lockfree.md`                        | Lock-free open-addressing buf-mapping + packed pin-state CAS       | `04-contention.md` §4.4 + GC scan        |
| 07 | `07-wal-fsm-insert.md`                          | 8-stripe WAL insert + FSM-distributed page selection               | `04-contention.md` §4.4 (livelock)       |
| 08 | `08-runtime-internals.md`                       | `//go:linkname` patterns (procPin, nanotime)                       | `02-cpu-bottlenecks.md` §2.3 (futex)     |
| 09 | `09-migration-and-rollout.md`                   | Phased rollout A–D, smoke tests, acceptance bands                  | All; the integration plan                |

## 5. Acceptance criteria for the design pass

The design pass — distinct from the implementation pass — is complete
when **all** the following hold:

1. Every chapter cites at least one `goopg file:line` reference and one
   PG `postgres/src/...` reference. Verifies grounding in real code.
2. Every chapter contains concrete Go signatures (not pseudocode) for
   the types and methods it proposes. Reviewers must be able to grep
   for the proposed symbol names.
3. Every chapter ends with a verification subsection sized against the
   `analysis/perf-optimize/` chapter it targets. Example: "expect
   `gcBgMarkWorker cum%` to drop from 63 % to < 15 % at c=10 SO."
4. `grep -RIn 'TODO\|FIXME\|TBD' docs/design/perf-optimize/*.md`
   returns zero. Unresolved questions are either decided in the doc
   or escalated to the user before the doc is considered final.
5. A reviewer subagent has been run at least once; its findings are
   addressed in a revision pass (see [[09-migration-and-rollout]]
   §reviewer for the contract).
6. The plan respects `.ralph/`-isolation: no design-doc files live
   under `.ralph/`; no Ralph state is modified during the writing
   pass; the Ralph autonomous loop continues to operate.

## 6. What this delivers against the user's directives

- **"GCに極力頼らず"** ("don't lean on GC")—every per-statement
  allocation moves to `mctx`; every `Datum` payload lives in mctx-
  resident bytes; every hot data structure (bufpool index, ProcArray,
  ActivityRegistry) becomes pointer-free. The GC scans the static
  `shared_buffers` plus the small bufpool index arrays, nothing else.
- **"アプリケーションコードでメモリ管理"** ("manage memory in
  application code")—`mctx` is application-level memory management
  with explicit `Acquire`/`Reset`/`Release`/`Free` semantics, modeled
  on PG's `MemoryContext` (`postgres/src/backend/utils/mmgr/mcxt.c`).
- **"arenaモジュールは用いない"** ("don't use the arena module")—Go's
  stdlib `arena` is never imported. The existing
  `internal/executor/arena.go` is *not* the stdlib arena (it predates
  the proposal); it is deleted as part of Phase A and replaced by
  `internal/mctx`.
- **"interfaceの利用は最低限"** ("minimise interface usage")—`Operator`
  and `TupleSlot` interfaces are removed from the per-row hot path
  and replaced by concrete sum-type pumps. The remaining interfaces
  are at coarse-grained session boundaries.
- **"PG互換のフォーマット維持"** ("preserve PG format compatibility")
  —every on-disk format stays PG-compatible. The refactor is purely
  in-memory and internal-Go-API.

## 7. Out of scope

- On-disk format changes. WAL records, heap pages, tuples, control
  file, CLOG bank file, FSM bytes, btree pages — all preserved as-is.
- Plan caching for prepared statements (`-M prepared`). pgbench
  `simple` mode does not exercise it; it is a follow-on milestone.
- TPC-H / OLAP-specific vectorized execution. Mentioned only where
  it overlaps with the OLTP fixes.
- Profiling PostgreSQL. PG is the TPS reference, not a profiling
  target.
- Implementation. This series is design-only; the implementation
  milestones (M0107+) are gated separately, one phase at a time per
  [[09-migration-and-rollout]].

## 8. Verification target (overall)

After all phases ship, re-running
`analysis/perf-optimize/scripts/run_perf_suite.sh` must show:

| Metric                                              | Pre-refactor (`ab1b955`) | Post-refactor target            |
|-----------------------------------------------------|--------------------------|---------------------------------|
| `gcBgMarkWorker` cum% (c=10 SO)                     | 63.3 %                   | **< 15 %**                      |
| `runtime.scanobject` cum% (c=10 SO)                 | 54.9 %                   | **< 12 %**                      |
| `runtime.futex` cum% (c=100 SO)                     | 23.0 %                   | **< 10 %**                      |
| c=10 select-only TPS                                | 2 307                    | **≥ 8 000**                     |
| c=50 simple-update TPS                              | 347                      | **≥ 2 000**                     |
| c=100 select-only TPS                               | 6 400                    | **≥ 12 000**                    |
| c=100 simple-update                                 | DEADLOCK / SKIPPED       | **measured TPS ≥ 500**          |
| c=100 standard                                      | DEADLOCK / SKIPPED       | **measured TPS ≥ 500**          |
| `mvcc.Manager.*` in mutex top-20                    | 92 % of write-side       | **absent**                      |
| `activity.Registry.*` in mutex top-20               | 95 % of c=100 SO         | **absent**                      |
| `bufferPartition.mu` in mutex top-20                | dominates writes         | **absent**                      |
| Datum pointer count                                 | 3 per Datum              | **0 per Datum**                 |
| `unsafe.Sizeof(Datum{})`                            | 64 B                     | **24 B (or 32 B, lint-final)**  |
| pgbench-history c=100 livelock                      | 19 goroutines stuck      | **does not reproduce**          |

The per-chapter verification subsections refine these into chapter-
specific gates ([[01-memory-context]] sizes the GC drop;
[[04-mvcc-procarray]] sizes the c=50 SU lift; etc.). Phase D of
[[09-migration-and-rollout]] is the final gate that consolidates all
metrics against pre-refactor measurements.
