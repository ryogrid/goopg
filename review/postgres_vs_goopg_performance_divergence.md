# Performance Divergence Review: PostgreSQL (`postgres/src`) vs goopg

This report reviews performance-relevant implementation gaps between upstream PostgreSQL code under `postgres/src` and goopg in this repository, focused on:

1. Executor (Operator)
2. Checkpointer
3. WAL generation to persistence flow
4. Snapshot Isolation (SI)
5. Shared buffer management and access
6. Logging / recovery
7. Materialization
8. Handling of scanned data
9. Serialization (tuple/WAL/etc.)

---

## 1) Executor (Operator)

**PostgreSQL reference**
- `postgres/src/backend/executor/execMain.c` (`ExecutorStart`, `ExecutorRun`, ...)
- `postgres/src/backend/executor/execScan.c` (`ExecScan`)
- `postgres/src/backend/executor/execTuples.c` (`TupleTableSlot` variants, deform/materialize paths)

**goopg reference**
- `internal/executor/executor.go` (plan -> operator build)
- `internal/executor/operator.go` (`Open/Next/Close`)
- `internal/executor/operators.go` (`sortOp`, `filterOp`, `projectOp`)
- `internal/executor/operators_join_agg.go`

**Divergence and performance impact**
- PostgreSQL uses `TupleTableSlot` polymorphism and mature slot lifecycle; goopg uses a simpler `Row` (`[]Datum`) pipeline.
- In goopg, sort fully materializes all rows in memory before output (`internal/executor/operators.go`, `sortOp.Open`, lines around 239-300), creating high memory pressure for large sorts.
- goopg operator model is simpler (lower abstraction overhead), but missing mature memory-governed behavior present in PostgreSQL executor stack.

**Severity**: **High**

---

## 2) Checkpointer

**PostgreSQL reference**
- `postgres/src/backend/postmaster/checkpointer.c`
  - `CheckpointerMain`
  - `CheckpointWriteDelay`
  - `AbsorbSyncRequests`

**goopg reference**
- `internal/wal/checkpointer.go`
  - `Run` (ticker-based loop)
  - `CheckpointNow` (synchronous immediate checkpoint)

**Divergence and performance impact**
- PostgreSQL runs a dedicated checkpointer process with explicit sync-request absorption and paced writeback.
- goopg uses an in-process goroutine loop (`time.NewTicker`) and volume polling; this is simpler, but `CheckpointNow()` is synchronous and can directly surface latency to request path callers.
- PostgreSQL’s request absorption and mature pacing logic are more optimized for sustained high write rates and backend decoupling.

**Severity**: **Medium**

---

## 3) WAL generation to persistence flow

**PostgreSQL reference**
- `postgres/src/backend/access/transam/xloginsert.c` (`XLogBeginInsert`, `XLogInsert`)
- `postgres/src/backend/access/transam/xlog.c` (insert/write pipeline)

**goopg reference**
- `internal/wal/writer.go` (`Config`, state loop, WAL buffer/page header behavior)
- `internal/wal/format.go` (`encodeRecord`, `encodeRecordXLog`, page-header handling)
- `internal/wal/xlog_emit.go` (`emitWithPageHeaders`)

**Divergence and performance impact**
- PostgreSQL has the long-optimized XLog insertion/writer architecture.
- goopg supports both legacy and PG-compatible framing modes. In `internal/wal/writer.go`, `WALBuffers` can be `0` (direct write path), and `PageHeaders` default behavior can remain legacy-compat oriented.
- Dual-format support increases branching/complexity and can dilute hot-path optimization focus; PostgreSQL is single-format in production behavior.

**Severity**: **Medium-High**

---

## 4) Snapshot Isolation (SI)

**PostgreSQL reference**
- `postgres/src/backend/access/transam/transaction.c`
- `postgres/src/backend/utils/time/snapmgr.c`
- `postgres/src/backend/storage/ipc/procarray.c`

**goopg reference**
- `internal/mvcc/snapshot.go`
  - `ParseIsolationLevel` maps `serializable` to repeatable-read behavior
  - `HasInProgress` performs linear scan of `InProgress`
- `internal/mvcc/manager.go`
- `internal/mvcc/visibility.go`

**Divergence and performance impact**
- goopg currently maps `SERIALIZABLE` semantics to repeatable-read snapshot behavior (`internal/mvcc/snapshot.go`, lines around 30-40), so SSI-level conflict machinery is not present.
- `Snapshot.HasInProgress` is linear (`for _, in := range s.InProgress`), and this executes on visibility-critical paths; under high concurrency, this can become CPU-expensive.
- PostgreSQL’s snapshot/visibility ecosystem is substantially more optimized and battle-tested for high client counts.

**Severity**: **High**

---

## 5) Shared buffer management and access

**PostgreSQL reference**
- `postgres/src/backend/storage/buffer/bufmgr.c`
- `postgres/src/backend/storage/buffer/freelist.c`
- `postgres/src/backend/storage/buffer/buf_table.c`

**goopg reference**
- `internal/storage/bufpool.go`
  - single pool mutex (`poolMu`)
  - clock-sweep style `usageCount`
  - in-flight read dedup via `ioByTag`
- `internal/storage/bgwriter.go`
- `internal/storage/scan_ring.go`

**Divergence and performance impact**
- goopg’s explicit `ioByTag` in-flight dedup is good for duplicate-read avoidance.
- Main pool metadata is guarded by one `poolMu`; this is simpler, but increases contention risk as core count and client concurrency rise.
- PostgreSQL’s buffer subsystem has finer-grained concurrency and more mature contention behavior.

**Severity**: **Medium**

---

## 6) Logging / recovery

**PostgreSQL reference**
- `postgres/src/backend/access/transam/xlogrecovery.c`
  - `PerformWalRecovery`
  - `xlogrecovery_redo`

**goopg reference**
- `internal/wal/recovery.go`
  - `RecordKind*` dispatch
  - switch-based apply path (`case RecordKind...`)
- `internal/wal/iterator.go`, `internal/wal/reader.go`

**Divergence and performance impact**
- PostgreSQL recovery path is deeply optimized and integrated with full RMGR ecosystem.
- goopg recovery dispatch is straightforward and maintainable, but still primarily single-thread style replay and less feature-rich recovery orchestration.
- For very large WAL histories, PostgreSQL’s mature recovery architecture has a clear scalability advantage.

**Severity**: **Medium**

---

## 7) Materialization

**PostgreSQL reference**
- `postgres/src/backend/executor/nodeSort.c`
- `postgres/src/backend/utils/sort/tuplesort.c` (work_mem-governed external sort)

**goopg reference**
- `internal/executor/operators.go` (`sortOp.Open`)
- `internal/executor/spill.go` (`spillWriter`, `spillReader`)

**Divergence and performance impact**
- PostgreSQL sort implementation is memory-governed with robust external spill strategies.
- goopg has spill infrastructure (`spill.go`), but `sortOp` currently appends all rows to memory and then sorts, with no work_mem-style bounded strategy.
- This is the largest practical performance risk for big analytical queries: memory spikes / OOM instead of graceful degradation.

**Severity**: **High**

---

## 8) Handling of scanned data

**PostgreSQL reference**
- `postgres/src/backend/executor/execScan.c`
- `postgres/src/backend/executor/execTuples.c`

**goopg reference**
- `internal/executor/operators_storage.go`
  - `DecodeRowInto` path in seq-scan loop
  - eager detoast call path (`DetoastRow`) before returning row in scan flow

**Divergence and performance impact**
- goopg scan path decodes row payload into `Row` buffers explicitly in the scan loop.
- Eager decode/detoast behavior in scan flow can do more per-row work than strictly necessary for narrow projections or late materialization strategies.
- PostgreSQL’s slot/deform machinery has very mature optimization behavior around scan/projection integration.

**Severity**: **Medium**

---

## 9) Serialization (tuple / WAL / other data)

**PostgreSQL reference**
- `postgres/src/include/access/xlogrecord.h` (+ transam encode/decode paths)
- tuple/datums and TOAST machinery in heap/executor/access layers

**goopg reference**
- `internal/executor/datum.go`
- `internal/executor/codec.go` (row encode/decode)
- `internal/wal/format.go` (record framing)
- `internal/wal/xlog_record.go`

**Divergence and performance impact**
- goopg uses compact, Go-native encoding strategies for row/WAL payload handling, improving implementation simplicity and often reducing per-record overhead in legacy mode.
- PostgreSQL’s canonical format and ecosystem tooling compatibility are stronger by default.
- Maintaining dual WAL framing paths (legacy + PG-compat) creates technical/performance tuning overhead over time.

**Severity**: **Medium**

---

## Consolidated priority (performance-first)

1. **Materialization / sort memory control**: integrate spill path into sort execution (`sortOp`) with explicit memory budgeting.
2. **SI visibility hot path**: replace linear `HasInProgress` lookup with sorted/binary-search or hash-backed membership for high-concurrency workloads.
3. **Buffer manager concurrency**: reduce contention around `poolMu` with partitioned metadata locks.
4. **Checkpoint request decoupling**: make user-triggered checkpoint request asynchronous at entry point while preserving durability contract.
5. **WAL format convergence**: converge toward one fast default format per cluster mode to reduce branching and simplify optimization.

---

## Bottom line

The largest performance divergence today is **memory behavior under large sort/materialization workloads**, followed by **SI visibility lookup cost at high concurrency** and **single-mutex buffer metadata contention**.  
goopg already has strong building blocks (spill subsystem, scan ring, explicit I/O dedup, WAL compatibility mode), but key paths are still less bounded and less concurrency-scaled than PostgreSQL’s production-hardened implementation.
