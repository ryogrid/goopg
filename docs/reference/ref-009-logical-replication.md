# REF-009: Logical Replication

## Overview

Logical replication streams changes (INSERT/UPDATE/DELETE) from a publisher to a subscriber. Unlike physical replication (which copies WAL blocks), logical replication decodes changes into row-level operations, enabling selective replication and cross-version compatibility.

## goopg Implementation

**Packages:** `internal/wal/pgoutput.go`, `internal/server/logicalwalsender.go`, `internal/wal/slot_decoder.go`

### Architecture

Logical replication in goopg follows PostgreSQL's architecture:

1. **Publication** — defines a set of tables to replicate. Created via `CREATE PUBLICATION`.
2. **Slot** — a replication slot tracks the subscriber's position in the WAL stream.
3. **pgoutput plugin** — decodes WAL records into protocol messages (Begin, Insert, Update, Delete, Commit).
4. **Subscription** — creates the apply worker on the subscriber side.
5. **Apply worker** — receives decoded messages and applies them to the local tables.

### pgoutput Plugin

`internal/wal/pgoutput.go` implements the streaming protocol:

- `Begin` — marks the start of a transaction.
- `Relation` — describes a table schema (column names and types).
- `Insert` — supplies the new row image.
- `Update` — supplies old and new row images.
- `Delete` — supplies the old row image (or key).
- `Commit` — marks transaction end.

The plugin is invoked via `slot_decoder.go`'s `Decode` loop, which
walks WAL records and calls the plugin for each decoded record.

### Apply Worker

The apply worker (`internal/server/logicalreceiver.go`) connects to
the publisher, streams changes via the pgoutput protocol, and
applies them to the subscriber's tables.

### Tablesync

Initial table synchronisation copies the current snapshot of a
table via COPY, then switches to streaming. Managed by the
tablesync state machine (catalog-based `pg_subscription_rel`
states: `i` → init, `d` → data sync, `s` → synced, `r` → ready).

## PostgreSQL Implementation (Deep Dive)

### Parallel Apply (PG 15+)

PostgreSQL 15 introduced parallel apply for logical replication:

- **Ordered apply** — the subscriber replicates transactions in
  the same order they committed on the publisher. This preserves
  causal consistency but limits throughput.
- **Unordered (parallel) apply** — independent transactions
  (those that did not touch the same tables) are applied in
  parallel. The leader apply process determines independence and
  dispatches work to parallel apply workers.

goopg's apply worker is serial — it applies one transaction at
a time.

### Two-Phase Commit (PG 15+)

PostgreSQL 15+ supports two-phase transactions (`PREPARE
TRANSACTION` / `COMMIT PREPARED`) in logical replication. The
subscriber prepares the transaction and awaits the commit
decision from the publisher.

goopg does not support two-phase commit in replication.

### Origin Tracking

Each logical replication message carries a **replication origin**
(the publisher's LSN and a unique origin ID). The subscriber
tracks the applied origin LSN, ensuring that a change applied
by replication is not re-replicated back to the publisher (loop
detection).

goopg does not implement origin tracking.

### Conflict Detection

When applying changes, PostgreSQL's apply worker detects several
conflict types:
- **Duplicate key** — INSERT with a key that already exists.
- **Tuple modified** — UPDATE/DELETE where the existing tuple
  differs from the expected old tuple.
- **Excluded column** — INSERT where a column has no default
  and the row image does not supply it.

Conflicts are reported via the subscription's `pg_stat_subscription`
view and logged, but not automatically resolved.

goopg does not detect or report conflicts.

### Tablesync

Initial table synchronisation uses:

1. **COPY** — the tablesync worker copies the current snapshot
   of the table via the COPY protocol.
2. **Streaming catch-up** — after COPY, the worker switches to
   streaming WAL changes from the slot's position.

The state machine tracks progress via `pg_subscription_rel`:
`i`(init) → `d`(data sync) → `s`(synced) → `r`(ready).

goopg implements the same state machine.

### Replication Slot Origin

Replication slots track not just the WAL position but also the
origin LSN (for logical slots). This allows the subscriber to
request changes from the correct position after a disconnect.

goopg's slots track only the WAL position (RestartLSN).

## goopg Improvement Analysis

### P1: Conflict Detection

Add conflict detection to the apply worker:
- On INSERT duplicate key: log a WARNING and skip the row.
- On UPDATE/DELETE missing tuple: log a WARNING and skip.
- Track conflict counts in pg_stat_subscription.

**Impact:** Production readiness — silent data loss is detected
and reported.

### P2: Origin Tracking

Add an origin ID to replication messages and track the applied
origin LSN in the subscription's catalog entry. Skip changes
whose origin matches the local server's origin ID.

**Impact:** Loop-free bi-directional replication.

## References

- goopg: `internal/wal/pgoutput.go`
- goopg: `internal/server/logicalwalsender.go`
- goopg: `internal/server/logicalreceiver.go`
- PG pgoutput: `postgres/src/backend/replication/pgoutput/pgoutput.c`
- PG apply worker: `postgres/src/backend/replication/logical/worker.c`
- PG parallel apply: `postgres/src/backend/replication/logical/launcher.c`
- PG origin: `postgres/src/backend/replication/logical/origin.c`
