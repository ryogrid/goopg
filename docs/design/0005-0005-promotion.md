# 0005-0005 — Standby Promotion (M0005)

Status: accepted (2026-04-29)

Milestone: [`0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md)

Predecessors:
- [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  §"Promotion path" — declared the design before this loop.
- [`0005-0002-standby-recovery-and-replay.md`](0005-0002-standby-recovery-and-replay.md)
  — owns the receiver/replayer goroutines this loop ties into.

## Why

Until this loop a goopg standby could stream WAL forever but could
never become a primary. There was no way to fail over: the
`standby.signal` file sat in the data directory forever, the
walreceiver kept dialling the (possibly dead) primary, and any
attempt to write to the standby would route through a path that
assumed a live primary upstream. Operators had to stop the process,
delete `standby.signal` by hand, and start a fresh primary — a
tear-down that loses the very `standby.signal`-driven recovery
sequencing PostgreSQL provides.

This loop closes the gap. A running standby can now be promoted
in-place via `goopg promote -D <dir>` (or directly via the control
socket). The promotion sequence drains in-flight WAL replay before
flipping state, so no replicated record is lost and no half-applied
record is left behind.

## Components

### `internal/control/control.go` — `OnPromote` callback

`Listener` grows an `OnPromote func() error` field next to the
existing OnStop / OnReload / OnCheckpoint hooks. The PROMOTE command
handler:
- Drops the read deadline (promotion can take seconds while WAL
  drains).
- Returns `ERR promote not configured` when `OnPromote` is nil — the
  v0 default for primary servers, so a stray `goopg promote` against
  a primary fails loudly instead of silently no-op'ing.
- Replies `OK` only after the handler returns, so client-side
  `goopg promote` blocks until the promotion is actually durable.

### `internal/server/server.go` — `Promote` seam

`server.Config` grows a `Promote func() error` field. When non-nil,
`startControlPlane` wires it into `cl.OnPromote` so the control
socket can dispatch into whatever policy the caller installed. The
seam keeps the server package agnostic of the receiver/replayer
goroutines: `cmd/goopg start` is the only caller that knows how
those are wired.

### `cmd/goopg/standby.go` — `standbyController`

New file. Owns the receiver + replayer goroutines for the lifetime
of a standby-mode `goopg start`. Constructed via `startStandby`,
which:
- Creates two child contexts (one per goroutine).
- Launches `startWalreceiver` and `startStandbyReplayer`.
- Captures the `*wal.StreamReplayer` so its `ApplyLSN` is observable
  during promotion.

`Promote(ctx)` runs the four-step drain:

1. Cancel the receiver context. No new WAL records can land.
2. Wait for the receiver goroutine to exit.
3. Snapshot the writer's `WrittenLSN` — that's the drain target.
4. Poll the replayer's `ApplyLSN` until it reaches the target, with
   a `drainTimeout` (5s) ceiling and a `drainPollInterval` (10ms)
   cadence. Polling is fine because the replayer wakes itself on
   the writer's flush-event subscription; the poll just observes
   the result.
5. Cancel the replayer, wait, then call
   `initdb.RemoveStandbySignal` and clear `Runtime.Standby`.

Promote is guarded by `sync.Once` + `atomic.Bool` so concurrent
PROMOTE commands from a flapping operator can't half-promote the
runtime: the first call wins, in-flight calls return "already
promoting", and post-completion calls return nil.

`Close()` is the lifecycle counterpart for the SIGTERM/control-STOP
shutdown path: it cancels both contexts (no-op after Promote ran)
and waits for the goroutines to exit before main returns.

### `cmd/goopg/main.go` — wire-up

`startStandbyReplayer` now returns the `*wal.StreamReplayer` it
constructs so `standbyController` can poll its `ApplyLSN`. The
runStart function:
- Builds a `standbyController` only when `rt.Standby` is true.
- Sets `cfg.Promote = boundPromoteToServer(sc)` BEFORE constructing
  the server, so the very first PROMOTE after listener bind has a
  handler.
- Calls `sc.Close()` on shutdown (mirroring the existing checkpointer
  cancel pattern).

### `cmd/goopg/main.go` — `runPromote` CLI

New subcommand. Reads `<DataDir>/postmaster.pid`, sends `PROMOTE`
over the control socket with a generous client-side timeout (default
300s, overridable with `-t`), and surfaces server-side ERR replies
verbatim.

## Tests

`internal/control/control_test.go` adds:
- `TestPromoteDispatch` — PROMOTE routes to OnPromote; reply waits
  for the handler to return.
- `TestPromoteUnconfigured` — server with no OnPromote replies
  `ERR promote not configured`.
- `TestPromoteHandlerError` — handler errors propagate verbatim.

`cmd/goopg/standby_test.go` (new) exercises the controller end-to-
end against a real `initdb`-prepared data directory:
- `TestStandbyControllerPromoteRemovesSignal` — Promote on an idle
  standby removes `standby.signal`, flips `Runtime.Standby` to
  false, and is idempotent.
- `TestStandbyControllerPromoteDrainsPendingReplay` — appending a
  checkpoint marker to the local WAL after the controller starts
  forces the drain loop to actually wait for ApplyLSN to catch up
  before returning.

## Out of scope

- **Demoting a primary back to a standby.** Once promoted, a node
  stays primary until `goopg stop` + `pg_ctl-rewind`-equivalent
  base-backup loop. v0 doesn't ship pg_rewind.
- **Timeline switching.** Upstream bumps the timeline ID on
  promotion so the (possibly still-up) old primary can't write into
  the same WAL byte-stream the new primary is producing. v0 runs
  single-timeline only — this is documented as a hazard in the
  architecture doc and is gated on the multi-timeline work being
  funded.
- **Trigger-file-based promotion.** Upstream's
  `promote_trigger_file` GUC polls a path on disk. v0 only supports
  PROMOTE-over-socket because the polling path adds a knob without
  a clear benefit when an explicit CLI exists.

## Cross-references

- Milestone:
  [`docs/milestones/0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md).
- Architecture (which declared the promotion path):
  [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md).
- Standby recovery (the goroutines this loop drives):
  [`0005-0002-standby-recovery-and-replay.md`](0005-0002-standby-recovery-and-replay.md).
- Slot-aware retention (uses the same Slots seam wired here):
  [`0005-0004-slot-aware-wal-retention.md`](0005-0004-slot-aware-wal-retention.md).
