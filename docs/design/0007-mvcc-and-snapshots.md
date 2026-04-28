# 0007 — MVCC Tuple Header and Snapshot Manager (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 requires both:

1. heap tuple format carrying `xmin` / `xmax` visibility metadata
2. snapshot semantics for `READ COMMITTED` and `REPEATABLE READ`

This loop completes both items with code in `internal/storage` and
`internal/mvcc`.

References into upstream:

- `postgres/src/include/access/htup_details.h` — `HeapTupleHeaderData`
- `postgres/src/include/storage/itemid.h` — line pointer bit packing
- `postgres/src/backend/utils/time/snapmgr.c` — snapshot lifecycle

## Decision

### Tuple header and line pointers

`internal/storage/heap.go` implements a minimal heap tuple layer:

- fixed tuple header (`xmin`, `xmax`, `xvac`, `ctid`, `infomask`,
  `infomask2`, `t_hoff`)
- binary marshal/unmarshal helpers
- line-pointer (`ItemIdData`) bit packing (15-bit off, 2-bit flags,
  15-bit len)
- page-level tuple write/read helpers (`PageAddHeapTuple`,
  `PageGetHeapTuple`)

Field ordering follows upstream. On x86_64 Linux we store in
little-endian. v0 defaults to `t_hoff=24` for tuples without null
bitmap/OID.

### Snapshot manager

`internal/mvcc` introduces a mutex-guarded transaction/snapshot manager:

- xid allocation starts at 3 (`FirstNormalTransactionID`), matching the
  upstream notion of normal xids after bootstrap/frozen ids
- active transaction set is tracked in-memory
- snapshot shape is `(xmin, xmax, in-progress[])`

Snapshot acquisition semantics:

- `READ COMMITTED`: fresh snapshot per statement
- `REPEATABLE READ`: first statement snapshot is pinned for the
  transaction lifetime

The parser/executor path that issues `BEGIN`/`COMMIT` lands in milestone
6; this package provides the storage-consistent semantics and API seam
for that integration.

### Tuple visibility function

`internal/mvcc/visibility.go` provides tuple visibility checks over
`storage.HeapTupleHeader` plus a statement snapshot and current xid.
v0 behavior aligns with PostgreSQL's common-case snapshot checks:

- insert xid must be visible as committed unless it is our own xid
- delete xid hides the tuple only when visible as committed or equal to
  current xid
- in-progress/future delete xids keep the tuple visible

## Alternatives considered

- **No centralized snapshot manager; read active xids ad hoc in each
  caller.** Rejected: would duplicate concurrency control logic and
  create semantic drift across executor paths.
- **Treat SERIALIZABLE as unsupported now.** Rejected for startup
  compatibility: `ParseIsolationLevel` accepts it and currently maps it
  to repeatable-read snapshot behavior until SSI lands.

## Consequences

- Heap tuples persist `xmin` / `xmax` metadata and can be interpreted by
  a shared visibility function.
- Snapshot behavior for `READ COMMITTED` and `REPEATABLE READ` is now
  explicit, testable, and isolated from parser progress.
- `SERIALIZABLE` currently aliases repeatable-read semantics and is
  documented as an intentional temporary deviation.
