# Milestone 0013 — WAL Buffers Optimization with Eviction-Safe WAL-before-Data Durability

**Status:** planned
**Depends on:** Milestone 0001 (WAL writer baseline), Milestone 0002 (buffer flush and WAL-before-data integration), Milestone 0007 (fdatasync-based WAL durability contract).
**Drives:** Reduced WAL append-path write pressure while preserving strict WAL-before-data correctness and commit durability.

## Context

goopg currently appends WAL by writing segment files on the append path, then makes durability visible through FlushUpTo and dataSync barriers.
This milestone introduces PostgreSQL-style WAL buffering behavior: generated WAL records are first retained in a bounded in-memory WAL buffer controlled by wal_buffers (default 16 MB), and are not written to segment files while capacity allows.

This optimization is valid only if WAL-before-data remains strict under pressure and eviction. The implementation must therefore guarantee that buffered WAL is forced to disk and made durable whenever required by data-page flush ordering.

## WAL Rule Guardrails (Mandatory)

- No data page may be written unless every WAL record up to that page LSN is already durable.
- WAL records still in memory buffer must be drainable on demand to satisfy FlushUpTo(targetLSN).
- Overflow-drained WAL bytes must be tracked as sync debt so later durability barriers include them.

## In Scope

### wal_buffers GUC and Bounded In-Memory WAL Buffer

- Add wal_buffers configuration surface with default 16 MB.
- Keep newly generated WAL records in memory buffer while usage is within capacity.
- Preserve global LSN ordering with the writer goroutine as the single serialization point.

### Overflow-Triggered Drain to WAL Segments

- If an append would exceed wal_buffers, drain buffered WAL records to WAL segment files in LSN order.
- Maintain contiguous WAL stream semantics across segment boundaries.
- Record drained segment debt so subsequent FlushUpTo/dataSync includes overflow-written bytes.

### FlushUpTo Two-Stage Semantics

- Stage 1: Ensure all WAL bytes up to targetLSN are present in WAL segment files (drain from memory buffer as needed).
- Stage 2: Execute dataSync durability barrier on all relevant dirty WAL segments through targetLSN.
- Preserve existing error contract for truly unwritten LSN requests.

### Shared-Buffer Eviction and Data-Flush Safety

- Before shared-buffer eviction or data-page writeback, if required WAL bytes for page LSN remain in WAL memory buffer:
- Force WAL drain through page LSN.
- Force durability barrier through page LSN.
- Only after successful WAL durability may data-page write proceed.
- Apply the same invariant to both eviction-driven and checkpoint-driven data flush paths.

### Concurrency and Failure Handling

- Keep writer state transitions atomic with respect to append, overflow drain, and flush.
- Ensure failed WAL file writes do not advance visible writeLSN or drain pointers.
- Ensure failed durability barrier keeps pending sync debt intact for retry.

### Observability

- Add counters for current buffered WAL bytes, overflow drains, forced drains due to page flush ordering, and bytes drained by FlushUpTo.
- Add startup/runtime diagnostics showing wal_buffers activation and forced-drain events.

## Out of Scope

- wal_writer_delay or wal_writer_flush_after style background scheduling parity.
- WAL format redesign.
- Non-WAL storage I/O redesign.
- Logical replication protocol changes.

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- 0013-0001-wal-buffers-architecture.md
- 0013-0002-overflow-and-eviction-durability-ordering.md
- 0013-0003-wal-buffers-observability-and-rollout.md

These design docs should cross-link to:

- docs/design/root-0008-wal-and-recovery.md
- docs/design/0002-0001-checkpointing.md
- docs/design/0007-0002-fdatasync-commit-path.md

## Reference

Upstream sources to consult:

- postgres/src/backend/access/transam/xlog.c
- postgres/src/backend/storage/buffer/bufmgr.c
- postgres/src/include/access/xlog_internal.h

## Definition of Done

1. wal_buffers exists with default 16 MB.
2. Append path keeps WAL in memory while within wal_buffers capacity.
3. Overflow drains WAL to segment files in strict LSN order with no gaps.
4. Overflow-drained bytes are tracked as dirty WAL sync debt for later dataSync.
5. FlushUpTo(targetLSN) first drains buffered WAL through targetLSN, then performs durability barrier.
6. Page eviction/writeback path enforces WAL durability through page LSN before data-page write.
7. Checkpoint-driven data flush path enforces the same WAL-before-data durability rule.
8. Regression tests cover normal buffering, overflow drain, forced drain by eviction/checkpoint, and failure/retry cases.
9. Required design docs are merged and accepted.
