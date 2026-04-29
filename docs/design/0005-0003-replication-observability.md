# 0005-0003 — Replication Observability (M0005)

Status: accepted (2026-04-29)

Milestone: [`0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md)

Predecessors:
- [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  declared the views; this loop ships the surface.
- [`0005-0002-standby-recovery-and-replay.md`](0005-0002-standby-recovery-and-replay.md)
  owns the receiver/replayer this view reflects.
- [`0005-0004-slot-aware-wal-retention.md`](0005-0004-slot-aware-wal-retention.md)
  drives the slot eviction the operator needs to spot in the view.

## Why

Without `pg_stat_replication` and `pg_stat_wal_receiver` an operator
running `goopg` has no SQL-visible way to see whether a standby is
attached, where it sits relative to the primary, or how much WAL the
primary is holding back on its behalf. Every diagnostic question
becomes "scrape the logs" or "attach a debugger." The two views
upstream PostgreSQL ships are the lingua franca of replication
monitoring, so portfolio dashboards and `\watch` muscle memory
already assume their existence.

This loop ships the surface. Existing tools (Grafana exporters,
psql-based runbooks, `pg_lsn_diff(...)` lag math) work without
modification.

## Components

### `internal/wal/replmon.go` — `Senders` + `Receivers` registries

Two lightweight, in-process registries:

- **`Senders`** is a slice-backed pool of `*Sender` handles. Each
  walsender goroutine calls `Senders.Register(SenderState)` on
  entry, gets back a handle, advances `SetSentLSN` after every
  WAL frame and `ApplyStandbyStatus` after every standby status
  reply, then `Senders.Unregister` on exit.
- **`Receivers`** holds at most one `*Receiver` (the standby's own
  walreceiver). Single-slot semantics matter: a reconnect that
  registers a fresh entry first, then has the old goroutine
  unregister its stale handle, must not blank the new entry —
  `Unregister` is a no-op when the supplied handle isn't current.

Both handle types use `atomic.Uint64`/`atomic.Int64` for the LSN
and timestamp fields so `Snapshot` is a lock-free read per entry
(only the registry mutex protects the entry-set membership).

LSN advance is monotonic-clamped via CAS — a stale standby reply
can't push `flush_lsn` backwards. Standby snapshots are sorted by
`(slot_name, pid)` so repeated `SELECT * FROM pg_stat_replication`
returns rows in a stable order (important for `\watch`).

### `internal/initdb/replication_views.go` — catalog wiring

Two new virtual tables:

- `pg_catalog.pg_stat_replication` — column shape mirrors PG 18.x
  (21 columns including `pid`, `application_name`, `client_addr`,
  `state`, `sent_lsn`/`write_lsn`/`flush_lsn`/`replay_lsn`,
  lag intervals, sync state, and `slot_name`). Fields v0 doesn't
  track yet (`backend_xmin` for hot-standby feedback, the lag
  interval columns, `reply_time`) emit either empty strings or
  `00:00:00` placeholders so the column count stays compatible with
  upstream tooling.
- `pg_catalog.pg_stat_wal_receiver` — column shape mirrors PG
  18.x (15 columns). Returns one row when the standby's
  walreceiver is registered, zero rows on a primary node. Single-
  timeline operation means `receive_start_tli` and `received_tli`
  are hard-coded to `1` (consistent with the v0 scope declared in
  `0005-0001`).

LSN columns render through `formatLSN` in upstream's
`XXXXXXXX/XXXXXXXX` hex form (LSN 0 → `0/0`). Timestamps render
through `formatTime` using upstream's
`YYYY-MM-DD HH:MM:SS.mmm-TZ` format so existing dashboards parse
them verbatim.

### `internal/server/server.go` — `WalSenders` seam

`server.Config` grows a `WalSenders *wal.Senders` field. nil
disables registration (a no-op for tests and for any deployment
that doesn't want the view).

### `internal/server/replication.go` — walsender hook

`replyStartReplication` registers a `Sender` on entry to the
streaming loop and unregisters via `defer` on exit (clean or error).
`SetSentLSN` runs after every successful `WriteCopyData`;
`ApplyStandbyStatus` runs inside `handleStandbyCopyData` whenever
the standby sends a status reply.

`client_addr` is currently `""` because the FrameReader doesn't
expose its underlying `net.Conn`. A follow-up loop can plumb the
RemoteAddr through `Server.handleConn`; the column already exists
in the row shape so populating it is a one-line change.

### `internal/server/walreceiver.go` — walreceiver hook

`WalReceiverConfig` grows `Receivers *wal.Receivers` and
`Conninfo string`. `DialWalReceiver` registers an entry once the
handshake + START_REPLICATION succeed; `Close` unregisters. Each
WAL-data frame calls `SetReceivedLSN(end)` and
`MarkMessageReceived(now)`; keepalives only touch the timestamp
(no LSN advance).

### `internal/initdb/open.go` — Runtime + view registration

`Runtime` carries the new `WalSenders` and `WalReceivers` registries
so `cmd/goopg start` can thread them into `server.Config` (sender
side) and `WalReceiverConfig` (receiver side). `Open` calls
`registerStatReplicationView` and `registerStatWalReceiverView`
alongside the existing `pg_stat_checkpointer` registration.

## Tests

`internal/wal/replmon_test.go`:
- `TestSendersRegisterUnregisterRoundTrip` — basic lifecycle.
- `TestSenderLSNAdvanceMonotonic` — stale reply doesn't regress.
- `TestSendersSnapshotSorted` — stable `(slot, pid)` ordering.
- `TestReceiversSingleRow` — reconnect-then-stale-unregister
  preserves the new entry.
- `TestReceiverProgressAdvances` — LSN + timestamp updates.
- `TestSendersConcurrentRegister` — race-checker for the registry
  mutex.

`internal/initdb/replication_views_test.go`:
- `TestStatReplicationRendersRegisteredSenders` — column shape and
  values flow through end-to-end; row vanishes after Unregister.
- `TestStatWalReceiverRendersWhenRegistered` — single-row semantics
  with column-by-column shape check.
- `TestFormatLSN` — pins the upstream hex format.

## Out of scope

- **Replication-related logging hooks for disconnect / replay-pause
  / retention-pressure events.** The retention pressure log already
  fires from `SlotAwareRetainer` (logged at INFO with the
  invalidated slot list), so the most operationally important event
  is covered. The disconnect / replay-pause logs ship in the
  walreceiver / standby controller alongside their existing
  "starting/stopped" entries; surfacing them as structured event
  records (separate from the running slog stream) is a follow-up
  alongside an explicit alert routing layer.
- **`pg_stat_replication.client_addr` population.** The
  walsender path doesn't currently see the connection's
  RemoteAddr — plumbing it through is a small but separate change.
- **`backend_xmin` (hot-standby feedback).** The standby never
  emits feedback today; until it does, the column reads "".
- **Lag interval columns (`write_lag` / `flush_lag` /
  `replay_lag`).** Computing these requires per-record
  send-timestamp tracking; v0 emits `00:00:00` so dashboards can
  still render.
- **`pg_replication_slots` view.** The `Slots` registry already
  contains everything needed; surfacing it as a view is a one-loop
  addition tracked separately.

## Cross-references

- Milestone:
  [`docs/milestones/0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md).
- Architecture (declared the views):
  [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md).
- Catalog mechanism for virtual tables:
  [`root-0017-data-directory.md`](root-0017-data-directory.md).
