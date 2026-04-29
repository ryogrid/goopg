# Milestone 0008 — Logical Replication Support

**Status:** planned
**Depends on:** Milestone 0001 (foundational server, wire-protocol
compatibility), Milestone 0002 (durable WAL with full-page-write
semantics), Milestone 0005 (physical streaming replication, replication
slots, and the replication-protocol harness), Milestone 0007 (WAL segment
preallocation and `fdatasync`-based commit path — required so the logical
decoding plugin can rely on a stable WAL stream).
**Drives:** Selective per-table replication, change-data-capture style
integrations, and the operational pattern of upgrading or sharding by
streaming changes from a publisher to one or more subscribers.

## Context

Milestone 0005 brought goopg up to physical streaming replication: a
primary streams its raw WAL to a standby, which replays it byte-for-byte.
That model is correct and operationally simple but offers no selectivity:
the whole cluster moves together, and the receiving side must be a
binary-compatible standby rather than an independent server applying
logical row-level changes.

Logical replication closes this gap. Upstream PostgreSQL exposes it
through three layers — logical decoding (turn WAL into row-level changes),
the `pgoutput` output plugin (encode those changes for the wire), and the
publication / subscription DDL surface (`CREATE PUBLICATION`,
`CREATE SUBSCRIPTION`) that operators actually configure. This milestone
introduces the equivalent in goopg, scoped tightly to row-level INSERT /
UPDATE / DELETE replication for tables with replica identity, with DDL,
sequences, and large objects deferred.

The milestone explicitly leans on M0005's slot machinery and on M0007's
WAL on-disk guarantees: logical decoding requires that historic WAL
segments remain readable for as long as a logical slot needs them, which
in turn depends on the slot-aware retention work already done in M0005 and
the predictable, preallocated, `fdatasync`-flushed segments delivered in
M0007.

## In Scope

### Logical Decoding

- WAL decoding pipeline that reads committed WAL records from a logical
  replication slot and turns them into a stream of row-level change
  events (`INSERT`, `UPDATE`, `DELETE`, plus transaction begin / commit
  markers) in commit order.
- Reorder buffer that holds in-progress transactions until commit, so
  that uncommitted changes are never emitted, and that aborted
  transactions are dropped.
- Snapshot building sufficient to expose committed-but-not-yet-decoded
  history when a slot is created, mirroring upstream's `SnapBuild`
  state machine closely enough that a slot started against a quiescent
  primary produces a consistent initial position.

### Logical Replication Slots

- Logical-slot variant of the existing physical replication-slot
  surface from M0005. Logical slots track the oldest WAL position the
  decoder still needs (`restart_lsn`) and the oldest in-progress
  transaction's catalog xmin (`catalog_xmin`).
- WAL retention behaviour that is safe for logical slots: the slot
  prevents removal of WAL segments and prevents catalog row pruning
  needed for historic snapshots.
- `pg_replication_slots` (or its goopg-equivalent system view from
  M0005) gains the logical-slot fields (`slot_type = 'logical'`,
  `plugin`, `database`, `confirmed_flush_lsn`, `catalog_xmin`).

### `pgoutput` Output Plugin

- A built-in output plugin named `pgoutput`, wire-compatible with
  upstream's `pgoutput` for the message types this milestone covers
  (`B`, `C`, `R`, `I`, `U`, `D`, plus the relation / type metadata
  messages required to make those interpretable).
- Replica-identity handling: `DEFAULT` (primary key), `FULL`,
  `NOTHING`, and `USING INDEX` semantics on the publisher side
  determine which old / new tuple columns are emitted.
- Protocol-version negotiation matching upstream's `pgoutput` v1
  semantics. Streaming of in-progress transactions and binary-format
  emission are *out of scope* (see below).

### Publication / Subscription DDL

- `CREATE PUBLICATION`, `ALTER PUBLICATION`, `DROP PUBLICATION` with
  per-table membership (`FOR TABLE …`) and the `publish` option for
  selecting `insert`, `update`, `delete` (truncate is out of scope).
  `FOR ALL TABLES` is in scope.
- `CREATE SUBSCRIPTION`, `ALTER SUBSCRIPTION`, `DROP SUBSCRIPTION`
  with the slot-creation and apply-worker lifecycle that operators
  expect: creating a subscription provisions a slot on the publisher
  and starts an apply worker on the subscriber.
- System catalogs / views: `pg_publication`, `pg_publication_rel`,
  `pg_publication_tables`, `pg_subscription`, `pg_subscription_rel`,
  populated and queryable.

### Apply Worker

- Subscriber-side apply worker that connects to the publisher's
  logical slot, decodes the `pgoutput` stream, and applies each
  change to the corresponding local table inside a transaction that
  commits at the publisher's commit boundary.
- Conflict surface for the obvious row-level cases (missing target
  row on UPDATE / DELETE, duplicate key on INSERT). Conflicts surface
  through the apply worker's error path and the apply worker stops
  with an actionable error — automatic conflict resolution is *out
  of scope*.
- Restart-safety: the apply worker resumes from `confirmed_flush_lsn`
  after a clean stop, a publisher disconnect, or a crash on either
  side.

### Initial Table Sync

- Per-table initial synchronisation when a subscription is created or
  a table is added to a subscription: a synchronisation worker copies
  the current contents and then hands off to the streaming apply
  worker at the slot's snapshot LSN, with no duplicated rows and no
  gap between copy-end and stream-start.
- `pg_subscription_rel.srsubstate` tracks each table's sync phase
  (`i`, `d`, `s`, `r`) using the upstream letter codes.

### Observability

- `pg_stat_replication` (publisher side) and
  `pg_stat_subscription` (subscriber side) gain logical-replication
  rows: connection state, last-applied LSN, replay lag, last-error.
- Apply-worker and walsender logs include slot name, publication
  name, current LSN, and a structured reason for any disconnection,
  matching the M0005 replication-event-logging contract.
- Operational controls for inspecting and resetting subscription
  state in test environments (skip a transaction, disable the
  apply worker, advance the slot) are in scope as a minimum
  reset surface.

## Out of Scope

- Truncate replication (`TRUNCATE` message in `pgoutput`).
- DDL replication. Schema changes must be applied out-of-band on
  publisher and subscriber.
- Sequence replication.
- Large-object (`pg_largeobject`) replication.
- Streaming of in-progress transactions (`pgoutput` protocol v2's
  `S` / `Y` / `c` messages). Decoding only emits at commit time.
- Two-phase-commit decoding.
- Binary-format / `binary = on` subscriptions. Text format only in
  this milestone.
- Row filters and column lists on publications. Whole-row, whole-set
  replication only.
- Conflict resolution beyond stop-on-error.
- Cross-version logical replication. Publisher and subscriber are
  both goopg at the same milestone.
- Logical replication between goopg and upstream PostgreSQL. The
  pgoutput-wire-compatibility goal here is a stepping stone, not a
  promised compatibility surface for this milestone's DoD.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0008-0001-logical-decoding-pipeline.md` — WAL → change-event
  pipeline, reorder buffer, snapshot building, interaction with the
  M0005 slot machinery and the M0007 WAL retention guarantees.
- `0008-0002-pgoutput-plugin.md` — message formats this milestone
  supports, replica-identity handling, protocol-version negotiation,
  cross-references to upstream `pgoutput.c`.
- `0008-0003-publication-subscription-ddl.md` — parser surface,
  catalog tables and views, lifecycle of `CREATE SUBSCRIPTION` and
  the slot it provisions on the publisher.
- `0008-0004-apply-worker-and-tablesync.md` — apply-worker process
  model, initial table-sync worker, `pg_subscription_rel` state
  machine, restart and conflict semantics.
- `0008-0005-logical-replication-observability.md` — system views,
  metrics, and structured logging for logical replication, building
  on the M0005 replication-event-logging contract.

## Reference

Upstream sources to consult:

- `postgres/src/backend/replication/logical/` — `decode.c`,
  `reorderbuffer.c`, `snapbuild.c`, `logical.c`, and the slot
  lifecycle in `slot.c`.
- `postgres/src/backend/replication/pgoutput/pgoutput.c` — output
  plugin, message encoding, replica-identity handling.
- `postgres/src/backend/replication/logical/worker.c` and
  `tablesync.c` — apply worker and per-table sync worker.
- `postgres/src/backend/commands/publicationcmds.c` and
  `subscriptioncmds.c` — DDL semantics, catalog updates, and the
  cross-server slot-creation handshake.
- `postgres/src/include/catalog/pg_publication.h`,
  `pg_publication_rel.h`, `pg_subscription.h`,
  `pg_subscription_rel.h` — catalog shapes.

## Definition of Done

1. A publisher and a subscriber, both running goopg, can be started
   from clean state. `CREATE PUBLICATION p FOR TABLE t` on the
   publisher and `CREATE SUBSCRIPTION s CONNECTION '…' PUBLICATION p`
   on the subscriber provisions a logical slot, runs initial table
   sync, and then streams ongoing INSERT / UPDATE / DELETE on `t` to
   the subscriber.
2. Replica-identity behaviour is correct: `DEFAULT` requires a primary
   key and emits only key columns in the old tuple of UPDATE / DELETE,
   `FULL` emits the entire old tuple, and `NOTHING` causes UPDATE /
   DELETE on that table to fail at the publisher with a clear error.
3. The subscription survives clean restarts of either node and a
   `SIGKILL` of the apply worker: replication resumes from the last
   `confirmed_flush_lsn` with no duplicated rows and no missed rows.
4. WAL retention is safe for logical slots: a stopped subscriber pins
   WAL on the publisher, the `wal_retained_bytes` (or equivalent)
   indicator advances, and resuming the subscriber drains it without
   data loss.
5. `pg_publication`, `pg_publication_rel`, `pg_publication_tables`,
   `pg_subscription`, and `pg_subscription_rel` are queryable and
   reflect the live state of a multi-table publication and its
   subscription.
6. `pg_stat_replication` and `pg_stat_subscription` expose
   logical-replication health (connection state, last-applied LSN,
   replay lag), and the structured replication-event log from M0005
   carries logical-replication events with publication / subscription
   identifiers.
7. End-to-end tests exercise: initial sync of a non-empty table,
   ongoing apply across publisher restart, ongoing apply across
   subscriber restart, an `ALTER PUBLICATION` that adds a table
   (triggering a new tablesync), a `DROP SUBSCRIPTION` that releases
   the publisher slot, and a conflict (duplicate-key INSERT) that
   stops the apply worker with an actionable error.
8. All required design docs (`0008-0001` … `0008-0005`) are merged
   with status `accepted`, and the M0005 streaming-replication
   "Out of Scope" entry that defers logical replication is
   cross-linked to this milestone.
