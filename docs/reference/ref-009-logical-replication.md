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

## PostgreSQL Implementation

PostgreSQL's logical replication (`pgoutput.c`, `worker.c`) is
significantly more capable:

- **Parallel apply** — PostgreSQL 15+ supports parallel (ordered
  and unordered) apply of transactions. goopg is serial.
- **Two-phase commit** — PostgreSQL 15+ supports two-phase
  transactions in logical replication.
- **Conflict resolution** — PostgreSQL's apply worker detects
  conflicts (e.g., duplicate keys) and reports them; it does not
  automatically resolve them.
- **DDL replication** — not currently supported in upstream either
  (planned for PG 18+).

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Parallel apply | Serial | Parallel ordered/unordered |
| Binary format | Text (JSON-like) | Binary (`pgoutput` protocol) |
| Conflict detection | None | Detected and reported |
| DDL replication | Not implemented | Not yet in upstream |
| Two-phase commit | Not implemented | Supported (PG 15+) |

## References

- goopg: `internal/wal/pgoutput.go`
- goopg: `internal/server/logicalwalsender.go`
- goopg: `internal/server/logicalreceiver.go`
- PG pgoutput: `postgres/src/backend/replication/pgoutput/pgoutput.c`
- PG apply worker: `postgres/src/backend/replication/logical/worker.c`
