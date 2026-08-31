# Streaming Replication Architecture (M0005)

**Status**: accepted
**Milestone**: 0005 (Streaming Replication Support)
**Companion docs (planned)**:
  - `0005-0002-standby-recovery-and-replay.md`
  - `0005-0003-replication-observability.md`

## Goal

Add primary/standby physical streaming replication where WAL
records generated on the primary stream continuously to one or more
standbys and are replayed on each. Correctness and operability
first; performance tuning later. Match upstream PostgreSQL 18.3's
wire-level surface closely enough that an existing standby driver
(`pg_basebackup`, `walreceiver`) could in principle interoperate,
even if v0 only ships a goopg-on-goopg path.

## Process model

```
+-----------------+                     +-------------------+
|     PRIMARY     |   wire connection   |       STANDBY     |
|                 | ------------------> |                   |
|  walsender(s)   |     CopyBoth        |   walreceiver     |
|     ^           |                     |      |            |
|     | reads     |                     |      | applies    |
|  WAL writer     |                     |      v            |
|     ^           |                     |   recovery loop   |
|     | append    |                     |      |            |
|  user backends  |                     |      v            |
|                 |                     |   buffer pool /   |
|                 |                     |   on-disk relations|
+-----------------+                     +-------------------+
```

### Primary side

- **walsender**: per-standby goroutine on the primary. Spun up
  when a client issues a `START_REPLICATION` simple-query
  command. Reads WAL records from the local WAL writer's stream
  and forwards them to the standby via `CopyBoth`.
- **WAL writer**: unchanged from M0002
  (`internal/wal/writer.go`). Already exposes
  `WrittenLSN()` (atomic, lock-free) for poll-based wake-up.
  Walsender uses that to decide when there's new WAL to send.
- **Replication slots**: a small struct on the primary that
  tracks `(slot_name, restart_lsn, confirmed_flush_lsn)` per
  active receiver. Slots prevent WAL removal — see "Retention"
  below.
- **Connection model**: a walsender connection is just a
  regular goopg backend that, after the startup handshake,
  receives a `MsgQuery` whose payload starts with
  `START_REPLICATION`. The dispatcher recognises this and
  hands the connection over to the walsender goroutine
  instead of routing to `dispatchSimpleQueryViaExecutor`.

### Standby side

- **walreceiver**: a separate process model — standby goopg
  starts up in standby mode, opens a libpq-style connection
  to the primary, sends `START_REPLICATION SLOT slot_name
  PHYSICAL N/N`, and reads incoming `CopyData` frames from
  that single long-lived connection.
- **Recovery loop**: the standby applies received WAL records
  by reusing `internal/wal/recovery.go`'s `ReplayRecords`
  kernel — extended for incremental, one-at-a-time apply
  rather than today's bulk-on-startup mode. Detail in
  `0005-0002-standby-recovery-and-replay.md`.
- **Standby mode flag**: presence of a
  `<DataDir>/standby.signal` file at startup tells `goopg
  start` to enter standby mode (mirrors upstream).
  Configuration (`primary_conninfo`, `primary_slot_name`)
  comes from `postgresql.conf` through the existing GUC
  registry.

## Wire protocol

We follow the upstream "v3 protocol replication subset" closely
enough that a future upstream-compatible client could connect:

### Startup

A walsender connection is a normal startup with `replication=true`
in the StartupMessage parameter list. The server parses this in
the existing parameter-bag code path and stores a per-connection
`Replication=true` flag. After handshake the server emits the
usual `AuthenticationOk / S×N / K / Z` reply.

### Replication command

The first `MsgQuery` from a replication-mode connection must be
one of (v0 implements only the bolded subset):

- `IDENTIFY_SYSTEM` → returns `(systemid, timeline, xlogpos,
  dbname)` as a single-row tuple. **Required for v0.**
- `READ_REPLICATION_SLOT slot_name` → returns slot metadata.
- `CREATE_REPLICATION_SLOT slot_name PHYSICAL` → creates a slot.
  **Required for v0.**
- `DROP_REPLICATION_SLOT slot_name` → removes slot.
- **`START_REPLICATION [SLOT slot_name] PHYSICAL <lsn>
  [TIMELINE n]`** → flips connection to streaming mode.
  **Required for v0.**

`BASE_BACKUP` is **deferred**: v0 ships base-snapshot via an
out-of-band `pg_basebackup`-equivalent CLI command rather than
inlining it in the protocol, so the standby setup story is one
clear command. See "Out of scope" below.

### Streaming mode

Once `START_REPLICATION` is accepted, the walsender replies with
`MsgCopyBoth ('W')` and from then on the connection is a
bidirectional CopyBoth channel. Two PG-compatible inner message
types travel inside `CopyData` frames:

- **WAL data ('w')**: header + raw WAL bytes.
  Header (25 bytes): `'w' | startLSN(8) | endLSN(8) | sendTime(8)`
  followed by the record payload.
- **Keepalive ('k')**: `'k' | walEnd(8) | sendTime(8) |
  replyRequested(1)`. Sent every `wal_sender_timeout/2` when
  there's no new WAL, so the receiver can advance its
  apply-progress reporting without waiting on data.

The standby replies with `MsgCopyData` carrying:

- **Standby status update ('r')**: `'r' | writeLSN(8) |
  flushLSN(8) | applyLSN(8) | sendTime(8) | replyRequested(1)`.
  Sent at least every `wal_receiver_status_interval`.
- **Hot-standby feedback ('h')**: deferred — not needed for v0
  since we don't yet do snapshot-aware vacuum coordination.

This is wire-compatible with upstream's
`src/backend/replication/walsender.c` and
`src/backend/replication/walreceiver.c` framing.

### New protocol frame types to add

`internal/protocol/protocol.go` already has `MsgCopyData ('d')`
and `MsgCopyDone ('c')`. Add `MsgCopyBoth ('W')` as a backend
type. The keepalive / WAL-data / status-update inner framings
live inside `CopyData` payloads — no new outer frame types
beyond `CopyBoth`.

## State transitions

```
                            connect
   START_REPLICATION                accepts
   (catalog says
    slot exists)
        |                                ^
        v                                |
    +---------------+   slot OK   +-----------+
    |  Authenticated| ----------> | Streaming |
    +---------------+             +-----------+
                                       |
                       primary down /  | network drop
                       slot dropped    v
                                  +---------------+
                                  |  Disconnected |
                                  +---------------+
                                       |
                       reconnect       | every retry_interval
                       (configurable)  v
                                  +-----------+
                                  | Streaming |  (resume from
                                  +-----------+   last apply LSN)
```

**Standby promotion** (deferred to a follow-up loop but designed
in here for the protocol surface):

- Trigger: `goopg promote -D <dir>` over the existing control
  socket (new `OnPromote` callback in
  `internal/control/control.go`).
- Effect: standby's recovery loop drains all received WAL up
  to the last received LSN, writes an `END_OF_RECOVERY`
  marker, removes `standby.signal`, and switches to primary
  mode. Existing connection is closed.

## Hooks into existing goopg code

This section is the implementation seam list — what each
follow-up loop will touch.

### Wire layer

- `internal/protocol/protocol.go:44-82` — add `MsgCopyBoth`
  byte = `'W'` next to existing `MsgCopyOutResponse`.
- `internal/protocol/messages.go` — add encoders for the
  WAL-data, keepalive, and standby-status payloads.
- `internal/server/server.go:319-396` (`serveConn`) — recognise
  `replication=true` in startup parameters; tag the per-conn
  ctx with `IsReplication`.
- `internal/server/server.go:552-650` (`runPostStartupLoop`) —
  intercept `MsgQuery` for replication commands when
  `IsReplication`; route to a new `handleReplicationCommand`
  in `internal/server/replication.go` (new file).

### Control layer

- `internal/control/control.go:234-277` — add `PROMOTE` command;
  new `OnPromote` callback in `startControlPlane`
  (`server.go:250-266`).
- `cmd/goopg/main.go` — add `goopg promote -D <dir>`
  subcommand that sends `PROMOTE` over the control socket.

### WAL layer

- `internal/wal/reader.go:19-68` — gain a streaming
  `RecordIterator` that yields one record at a time starting
  from a given LSN, blocking when caught up to
  `WrittenLSN()`. Today only `ReadAll` exists.
- `internal/wal/writer.go:71` — add a small subscription
  channel callers can register on so walsender goroutines
  wake on WAL-flush events instead of polling
  `WrittenLSN()`.
- `internal/wal/recovery.go:338-386` (`ReplayRecords`) — split
  the kernel so single-record replay can be invoked outside
  the bulk startup path. Already idempotent via `pd_lsn`
  comparison (lines 474, 509, 541), so resumption from any
  LSN is safe.

### Catalog / state

- `internal/wal/slots.go` (new) — `Slot{Name, RestartLSN,
  ConfirmedFlushLSN, Active}`. Persisted to
  `<DataDir>/pg_replslot/<slot_name>/state` (mirrors upstream
  `slotdata.c`). Loaded at startup; rewritten on
  `CONFIRMED_FLUSH_LSN` updates from standby status replies.
- The WAL writer's segment-removal path (M0002 retention)
  must consult `min(slot.RestartLSN)` before unlinking, with
  `max_slot_wal_keep_size` as the upper bound on retention.

## Replication slot retention semantics

The primary's WAL recycling/retention path (today triggered by
the M0002 `max_wal_size` checkpoint volume rule) needs to honour
slot reservations:

```
oldest_required_lsn = min(checkpoint_lsn,
                          min(slot.RestartLSN  ∀ active slots))
```

Plus the upstream-aligned safety knob `max_slot_wal_keep_size`:
if a slot's lag exceeds this, the slot is **invalidated**
(marked as such, restart_lsn cleared) and WAL recycling
proceeds. The standby reconnecting against an invalidated slot
gets an error and must rebuild from a fresh base backup. v0's
default for this knob mirrors upstream's `-1` (unlimited).

## Configuration surface

GUCs the M0005 implementation will register (consumed in later
loops):

Primary side:
- `wal_level` (`replica` | `minimal`) — must be ≥ `replica` for
  any walsender to start. Default flips to `replica` in v0.
- `max_wal_senders` (default 10).
- `max_replication_slots` (default 10).
- `wal_sender_timeout` (default 60s).
- `max_slot_wal_keep_size` (default -1 / unlimited).

Standby side:
- `primary_conninfo` (libpq DSN).
- `primary_slot_name`.
- `wal_receiver_status_interval` (default 10s).
- `recovery_target_timeline` (default `latest`; v0 supports
  only single-timeline operation, so the value is logged but
  not yet enforced).
- `hot_standby` (default `on` in v0; standby accepts read-only
  queries during replay).

### `primary_conninfo` key parsing (2026-07-08)

`cmd/goopg/main.go`'s `parsePrimaryConninfoFull` parses `host`,
`port`, `application_name`, `user`, and (as of this loop) `sslmode`
out of the libpq-style DSN. `sslmode` is enforced, not just captured:
goopg has no TLS implementation, so `internal/server`'s
`DialWalReceiver` (via `checkSSLMode`) accepts `disable`/`allow`/
`prefer`/unset (all connect in plaintext — the same outcome `prefer`
would negotiate down to against any server that doesn't speak SSL)
and rejects `require`/`verify-ca`/`verify-full` before dialing, rather
than silently connecting in plaintext when the operator asked for
encryption. `password` remains unparsed: the replication handshake
(`WalReceiver.handshake`) never reads an `Authentication*` challenge
from the primary (v0 is trust-only per `WalReceiverConfig.User`'s doc
comment), so there is nowhere for a password to be consumed yet —
tracked as a deferred item pending real replication auth, not silently
dropped.

## Test strategy

The M0005 acceptance test (see milestone DoD #4) is an
end-to-end goopg-on-goopg run that:

1. starts a primary cluster,
2. runs a base-backup CLI to seed a standby data directory,
3. starts the standby pointed at the primary's listen address +
   slot name,
4. issues writes to the primary and observes them on the
   standby (`SELECT count(*) FROM t` matches after the standby
   has caught up),
5. kills the primary, promotes the standby, and confirms the
   promoted node accepts new writes.

The harness lives at `internal/testutil/replcluster/` (mirroring
the existing `internal/testutil/cluster/`). A new design doc
`0005-0003-replication-observability.md` will define the
status-view surface the test queries.

## Out of scope

- `BASE_BACKUP` over the wire protocol. v0 ships base-snapshot
  via a CLI tool that copies data files + reads pg_control;
  full upstream-compatible BASE_BACKUP is a follow-up.
- Logical replication (deferred per milestone definition).
- Synchronous replication / quorum commit (deferred per
  milestone definition).
- Multi-timeline operation. v0 supports a single timeline;
  recovery_target_timeline is accepted but only `latest` is
  honoured.
- Cascading replication (standby-of-standby). Single-hop only.
- Authentication for replication connections beyond what
  regular SQL connections already get (trust / md5 /
  scram-sha-256 from `internal/auth/`).

## References

- Upstream PostgreSQL 18.3:
  `postgres/src/backend/replication/walsender.c`,
  `postgres/src/backend/replication/walreceiver.c`,
  `postgres/src/backend/replication/slot.c`.
- Wire protocol: `postgres/src/include/replication/walprotocol.h`.
- Existing M0002 design docs that this work composes with:
  - `0002-0001-checkpointing.md` (WAL writer + retention
    machinery this layers on top of).
  - `0002-0003-redo-records.md` (logical record kinds the
    standby will replay).
- This milestone: `docs/milestones/0005-streaming-replication-support.md`.
