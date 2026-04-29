// Structured event names for replication-related logging.
//
// These string constants are the canonical `event=...` slog field
// values for replication lifecycle moments. Centralising them lets
// dashboards (Grafana / Loki) build alerting rules against a stable
// vocabulary instead of grepping for free-form log lines, and keeps
// the producer call sites self-documenting.
//
// Event taxonomy:
//
//   - walreceiver_dial_failed     standby couldn't reach the primary
//   - walreceiver_connected       walreceiver completed handshake
//   - walreceiver_disconnect      walreceiver lost the stream (reconnect follows)
//   - standby_replay_error        replayer hit an unrecoverable per-record error
//   - standby_replay_stopped      replayer exited cleanly (ctx cancel / writer close)
//   - walsender_disconnect        primary's walsender goroutine exited
//   - slot_lag_warning            slot lag passed warn threshold (cap not yet exceeded)
//   - slot_invalidated            slot was forcibly invalidated by lag eviction
//   - wal_segments_recycled       retainer unlinked one or more segment files
//
// The slog field key is always `event`; producers pass additional
// structured context via the standard slog key/value pairs (slot,
// primary, lsn, lag_bytes, err, etc.).
//
// See docs/design/0005-0007-replication-event-logging.md.
package wal

const (
	EventWalreceiverDialFailed = "walreceiver_dial_failed"
	EventWalreceiverConnected  = "walreceiver_connected"
	EventWalreceiverDisconnect = "walreceiver_disconnect"
	EventStandbyReplayError    = "standby_replay_error"
	EventStandbyReplayStopped  = "standby_replay_stopped"
	EventWalsenderDisconnect   = "walsender_disconnect"
	EventSlotLagWarning        = "slot_lag_warning"
	EventSlotInvalidated       = "slot_invalidated"
	EventWALSegmentsRecycled   = "wal_segments_recycled"
)

// LagWarnFraction is the slot-lag fraction of `max_slot_wal_keep_size`
// at which the SlotAwareRetainer emits an EventSlotLagWarning event.
// Picked at 50% so an operator with a long-running query pinning
// hot-standby feedback (or a slow standby) gets a chance to react
// before the slot is invalidated.
const LagWarnFraction = 0.5
