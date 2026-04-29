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
//   Physical replication / retention (M0005):
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
//   Logical replication (M0008):
//   - apply_commit                subscriber apply worker committed a remote txn
//   - apply_error                 subscriber apply worker hit a fatal per-message error
//   - tablesync_started           per-rel initial-COPY exchange opened
//   - tablesync_completed         per-rel initial-COPY exchange ended (rows, err)
//   - tablesync_state_change      pg_subscription_rel.srsubstate moved (from→to)
//
// The slog field key is always `event`; producers pass additional
// structured context via the standard slog key/value pairs (slot,
// primary, lsn, lag_bytes, sub, rel, from, to, rows, err, etc.).
//
// See docs/design/0005-0007-replication-event-logging.md and
// docs/design/0008-0006-structured-replication-event-logging.md.
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

	// Logical replication (M0008).
	EventApplyCommit          = "apply_commit"
	EventApplyError           = "apply_error"
	EventTablesyncStarted     = "tablesync_started"
	EventTablesyncCompleted   = "tablesync_completed"
	EventTablesyncStateChange = "tablesync_state_change"
)

// LagWarnFraction is the slot-lag fraction of `max_slot_wal_keep_size`
// at which the SlotAwareRetainer emits an EventSlotLagWarning event.
// Picked at 50% so an operator with a long-running query pinning
// hot-standby feedback (or a slow standby) gets a chance to react
// before the slot is invalidated.
const LagWarnFraction = 0.5
