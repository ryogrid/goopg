# 0005-0007 — Replication Event Logging (M0005)

Status: accepted (2026-04-29)

Milestone: [`0005-streaming-replication-support.md`](../../milestones/0005-streaming-replication-support.md)

Predecessors:
- [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  declared the operational logging surface.
- [`0005-0003-replication-observability.md`](0005-0003-replication-observability.md)
  ships the SQL-visible counters; this loop ships the structured
  log-visible event surface that complements them.
- [`0005-0004-slot-aware-wal-retention.md`](0005-0004-slot-aware-wal-retention.md)
  is the producer of slot-invalidation and retention-pressure
  events; this loop adds the warning-threshold sweep that fires
  *before* invalidation.

## Why

Before this loop a goopg standby's "I lost the primary" or "I came
back up" was a free-form INFO line scattered across two call sites.
Slot invalidation by lag-eviction logged *after* the slot flipped,
with no early-warning signal. There was no canonical vocabulary for
dashboards to gate alerts on — every ops team would invent their
own grep pattern.

This loop ships the canonical event vocabulary plus an early-warning
sweep so:
- **dashboards** can build alert rules against stable
  `event=<name>` field values, not regex matches against
  free-form messages;
- **operators** get a heads-up event when a slot's lag crosses
  half its `max_slot_wal_keep_size` cap, with enough time to react
  (raise the cap, fix the standby) before the slot is forcibly
  invalidated.

## Components

### `internal/wal/repllog.go` — event vocabulary

A small constants file that names every replication lifecycle
moment. Producers always emit through `slog.Info`/`slog.Warn` with
an `event=<EventXxxxx>` field plus the relevant context (slot,
primary, lsn, lag_bytes, err).

The vocabulary:

| Event                          | Severity | Producer                | When                          |
|--------------------------------|----------|--------------------------|-------------------------------|
| `walreceiver_dial_failed`      | WARN     | `cmd/goopg/main.go`     | dial attempt failed; retrying |
| `walreceiver_connected`        | INFO     | `cmd/goopg/main.go`     | dial + START_REPLICATION ok   |
| `walreceiver_disconnect`       | INFO/WARN| `cmd/goopg/main.go`     | stream ended; reconnect       |
| `standby_replay_error`         | ERROR    | `cmd/goopg/main.go`     | replayer hit per-record error |
| `standby_replay_stopped`       | INFO     | `cmd/goopg/main.go`     | replayer exited cleanly       |
| `walsender_disconnect`         | INFO     | `internal/server/...`   | walsender goroutine exited    |
| `slot_lag_warning`             | INFO     | `SlotAwareRetainer`     | lag > LagWarnFraction*cap     |
| `slot_invalidated`             | WARN     | `SlotAwareRetainer`     | slot evicted by lag cap       |
| `wal_segments_recycled`        | INFO     | `SlotAwareRetainer`     | one+ segments unlinked        |

Severity levels are picked so a typical ops dashboard can route:
- INFO → log only.
- WARN → page on burst (multiple disconnects, slot eviction).
- ERROR → page immediately (replay halted, data divergence).

### `internal/wal/retention.go` — lag warning sweep

`SlotAwareRetainer.Retain` now runs a `warnLaggingSlots` pass
before the invalidation sweep. For every non-invalidated slot whose
lag exceeds `LagWarnFraction × MaxSlotKeepBytes` (default 50%) but
is still under the cap, an `event=slot_lag_warning` INFO log fires
with the slot name, lag in bytes, the cap, and the warn fraction.
Slots that are about to be invalidated in the same call are
skipped — the WARN log from the post-invalidation per-slot loop
covers them, no double-logging.

The 50% threshold is opinionated but documented: at half-full an
operator typically still has time to either raise
`max_slot_wal_keep_size` or unblock the standby (kill a long
read, rebuild from base backup). Tuning this would mean adding
another GUC; the constant is fine for v0 and easy to extract later
if requested.

### `cmd/goopg/main.go` — structured walreceiver / replayer events

`startWalreceiver` and `startStandbyReplayer` now stamp every
lifecycle log with the appropriate `event=` field. The
walreceiver's connect log is new — previously the only signal a
standby was actually streaming was the "starting walreceiver" line
(which fires before the dial). Now there's an explicit
`walreceiver_connected` event that proves the handshake
succeeded.

### `internal/server/replication.go` — walsender disconnect event

`replyStartReplication` now defers an `event=walsender_disconnect`
INFO log that fires on every exit path (clean stream end,
keepalive write failure, ctx cancel) with the slot name, the
walsender's start LSN, and the primary's current `WrittenLSN` at
disconnect time. Operators tracking standby churn have a single
event name to count without grepping for arbitrary strings.

## Tests

`internal/wal/retention_test.go`:
- `TestSlotAwareRetainerEmitsLagWarning` — slot whose lag has
  crossed `LagWarnFraction×cap` but not the cap itself emits an
  `event=slot_lag_warning` log without being invalidated.

The existing `TestSlotAwareRetainerInvalidatesAndPrunes` test
already covers the post-invalidation `event=slot_invalidated`
WARN path (visible in the test output's captured slog).

A dedicated logCapture helper (in retention_test.go) decodes
JSON-encoded slog records so future event tests can assert on
field values without depending on text formatting.

## Out of scope

- **Per-event rate limiting / dedup.** Walreceiver disconnect storms
  during a primary outage will produce one event per reconnect
  attempt. v0 emits each one; aggregating into "N disconnects in
  M seconds" needs a separate observability layer (Prometheus
  counter, etc.) that's outside the core server.
- **Configurable LagWarnFraction.** Hard-coded at 50% in v0. A
  future loop can promote it to a GUC if operators want
  per-cluster tuning.
- **Promotion event log.** `goopg promote` already logs through
  the existing `slog` calls in `standbyController.Promote`; an
  explicit `event=promotion_started` / `promotion_completed`
  pair is a follow-up alongside post-promote timeline switching.
- **Hot-standby feedback events.** No feedback channel in v0, so
  no event vocabulary needed.

## Cross-references

- Milestone:
  [`docs/milestones/0005-streaming-replication-support.md`](../../milestones/0005-streaming-replication-support.md).
- Architecture:
  [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md).
- Slot-aware retention (the producer of two of the new events):
  [`0005-0004-slot-aware-wal-retention.md`](0005-0004-slot-aware-wal-retention.md).
- SQL-visible counterpart:
  [`0005-0003-replication-observability.md`](0005-0003-replication-observability.md).
