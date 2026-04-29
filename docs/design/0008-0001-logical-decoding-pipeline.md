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

### Pipeline architecture (future loops)

The remaining pipeline pieces below land in subsequent loops:

1. **Reorder buffer.** A per-transaction in-memory accumulator keyed
   by `XID`. Each WAL record decoded into a change event is appended
   to its xact's queue; commit fires the queue at the output plugin
   in commit order; abort drops it.

2. **Snapshot builder.** Tracks committed transactions and produces
   a `HISTORIC` snapshot for the decoder. The simplest correct v0
   shape is "snapshot built at slot creation time stays consistent
   for the slot's lifetime" — schema changes during the slot's life
   are out of scope for the first usable cut and explicitly
   surface as decoder errors. Upstream's full `SnapBuild` state
   machine is the long-term target.

3. **Decoder loop.** Reads WAL through a `RecordIterator` (already in
   place from M0005), classifies each record (heap-insert /
   heap-delete / heap-vacuum / btree-* / checkpoint / xact-commit /
   xact-abort), routes data records into the reorder buffer keyed by
   xact, and on a commit record drains the buffer through the output
   plugin.

4. **Output plugin contract.** A small interface (`OutputPlugin`)
   that the decoder calls per change event and per commit. v0 ships
   one implementation: `pgoutput` (0008-0002).

These sub-pieces are explicitly **not** delivered in this loop —
they're documented here so the slot/view foundation has the right
shape from the start.

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

This loop's slice (slot foundation + view) is verified by:

- `internal/wal/slots_test.go` — `TestCreateLogicalSlot` pins that a
  logical slot is created with the right `Kind` / `Plugin` /
  `Database` / `CatalogXmin` and that `MinRestartLSN` includes it.
- `internal/wal/slots_test.go` — `TestPhysicalSlotJSONUnchangedAcrossM0008`
  pins the wire-format forward-compat: a physical slot persisted before
  M0008 (no `plugin` / `database` / `catalog_xmin` keys) round-trips
  cleanly into `Plugin == "" && Database == "" && CatalogXmin == 0`.
- `internal/initdb/replication_views_test.go` —
  `TestPgReplicationSlotsViewRendersBothKinds` pins that the view
  renders one row per slot with the upstream-shaped column set.

End-to-end pipeline verification (decoder, reorder buffer, apply) is
covered by the M0008 DoD tests added in subsequent loops.

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
