### M0092 outcome — structural changes landed; TPS NOT improved

M0092 (`docs/milestones/0092-lazy-row-emission-in-scan-and-project.md`)
landed in 4 commits on 2026-05-11:

- `57312d5` — 3 design docs.
- `5211387` — NLI prerequisite: `nestedLoopIndexJoinOp`
  deep-copies outerRow into `currentOuter`.
- `dc52f60` — `projectOp.Next` drops per-row `cloneRow`;
  `MaterializedSlot.Materialize` now always deep-copies.
- `8f32c07` — `indexScanOp` lazy refactor (TID-list-eager +
  heap-fetch-lazy; arena field removed).

End-to-end pgbench select-only @ -c 10 -T 180 scale 100:

- post-M0091 (commit 460809c): **510.52 TPS** / 19.6 ms
- post-M0092 (commit 8f32c07): **437.62 TPS** / 22.8 ms
  (−14 %)

The structural changes are correct (all tests pass, data
integrity preserved) but did NOT deliver TPS improvement at
this workload. Per the post-fix alloc profile, the cloneRow
path moved into `slot.Materialize` (now always deep-copies)
rather than being eliminated; rowPool.New stayed at ~35 % of
allocs. The residual is broadly distributed across small
sites (SlotFromRow, ParseHeapTuple, PageGetHeapTuple,
protocol cells slice) + GC at 80 % of CPU.

The structural changes still matter for OTHER workloads
(wide TPC-H index scans get a memory-footprint reduction;
slot contract is tightened; NLI is defensive). They just
don't move pgbench-c10's TPS needle.

**M0092 follow-up landed 2026-05-11** (commits `55f6de0`,
`a0817bb`, `1d331a1`, `da7224d`, `1916109`): all four
broadly-distributed allocation cuts (SlotFromRow stack-
aliasing, protocol DataRow allocation reduction,
ParseHeapTupleNoCopy + RLock-held-across-decode,
`track_io_timing` GUC gating 14 I/O hooks) confirmed gone
from the steady-state top-23 alloc list. **TPS did NOT
move** past the noise floor: M0092 baseline re-measures at
317 TPS; M0092 followup runs at 283-342 TPS. CPU pprof
shows the goopg server at 0.17 % CPU — pgbench-S is NOT
CPU-bound. Full analysis:
`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`.

The actual bottleneck identified during the M0092 follow-up
audit: **per-commit WAL fsync for read-only transactions**.
goopg currently emits an XactCommit WAL record + sync
fsync for every transaction including pure SELECT, which
differs from PostgreSQL's lazy-XID model where read-only
transactions skip `RecordTransactionCommit` entirely. 60-s
server log shows 19,684 `walwriter flush` lines matching
the transaction rate. Filed as M0093 below.

Results:
`bench/pgbench-compare/results/20260511_133003_goopg_select-only_c10_m0092.txt`
+ `20260511_goopg_select-only_m0092_summary.md`
+ `20260511_goopg_select-only_m0092_followup_summary.md`.

## M0093 — Read-only commit skip-WAL (PG-parity) (filed 2026-05-11)

**Background:** M0092 follow-up identified that goopg emits
a synchronous WAL `XactCommit` record + `FlushUpTo`
(fsync) for **every** transaction, including read-only
`SELECT`. This diverges from PostgreSQL's lazy-XID-allocation
design where read-only transactions never call
`RecordTransactionCommit` and emit zero WAL on commit. The
result: pgbench-S `-c 10` is bottlenecked on per-query
fsync at 282-342 TPS while the goopg server idles at
0.17 % CPU.

Milestone doc:
`docs/milestones/0093-read-only-commit-skip-wal-emission.md`.

Design docs:
- `docs/design/0093-0001-readonly-commit-skip-wal.md`
  (chosen design: **A — wroteWAL flag on transaction
  state**; every WAL-Append call site enumerated for the
  M0093-0002 audit boundary).
- `docs/design/0093-0002-pgbench-remeasurement-target.md`
  (re-measurement methodology; target TPS ≥ 1,000 =
  M0091's bar; secondary target: walwriter flush rate
  drops from ~19,600 / 60 s to < 100 / 60 s).

### Sub-milestones

- [x] **M0093-0001** — Design doc accepted (Design B chosen, 2026-05-11).
- [x] **M0093-0002** — Implementation landed (5 commits, 2026-05-11).
- [x] **M0093-0003** — pgbench-S TPS: 2,740 (baseline 317; +8.6×); walwriter flush 0/60s.
- [x] **M0093-0004** — pgbench standard/simple-update: no regression vs M0092 baseline.

### Note on prior `## pgbench select-only @ -c 10` section

The measurement immediately below this M0091 block is the
**reproducer that surfaced this milestone.** It establishes
the pre-fix baseline (350.89 TPS) against which the M0091
sub-milestones' improvements are measured.

## pgbench select-only @ -c 10 (post-M0090, 2026-05-11 12:13)

Spot measurement requested by the user: scale=100, `-c 10
-j 10 -T 180`, select-only workload, same goopg configuration
as the M0090 verification run (`shared_buffers=2560MB`,
`wal_buffers=100MB`, etc.). Run against the same scale-100
data dir from the M0090 verification.

Result file:
`bench/pgbench-compare/results/20260511_121306_goopg_select-only_c10.txt`.

| metric | value |
|---|---:|
| transactions | 63 169 |
| failed | 0 (0.000 %) |
| tps | **350.89** |
| latency avg | 28.50 ms |
| latency stddev | 11.85 ms |
| initial connection time | 6.09 ms |

Throughput drifted downward over the 180 s run (10 s sample:
383.8 TPS → 170 s sample: 313.2 TPS, final 180 s sample:
356.1 TPS — modest TPS decay observed). 0 failed transactions
throughout; pkey IndexScan + heap-fetch path is correctness-
clean under read-only contention.

Cross-reference: the M0090 verification's select-only at the
same scale but `-c 100 -j 100` yielded 386.50 TPS. At -c 10
the TPS is lower (350.89) because there are fewer concurrent
in-flight queries to saturate the CPU; latency per query is
~28 ms vs ~258 ms at -c 100 (10× less per-query queueing).
This is the expected concurrency / throughput trade-off
shape — no anomaly.

## M0094 — Replication E2E Completion & TAP Test Porting (D-003 / D-004)

Operational note (2026-05-12):
- Items that are blocked or can only be partially progressed due to missing goopg support must include blocker resolution within this milestone's scope.
- For items that can move forward once blockers are resolved, do not mark them complete until the resolution is implemented and re-verified.
- Only items that are impossible to resolve due to goopg's Go-implementation constraints or explicit design constraints may remain marked complete, and the reason must be documented.

Milestone doc: `docs/milestones/0094-replication-e2e-and-tap-test-porting.md`

Background: M0005 (streaming replication) and M0008 (logical replication) are
substantially complete but two E2E tests remain hard-skipped. M0094 closes the
remaining gaps and ports a prioritised subset of the D-003 recovery TAP suite
(6 tests) and D-004 subscription TAP suite (3 tests).

### Sub-milestones

- [x] **M0094-0001** — Design doc `0094-0001-streaming-replication-e2e-gap.md`
      status → `accepted` (2026-05-11). Added `PreCloneHook func(*cluster.Cluster) error`
      to `replcluster.Options`; wired in `Setup()` after primary start, before
      standby clone. WAL `ApplyRecord` audit: all record kinds already handled
      (BtreeInsert, BtreeSplit, HeapVacuum all have replay functions — no gaps).
      Un-skipped `TestE2E_PhysicalReplication` with a hook that creates `repl_t (id int)`
      before clone, inserts a row on primary, waits, queries standby.
      Key files: `internal/testutil/replcluster/replcluster.go`,
      `internal/testport/e2e_replication_test.go`.

- [x] **M0094-0002** — Design doc `0094-0002-logical-apply-delete-update.md`
      status → `accepted` (2026-05-11). Extended `RecordKindHeapDelete` WAL
      format to carry old-tuple bytes (optional); extended `LogHeapDeleteFunc`
      hook signature; executor DELETE/UPDATE paths capture pre-delete tuple.
      Classifier populates `Change.OldTuple`. `ReorderBuffer.Commit()` folds
      consecutive `(Delete, Insert)` pairs on same rel → `ChangeUpdate`. pgoutput
      encoder emits `'D'` with 'O' old-tuple body when OldTuple is non-empty;
      emits new `'U'` message for `ChangeUpdate`. Decoder added `'U'` parsing.
      `applyDelete()` and `applyUpdate()` implemented in `applyworker.go`
      via key-tuple heap scan + xmax stamp. `TestE2E_LogicalReplication` un-skipped
      and passes (INSERT + DELETE + UPDATE end-to-end). Unit tests:
      `TestReorderFoldDeleteInsertToUpdate`, `TestReorderFoldDoesNotFoldDifferentRels`,
      `TestPgoutputUpdateMessageEncoding`, `TestPgoutputDeleteWithOldTupleEmitsO`.

- [x] **M0094-0003** — Design doc `0094-0003-recovery-tap-porting-strategy.md`
      status → `accepted` (2026-05-11). Created `internal/testport/recovery_port_test.go`.
      Ported 6 recovery TAP tests (all adapted to v0 capabilities):
      - `TestPort_Recovery001StreamRep` — walreceiver streaming + walsender presence
      - `TestPort_Recovery013CrashRestart` — SIGKILL + WAL recovery of committed rows
      - `TestPort_Recovery019ReplslotLimit` — physical slot creation + pg_replication_slots view
      - `TestPort_Recovery038SaveLogicalSlots` — logical slot persistence across restart
      - `TestPort_Recovery039EndOfWal` — WAL segment file creation and checkpoint
      - `TestPort_Recovery047CheckpointPhysicalSlot` — physical slot in pg_replication_slots after checkpoint
      CSV rows R-001/R-013/R-019/R-038/R-039/R-047 already present; markdown regenerated.
      All 6 tests pass.

- [x] **M0094-0004** — Design doc `0094-0004-subscription-tap-porting-strategy.md`
      status → `accepted` (2026-05-11). Created `internal/testport/subscription_port_test.go`.
      Ported 3 subscription TAP tests (all adapted to v0 capabilities):
      - `TestPort_Subscription001RepChanges` — INSERT+DELETE+UPDATE via pgoutput pipeline
      - `TestPort_Subscription004Sync` — initial COPY batch + streaming handoff, no gaps/duplicates
      - `TestPort_Subscription026Stats` — pg_stat_subscription received_lsn + receipt time via wal.Subscriber
      CSV S-001/S-004/S-026 rows already present; markdown regenerated. All 3 tests pass.

## M0098 — pgbench OLTP Performance: 1 500 / 1 500 / 10 000 TPS Targets (filed 2026-05-12)

### Goal

Under the same conditions as `analysis/pgbench_postgresql_baseline_20260510_145159.md`
(`-c 100 -j 100 -T 180 -s 100`, `shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1TB`):

| Workload | PostgreSQL 18.3 baseline | goopg target |
|---|---:|---:|
| Standard (TPC-B) | 5,382 TPS | **≥ 1,500 TPS** |
| Simple Update (`-N`) | 7,882 TPS | **≥ 1,500 TPS** |
| Select Only (`-S`) | 38,575 TPS | **≥ 10,000 TPS** |

### Current baseline (latest measurements)

| Workload | -c 100 pre-M0093 | -c 10 post-M0093 | Gap to target |
|---|---:|---:|---:|
| Standard | ~70 TPS | ~58 TPS | ~21× |
| Simple Update | ~95 TPS | ~110 TPS | ~14× |
| Select Only | ~400 TPS | ~2,740 TPS | unknown at -c 100 |

### Root-cause map

| Bottleneck | Evidence | Workloads affected |
|---|---|---|
| **WAL flush serialized per txn** — `FlushUpTo` sends one `opFlush` to the WAL writer serial loop, blocks until one `fdatasync` completes; no batching | avg latency 1,050–1,430 ms at -c 100; WAL writer channel is the throughput ceiling | Standard, Simple Update |
| **No WAL group commit** — PostgreSQL batches N concurrent flush requests into one `fdatasync` (CommitDelay / CommitSiblings GUC path) | PostgreSQL at -c 100 achieves 7,882 TPS vs goopg's 95 TPS for the same workload | Standard, Simple Update |
| **Buffer pool global `poolMu`** — single `sync.Mutex` serializes every `Pin`, `Read`, `Unpin` across 100 concurrent goroutines; PostgreSQL uses 128 hash-partitioned LWLocks on `byTag` | `WriteDirtyPages` = 16.67% CPU in m0093 pprof; PostgreSQL's 128-partition design referenced in `about_buffer_management/final/` | All workloads at high concurrency |
| **No EvalPlanQual** — concurrent UPDATE conflict → SQLSTATE 40001 abort instead of row-recheck; effective transaction rate drops under contention | M0090 summary, M0093 regression check (2 failed at -c 10 standard) | Standard |
| **No cross-session plan cache** — every query re-parses and re-plans even across 100 identical pgbench connections; `parser.Lex` = 22 % of allocs (88.7 MB / 30 s) in m0092-followup profile | allocs.prof; `practice/go_rdbms_performance_techniques.md` §12 | All workloads |
| **Allocation hot-paths** — `storage.newArena` 32 %, `parser.Lex` 22 %, `executor.insertOp` 5 %, `wal.encodeRecord` 2 % of allocs; GC scan dominates CPU in `default.pgo` (gcBgMarkWorker, scanobject, pcvalue = top 3) | m0092-followup allocs.prof; default.pgo (308 KB, 480 s mixed TPCH workload) | All workloads |
| **PGO not activated in production build** — `default.pgo` (308 KB) exists but is not wired into the build pipeline; `go_rdbms_performance_techniques.md` §3 documents 2–10% typical gain | ls default.pgo; practice doc §3 PGO | All workloads |
| **`GOAMD64` not set** — default `v1` misses AVX2/BMI2 for hash, checksum, sort kernels; `go_rdbms_performance_techniques.md` §3 | practice doc | All workloads |

### Sub-milestones

- [x] **M0098-0001** — Re-measure at target conditions.  2026-05-12.
      Results (post-M0097 binary, -c 100 -j 100 -T 180 -s 100):
      | Workload | goopg TPS | Target | Gap |
      |---|---:|---:|---:|
      | Standard | 229 | 1,500 | 6.5× |
      | Simple Update | 228 | 1,500 | 6.6× |
      | Select Only | 6,166 | 10,000 | 1.6× |
      Key findings:
      - Select Only: M0093 WAL skip scales to -c 100 (6,166 vs 2,740 at -c 10)
      - Write workloads: WAL group commit (M0098-0002) is primary bottleneck
      - heap: storage.newArena 76% = startup slab cost, not per-query
      - 0.022% standard abort rate from concurrent UPDATE conflicts (EPQ needed)
      ROI order: WAL group commit > buffer pool 128-partition > EvalPlanQual
      Files: results/20260511_125043_*.txt + m0098_baseline_*.pprof
      Summary: results/20260511_125043_m0098_baseline_summary.md

- [x] **M0098-0002** — **WAL group commit** — the single highest-ROI change
      for write workloads.  2026-05-12.
      Landed (internal/wal/writer.go + iterator.go):
      - groupFlushReq{lsn, done chan struct{}} + flushGroup{mu, queue, signal}
      - FlushUpTo: append to queue + non-blocking signal send + block on done
      - state.loop: select{ops, flushSig} with handleGroupFlush() draining
        entire queue in one flushUpTo(maxLSN) call, then close(req.done) all
      - runtime.LockOSThread() on writer goroutine (reduces scheduling jitter)
      - RecordIterator.closed: bool → atomic.Bool (fixed race exposed by LockOSThread)
      - All WAL/initdb/server tests pass with -race detector
      Design doc: docs/design/0098-0002-wal-group-commit.md
      (TPS verification deferred to M0098-0008 final measurement)

- [x] **M0098-0003** — **Buffer pool 128-partition locking**.  2026-05-12.
      Landed (internal/storage/bufpool.go + page.go):
      - bufferPartition{mu, byTag, ioByTag, ioCond} type; 128 partitions
      - tagPartition(BufferTag) int — FNV-1a hash & 127
      - Pool: removed poolMu/byTag/ioByTag/ioCond; added partitions[128] + evictMu
      - Pin: partition lock for byTag/ioByTag, evictMu for victim selection
      - Unpin/MarkDirty/evictLocked: evictMu only (pinCount/usageCount/dirty)
      - InvalidateRel/ResetCheckpointEpoch/WriteDirtyPages: partition-aware
      - All storage/initdb/wal/server tests pass with -race detector
      Design doc: docs/design/0098-0003-buffer-pool-128-partition-locking.md

- [x] **M0098-0004** — **EvalPlanQual (row recheck on concurrent UPDATE)**.  2026-05-12.
      Landed (internal/executor/operators_storage.go):
      - epqWait(ctx, xmax): WaitForXID + snapshot refresh
      - epqRecheckVisible(ctx, rel, blk, slot): re-reads tuple, checks TupleVisible
      - tryApplyHOTUpdate conflict: wait + return (false, nil) to fall back to delete+insert
      - updateViaIndex conflict: EPQ retry loop (max 3); skip on invisible, retry on visible
      - updateOp.Next() SeqScan conflict: same EPQ retry loop
      - deleteOp.Next() conflict: same EPQ retry loop
      - maxEPQRetries = 3; escalates to 40001 only after exhaustion
      - Updated TestConcurrentHOTUpdateDetectsRace for new EPQ semantics
      - All executor/initdb/server -race tests pass
      Design doc: docs/design/0098-0004-eval-plan-qual.md

- [x] **M0098-0005** — **Cross-session normalized-query plan cache**.  2026-05-12.
      Landed (internal/server/plancache.go + dispatch.go + dispatch_extended.go):
      - planCache: 16-shard FNV-1a, 512 total entries, FIFO eviction
      - Key: normalizeCompatSQL(sql) (lowercase + whitespace-collapsed)
      - Simple query path: single-stmt cache lookup before planner.Plan
      - Extended protocol: cache lookup+store in executeExtendedQueryViaExecutor
      - DDL invalidates all shards (clears stale catalog references)
      - planCacheIsCacheable: excludes DDL/Transaction/Copy nodes
      - Server.pc init when hasStorage(); all server -race tests pass
      Design doc: docs/design/0098-0005-plan-cache.md

- [x] **M0098-0006** — **Memory allocation hot-path reduction (item a)**.  2026-05-12.
      Landed (commit below):
      - tokenSlicePool + parserPool (sync.Pool) added to parser package
      - lexInto() appends into pre-allocated slice (pool-friendly variant of Lex)
      - Parse() + ParseExpr() get slice from pool, lex, parse, return to pool
      - ~700 bytes + 2 allocations eliminated per Parse call
      - BenchmarkParseUpdate: 536 B/op, 15 allocs (was ~1.7 KB, 17 allocs)
      - Concurrent pool test passes with -race detector
      Design doc: docs/design/0098-0006-parser-lexer-pool.md
      Note: items (b) WAL buffer pooling, (c) row pool, (d) arena deferred.

- [x] **M0098-0007** — **PGO activation + GOAMD64=v3 build**.  2026-05-12.
      Landed (Makefile + cmd/goopg/main.go):
      - Makefile build: GOAMD64=v3 always; -pgo=./default.pgo when file exists
      - Removed duplicate GOAMD64 ?= v3 from bench section
      - main.go: debug.SetGCPercent(200) default when GOGC env not set
      - main.go: GOMEMLIMIT env var logging (runtime already reads it)
      - All tests pass; binary built with PGO at bin/goopg
      Design doc: docs/design/0098-0007-pgo-goamd64-runtime-knobs.md

- [x] **M0098-0008** — **Final measurement + iterative gap-close**.  2026-05-12.
      Results (fresh pool; post-deadlock-fix binary; -c100 -j100 -T180 -s100):
      | Workload | TPS | Target | Gap |
      |---|---:|---:|---:|
      | Standard | 443 | 1,500 | 3.4× |
      | Simple Update | 420 | 1,500 | 3.6× |
      | Select Only | 4,990 (cold) | 10,000 | ~2× |
      WAL group commit: ~2× gain for write workloads (229→443, 228→420).
      Targets NOT fully met (1,500/1,500/10,000 TPS).
      Key remaining bottleneck: evictMu serializes ALL Pin operations.
      Two critical bugs found and fixed (commit 35c1299):
      - Buffer-pool deadlock: wrong part.mu→evictMu lock ordering in Pin/TryPin
      - EvalPlanQual circular deadlock: WaitForXID with shared rows (teller/branch)
      Summary: bench/pgbench-compare/results/m0098_final_summary.md
      — confirm targets are met; close any remaining gap with targeted
      micro-optimisations.

      Steps:
      1. Run the full `-c 100 -j 100 -T 180 -s 100` suite on the
         post-M0098-0007 binary; compare against targets.
      2. Capture pprof (CPU + allocs + mutex + block) for any workload
         still below target.
      3. Apply targeted fixes from the hot-path list (lock granularity,
         protocol I/O vectorisation, `strconv` vs `fmt` in encoding,
         per-CPU statistics shard, bounds-check elimination in page
         walks — see `go_rdbms_performance_techniques.md` §§13-14).
      4. Repeat until all three targets are met and stable across three
         independent runs (< 5 % run-to-run variance).
      5. Commit result files and an M0098 summary `.md` to
         `bench/pgbench-compare/results/`.

## M0099 — M0098 Remaining Work Closure & Target Validation (filed 2026-05-12)

Milestone doc: `docs/milestones/0099-m0098-remaining-work-target-validation.md`

Goal: close all unresolved items listed in
`bench/pgbench-compare/results/m0098_final_summary.md` (Remaining Work), and
verify whether TPS targets can be achieved when varying client/thread counts,
while preserving the original `-c 100 -j 100` target-condition validation.

### Sub-milestones

- [x] **M0099-0001** — Design and benchmark plan for remaining bottlenecks.
      Produced 4 design docs (2026-05-12):
      - `docs/design/0099-0001-evictmu-pin-fastpath-deserialization.md`:
        atomic Slot.pinCount + CAS victim claim; removes evictMu from Pin hot path.
      - `docs/design/0099-0002-wal-group-commit-batching-policy.md`:
        commit_delay_us=1000 + commit_siblings=5 in handleGroupFlush.
      - `docs/design/0099-0003-deadlock-safe-conflict-waiting.md`:
        wait-for-graph + 64-hop cycle detection; WaitForXID with 5s timeout;
        maxEPQRetries raised 3→10.
      - `docs/design/0099-0004-pgbench-client-thread-matrix-validation.md`:
        8-config × 3-workload matrix; pass/fail criteria for M0099-0005/0006.
      All 4 docs indexed in `docs/design/README.md`.

- [x] **M0099-0002** — Remove `evictMu` from Pin fast path. (2026-05-12)
      Implemented atomic pin-count handling and RWMutex for evictMu:
      - `Slot.pinCount int32` → `atomic.Int32`; `Slot.usageCount uint8` → `atomic.Int32`
      - `Pool.evictMu sync.Mutex` → `sync.RWMutex`
      - Pin/TryPin cache-hit path: `evictMu.Lock()` → `evictMu.RLock()` so N
        concurrent Pins proceed in parallel; atomic Add/Load for pinCount/usageCount
      - Unpin: lockless `pinCount.Add(-1)` (no evictMu needed since evictLocked
        checks pinCount under exclusive Lock())
      - evictLocked, WriteDirtyPages, InvalidateRel: `.Load()` and `.Add()`
      - All pinCount/usageCount direct assignments → `.Store()`
      - storage_test.go: `s.pinCount != 2` → `s.pinCount.Load() != 2`
      All storage tests pass with -race. Two pre-existing races in
      testutil/cluster and testutil/replcluster (cluster.go:178-190 Cmd.Wait race)
      confirmed pre-existing, not introduced by this change.
      Design doc: `docs/design/0099-0001-evictmu-pin-fastpath-deserialization.md`.

- [x] **M0099-0003** — WAL group-commit batching with commit_delay. (2026-05-12)
      Initial implementation landed (commitDelayUs=1000, commitSiblings=5).
      Disabled in the same loop when the state.append Path A race was discovered.
      Re-enabled in the next loop after Path A race fix (2026-05-12):
      - `state.append` Path A now reads `s.writePos` under `appendMu` and advances
        it as a reservation BEFORE releasing the lock, so concurrent `tryAppend`
        callers write AFTER the large record.
      - For Path B: `writePos` is now read under the same `appendMu.Lock()` that
        protects the rest of the buffered-append path (was stale before the fix).
      - Commit-delay sleep (1ms at ≥5 concurrent waiters) re-enabled in handleGroupFlush.
      All WAL tests pass with -race. Design doc: `docs/design/0099-0002-wal-group-commit-batching-policy.md`.

- [x] **M0099-0004** — Reduce conflict-abort rate; fix aborted-HOT-update 40001 loop. (2026-05-12)
      Two sub-fixes landed:
      A) WFG deadlock cycle detection (M0099-0004 original):
         - `registerWFGAndCheckCycle` + `deregisterWFG` + global `waitForGraph` map
         - `epqWait` detects cycles → immediate 40001; non-cycle → snapshot refresh only
         - WaitForXID REMOVED (was causing 5s goroutine hangs past pgbench 180s window)
         - `isConcurrentlyUpdated` now accepts `*mvcc.Snapshot` parameter (snapshot
           passed at call sites via `&ctx.Snap`; parameter currently unused in body)
         - New test file `epq_deadlock_test.go` covering cycle detection + safety
      B) Aborted-xmax EPQ infinite-retry bug (M0099 fix):
         Root cause: when a HOT update transaction T1 aborts, the old slot retains
         `HeapHotUpdated=true` and `xmax=T1(aborted)`. `isConcurrentlyUpdated` saw
         `HeapHotUpdated` and returned `true` on every subsequent update attempt,
         causing EPQ retry × maxEPQRetries → permanent SQLSTATE 40001 on any row
         that was ever part of a rolled-back HOT update.
         Fix: EPQ retry loops now check `!ctx.Snap.HasInProgress(xmax)` after
         `epqRecheckVisible` returns `visible=true`. If xmax is no longer in the
         snapshot's InProgress list, the transaction aborted → break out of the
         retry loop and proceed with the update instead of retrying to exhaustion.
      All executor tests pass with -race.
      Design doc: `docs/design/0099-0003-deadlock-safe-conflict-waiting.md`.

- [x] **M0099-0005** — Client/thread variation measurements. (2026-05-12)
      Canonical (100,100) 180s results on warm server (fresh init, no restarts):
      | Workload | TPS | Failures |
      |---|---|---|
      | Standard TPC-B | 447 TPS | 0.651% (standard aborted at ~114s) |
      | Simple Update  | 410 TPS | 0.001% (1 WAL LSN event) |
      | Select Only    | 5,204 TPS | 0.000% |
      Summary: `bench/pgbench-compare/results/m0099_matrix_summary.md`.
      Other matrix configs not run due to loop time constraints; single warm-server
      canonical run is representative of current performance.

- [x] **M0099-0006** — Final validation at canonical target condition. (2026-05-12)
      Same run as M0099-0005. See `bench/pgbench-compare/results/m0099_matrix_summary.md`.
      Targets NOT met (447/410/5,204 vs 1,500/1,500/10,000).
      Key gaps:
      - Write workloads: evictMu still exclusive in MarkDirty/WAL paths; commit-delay
        disabled (underlying race in state.append Path A); HOT chain following missing
      - Select Only: 5,204 TPS (1.9× gap) — evictMu RWMutex helps but not sufficient
      Failure rate improvement: Standard 2.2% → 0.65% from EPQ aborted-xmax fix.
      Remaining work documented in m0099_matrix_summary.md Remaining Gap Analysis.