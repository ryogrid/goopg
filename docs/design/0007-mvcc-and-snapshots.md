# 0007 — MVCC Tuple Header and Snapshot Direction (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 requires two adjacent capabilities:

1. heap tuple format carrying `xmin` / `xmax` visibility metadata
2. snapshot semantics (`READ COMMITTED` / `REPEATABLE READ`)

This loop implements (1) in `internal/storage` and defines the v0
direction for (2).

References into upstream:

- `postgres/src/include/access/htup_details.h` — `HeapTupleHeaderData`
- `postgres/src/include/storage/itemid.h` — line pointer bit packing
- `postgres/src/backend/utils/time/snapmgr.c` — snapshot lifecycle

## Decision

### Implemented now (tuple format)

`internal/storage/heap.go` adds a minimal heap tuple layer:

- fixed tuple header with `xmin`, `xmax`, `xvac`, `ctid`,
  `infomask`, `infomask2`, `t_hoff`
- binary marshal/unmarshal helpers
- page line-pointer (`ItemIdData`) encode/decode (15-bit off/len,
  2-bit flags)
- `PageAddHeapTuple` and `PageGetHeapTuple`

This gives an on-page tuple representation that already carries MVCC
metadata, even though visibility checks are not yet centralized in a
snapshot manager.

### Header subset and defaults

v0 uses the upstream field ordering and little-endian encoding on
x86_64 Linux. The default tuple payload offset is aligned to 24 bytes
(`t_hoff=24`) for tuples without null bitmap/OID.

### Snapshot direction (next loop)

Snapshot manager work is intentionally separated into the next item.
The tuple shape implemented here is sufficient for that next step:

- `xmin` / `xmax` are present and stable on-disk
- tuple fetch returns parsed header + payload
- visibility logic can be added without reworking page packing

Planned snapshot API direction:

- transaction-visible check function taking tuple header + snapshot
- snapshot types for `READ COMMITTED` and `REPEATABLE READ`
- xid horizon fields equivalent to upstream active-xid snapshot set

## Alternatives considered

- **Implement tuple layout + snapshot manager in one loop.**
  Rejected to preserve one-item-per-loop discipline and keep changes
  auditable.
- **Defer tuple header and store user payload only.**
  Rejected because visibility metadata must be stored with the tuple;
  retrofitting later would force page rewrite logic changes.

## Consequences

- Heap tuples can now persist `xmin` / `xmax` metadata.
- WAL page-image replay already works with these tuple-carrying pages.
- Snapshot manager can be implemented as pure interpretation logic over
  the stored tuple header fields.
