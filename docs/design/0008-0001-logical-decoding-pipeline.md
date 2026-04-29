# Logical Decoding Pipeline (Milestone 0008)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0008 — Logical Replication Support                     |
| Refines    | [0005-0001-streaming-replication-architecture.md](0005-0001-streaming-replication-architecture.md), [0005-0004-slot-aware-wal-retention.md](0005-0004-slot-aware-wal-retention.md), [0007-0001-wal-segment-preallocation.md](0007-0001-wal-segment-preallocation.md) |
| Supersedes | —                                                      |

## Problem

M0005 delivered physical streaming replication: the standby pulls raw
WAL bytes from the primary's walsender and replays them. That model
gives byte-for-byte cluster cloning, but it has no selectivity — there
is no way to replicate "only table `t` from database `db`", and the
receiving side must be a binary-compatible standby rather than an
independent server applying logical row-level changes.

M0008 introduces logical replication: a publisher decodes its WAL
into a stream of row-level change events
(`INSERT` / `UPDATE` / `DELETE` plus transaction begin / commit), and
a subscriber applies those events to its local tables. Upstream
PostgreSQL implements this on top of a logical-decoding pipeline:

```
WAL records  ──▶ slot reader  ──▶ reorder buffer  ──▶ output plugin  ──▶ wire
                                  ▲   (per-xact)      (pgoutput)
                                  │
                              snapshot builder
                              (HISTORIC snapshots)
```

This document is the milestone-level design for that pipeline. It
covers four sub-pieces, each with its own implementation slice across
multiple loops:

1. **Logical replication slots** — the persistent state that anchors a
   reader at a specific WAL position and prevents WAL / catalog row
   reclamation it still needs.
2. **Reorder buffer** — buffers per-transaction change events until
   commit, drops them on abort.
3. **Snapshot builder** — provides a HISTORIC snapshot consistent at a
   specific commit position so the decoder can read a relation's
   catalog state as it was when the WAL record was written.
4. **Decoder loop** — reads WAL through the slot, dispatches records
   into the reorder buffer, and on commit drives the output plugin to
   emit the per-transaction event stream.

The output plugin (`pgoutput`) lives in
[0008-0002-pgoutput-plugin.md](0008-0002-pgoutput-plugin.md). The
publication / subscription DDL surface lives in
[0008-0003-publication-subscription-ddl.md](0008-0003-publication-subscription-ddl.md).
The apply worker and tablesync live in
[0008-0004-apply-worker-and-tablesync.md](0008-0004-apply-worker-and-tablesync.md).
Observability lives in
[0008-0005-logical-replication-observability.md](0008-0005-logical-replication-observability.md).

## Decision

### Logical replication slots — first slice

`internal/wal/slots.go` already supports `SlotPhysical`. Extend it
with `SlotLogical` and four upstream-shaped fields:

```go
const (
    SlotPhysical SlotKind = "physical"
    SlotLogical  SlotKind = "logical"
)

type Slot struct {
    Name              string   `json:"name"`
    Kind              SlotKind `json:"kind"`
    RestartLSN        uint64   `json:"restart_lsn"`
    ConfirmedFlushLSN uint64   `json:"confirmed_flush_lsn"`
    Invalidated       bool     `json:"invalidated,omitempty"`

    // Logical-slot-only fields. JSON-tagged with omitempty so a
    // physical slot's on-disk file stays byte-identical to its
    // pre-M0008 shape, and physical Slots loaded from a pre-M0008
    // state file round-trip cleanly with these fields zero-valued.
    Plugin      string `json:"plugin,omitempty"`
    Database    string `json:"database,omitempty"`
    CatalogXmin uint64 `json:"catalog_xmin,omitempty"`

    Active bool `json:"-"`
}
```

The mapping to upstream's `pg_replication_slots` columns:

| Column                  | Source field                                      |
| ----------------------- | ------------------------------------------------- |
| `slot_name`             | `Slot.Name`                                       |
| `plugin`                | `Slot.Plugin` (NULL/empty for physical)           |
| `slot_type`             | `Slot.Kind` (`"physical"` / `"logical"`)          |
| `database`              | `Slot.Database` (NULL/empty for physical)         |
| `temporary`             | always `false` in v0 (temporary slots deferred)   |
| `active`                | `Slot.Active`                                     |
| `xmin`                  | always `0` in v0 (xmin tracking deferred)         |
| `catalog_xmin`          | `Slot.CatalogXmin`                                |
| `restart_lsn`           | `Slot.RestartLSN`                                 |
| `confirmed_flush_lsn`   | `Slot.ConfirmedFlushLSN`                          |
| `wal_status`            | derived from `Slot.Invalidated` (`reserved` /
                           `lost`)                                            |

`Slots.Create` already takes a `SlotKind` argument; this loop drops
the `if kind != SlotPhysical { return ErrSlotKindMismatch }` guard
and accepts both. A new typed constructor
`Slots.CreateLogical(name, plugin, database, startLSN)` is provided
for clarity.

### `pg_replication_slots` virtual view

`internal/initdb/replication_views.go` already houses
`pg_stat_replication` and `pg_stat_wal_receiver`. This loop adds
`pg_replication_slots` backed by the existing `*wal.Slots` registry.
Column order mirrors upstream PG 18.x. The view powers the
`pg_replication_slots` row-set described in the M0008 DoD.

### WAL retention is already slot-aware

M0005 / 0005-0004 already keys retention off
`Slots.MinRestartLSN()` and `max_slot_wal_keep_size`. Logical slots
are picked up by exactly the same code path because they share the
`Slot` shape and the `RestartLSN` field. A logical slot pinning WAL
inhibits checkpointer-driven recycling exactly the same way a
physical slot does. No new retention code is needed in this slice.

### Catalog xmin retention — explicit deferral

A logical slot's `CatalogXmin` is the oldest xact whose catalog row
versions the decoder may still need to reconstruct historic
snapshots. Upstream's vacuum and pruning paths consult this so they
don't reclaim catalog tuples a logical slot can still see.

v0's vacuum / pruning paths consult only the global `oldestXmin`
horizon — they don't yet honour per-slot `catalog_xmin`. This
**is a correctness gap** for logical replication of tables whose
schemas can change while a slot is active; it's tracked under
`0008-0001-followup` and addressed in a sibling loop. The slot
field is persisted now so the surface is in place when the vacuum
hook lands.

### Pipeline architecture

The first M0008 / 0008-0001 loop landed the slot foundation + view
described above. This second loop lands the reorder-buffer and
decoder-orchestration layer in `internal/wal/reorder.go` and
`internal/wal/decoder.go` — pure, sequential, in-process data
structures that the future WAL classifier will drive.

1. **Reorder buffer (`internal/wal/reorder.go`).** Per-transaction
   in-memory accumulator keyed by `storage.TransactionID`.
   - `Append(xid, Change)` queues `Change` under `xid` (first
     append records the begin LSN).
   - `Commit(xid)` returns the queued changes in append order and
     drops the entry; the caller drives the output plugin with
     the returned slice.
   - `Abort(xid)` drops the entry without producing output.
   - `Active() / OldestBeginLSN()` for observability and the
     publisher's catalog-xmin tracker.

   Single-process, single-decoder for v0 — no goroutine safety
   because the decoder loop is sequential. xid==0 (the
   `InvalidTransactionID` sentinel) is rejected at Append time.

2. **Decoder orchestrator (`internal/wal/decoder.go`).** Sits
   between the reorder buffer and the output plugin. Defines:
   - `OutputPlugin` interface — `Begin(xid, commitLSN)` /
     `Change(c Change)` / `Commit(xid, commitLSN)`. pgoutput will
     be the first implementation (0008-0002).
   - `Decoder.ApplyChange(xid, c)`, `ApplyCommit(xid, commitLSN)`,
     `ApplyAbort(xid)` — the data-flow contract that the future
     WAL classifier calls into.
   - `ApplyCommit` drains the buffer through `Begin → Change* →
     Commit` in append order. Unknown xids are no-ops (catalog-
     only xacts that the classifier filtered every change for).
     `ErrNoPlugin` when no plugin was provided and a commit-with-
     changes happens.

3. **Snapshot builder.** Tracks committed transactions and produces
   a `HISTORIC` snapshot for the decoder. The simplest correct v0
   shape is "snapshot built at slot creation time stays consistent
   for the slot's lifetime" — schema changes during the slot's
   life are out of scope for the first usable cut and explicitly
   surface as decoder errors. Upstream's full `SnapBuild` state
   machine is the long-term target. Deferred to the next M0008
   loop.

4. **WAL classifier (`internal/wal/classifier.go`).** Walks decoded
   `Record`s and dispatches them into a `*Decoder` by xid. Loop 3
   delivered:

   - New WAL record kinds `RecordKindXactCommit` (8) and
     `RecordKindXactAbort` (9), 5-byte payloads (`kind | xid`).
     `EncodeXactCommit` / `EncodeXactAbort` / `DecodeXactMarker`
     are the encoders. `ApplyRecord` treats both as physical-
     recovery no-ops — they exist purely so the M0008 logical
     decoder can drive its reorder buffer.
   - `Classify(decoder, record)` extracts the xact id per kind:
     - `HeapInsert`: xmin parsed from the encoded heap-tuple
       header (offset 0..3, mirrors `storage.HeapTuple.MarshalBinary`).
     - `HeapDelete`: the encoded `xmax` field — already in the
       record, no wire-format change.
     - `XactCommit` / `XactAbort`: xid in the payload.
     - All other kinds (vacuum, btree-*, page-image, checkpoint)
       are silently skipped — not user-data transactional events.

   Per-record xid plumbing on the existing logical change records
   was avoided because the on-disk tuple body already carries
   xmin and HeapDelete already carries xmax. That keeps the wire
   format backwards-compatible with persisted WAL streams.

   Loop 4 closed the wire-layer emission: `mvcc.Manager` grew
   `SetXactMarkerLogger(fn func(xid, kind) error)` and `Commit`/
   `Rollback` invoke it under the manager's lock before removing
   the txn from the active set. A logger error propagates back
   through `Commit`/`Rollback` so a WAL-append failure stops the
   txn from finishing — the caller can retry or escalate.
   `initdb.Open` installs a hook that calls
   `walWriter.Append(EncodeXactCommit/Abort(xid))`, so every
   server path that flows through `mvcc.Manager.Commit/Rollback`
   (simple-query, extended-query, COPY) automatically emits
   markers without needing per-call-site code changes.

   The end-to-end pin lives in
   `internal/initdb/open_test.go::TestOpenWiresXactMarkerHook`:
   begin/commit + begin/rollback through the runtime's TxnMgr,
   then `wal.ReadAll` against the data directory finds both
   markers with matching xids.

5. **Output plugin contract.** Already shipped by this loop as
   `wal.OutputPlugin`. v0's first implementation is `pgoutput`
   (0008-0002).

### Out of scope for this milestone

(Mirrors the M0008 milestone "Out of scope" section verbatim;
duplicated here for design-doc completeness.)

- Truncate replication.
- DDL replication.
- Sequence replication.
- Large-object replication.
- Streaming of in-progress transactions (`pgoutput` v2 protocol).
- Two-phase-commit decoding.
- Binary format subscriptions.
- Row filters / column lists on publications.
- Conflict resolution beyond stop-on-error.
- Cross-version logical replication.
- Logical replication between goopg and upstream PostgreSQL.

## Verification

Slot foundation + view (loop 1):

- `internal/wal/slots_test.go::TestCreateLogicalSlot` — logical slot
  is created with the right `Kind` / `Plugin` / `Database` /
  `CatalogXmin` and `MinRestartLSN` includes it.
- `internal/wal/slots_test.go::TestPhysicalSlotJSONUnchangedAcrossM0008`
  — pre-M0008 physical-slot files (no `plugin`/`database`/
  `catalog_xmin` keys) round-trip cleanly.
- `internal/initdb/replication_views_test.go::TestPgReplicationSlotsViewRendersBothKinds`
  — view renders one row per slot with the upstream column shape.

Wire-layer xact-marker emission (loop 4):

- `internal/mvcc/manager_test.go::TestXactMarkerLoggerCalledOnCommit`
  / `…OnRollback` — successful Commit/Rollback invokes the logger
  with the right xid and kind.
- `…::TestXactMarkerLoggerErrorAbortsCommit` — a logger error
  surfaces as a Commit error and the txn stays in-progress.
- `internal/initdb/open_test.go::TestOpenWiresXactMarkerHook` —
  end-to-end: a real Begin/Commit through `Runtime.TxnMgr` lands
  an `XactCommit` record in the WAL stream readable via
  `wal.ReadAll`.

WAL classifier (loop 3):

- `internal/wal/classifier_test.go::TestClassifyHeapInsertRoutesByXmin`
  — xmin from the heap-tuple body drives the xid dispatch.
- `…::TestClassifyHeapDeleteRoutesByXmax` — xmax from the record
  payload drives the xid dispatch.
- `…::TestClassifyAbortDropsXact` — `XactAbort` marker drops the
  xact's queued changes; the plugin never sees them.
- `…::TestClassifyIsolatesConcurrentXacts` — interleaved xids stay
  isolated through the classifier and through the buffer.
- `…::TestClassifySkipsNonTxRecords` — vacuum, btree, page-image,
  checkpoint records are silently skipped.
- `…::TestEncodeDecodeXactMarker` — round-trip for the new marker
  records.

Reorder buffer + decoder orchestration (loop 2):

- `internal/wal/reorder_test.go::TestReorderBufferCommitDrainsInOrder`
  — append-order is preserved through commit.
- `…::TestReorderBufferAbortDropsChanges` — aborted xact's queued
  changes never resurface.
- `…::TestReorderBufferIsolatesXacts` — concurrent xact queues are
  independent; committing one doesn't affect the other.
- `…::TestReorderBufferOldestBeginLSN` — tracks the smallest
  in-flight begin LSN for the catalog-xmin contract.
- `…::TestReorderBufferRejectsInvalidXID` — `xid==0` defensive guard.
- `…::TestDecoderApplyCommitDrivesPlugin` — pins the
  `Begin → Change* → Commit` ordering through the plugin interface.
- `…::TestDecoderAbortSkipsPlugin` — aborted xacts don't reach the
  plugin.
- `…::TestDecoderUnknownCommitIsNoop` — a commit on an unseen xid
  is a no-op (handles catalog-only xacts where every change was
  filtered).
- `…::TestDecoderRequiresPlugin` — `ErrNoPlugin` when no plugin is
  configured and a commit-with-changes arrives.

End-to-end pipeline verification (WAL classifier, snapshot builder,
pgoutput, apply worker) is covered by tests added in subsequent
M0008 loops.

## Cross-references

- Milestone: `docs/milestones/0008-logical-replication-support.md`.
- Predecessor (physical slots): `0005-0004-slot-aware-wal-retention.md`.
- Sibling design docs in M0008: `0008-0002` … `0008-0005` (planned).
- Upstream:
  - `postgres/src/backend/replication/logical/decode.c` — record
    classification.
  - `postgres/src/backend/replication/logical/reorderbuffer.c` —
    per-xact buffering.
  - `postgres/src/backend/replication/logical/snapbuild.c` —
    historic snapshot machinery.
  - `postgres/src/backend/replication/slot.c` — slot lifecycle.
  - `postgres/src/include/catalog/pg_replication_slots.h` — view
    column shape.
