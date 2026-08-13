package server

import (
	"sync"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
)

// notifyHub is the server-wide LISTEN/NOTIFY exchange (M0118-0009, async-notify).
//
// PostgreSQL implements asynchronous notifications in src/backend/commands/
// async.c: a backend registers interest with LISTEN, NOTIFY queues entries that
// become visible to listeners only when the notifying transaction COMMITs, and
// each listening backend is delivered the pending entries at a command boundary
// (ProcessCompletedNotifies, fired before ReadyForQuery / when idle). goopg
// multiplexes every connection in one OS process, so the "queue" is an in-memory
// per-session inbox guarded by a single mutex rather than the SLRU-backed shared
// queue PostgreSQL uses across processes.
//
// Sessions are identified by their stable *config.SessionRegistry — the same
// per-connection identity used as the advisory-lock owner — so registrations and
// pending deliveries survive across the per-statement executor contexts.
type notifyHub struct {
	mu sync.Mutex
	// listeners[channel] is the set of sessions LISTENing on that channel.
	listeners map[string]map[*config.SessionRegistry]struct{}
	// pending[session] is the FIFO of notifications delivered to but not yet
	// drained by that session.
	pending map[*config.SessionRegistry][]Notification
}

// Notification is one delivered LISTEN/NOTIFY message. PID is the backend PID of
// the notifying session (rendered as the int32 of the 'A' NotificationResponse
// and joined to pg_stat_activity by clients).
type Notification struct {
	PID     uint32
	Channel string
	Payload string
}

func newNotifyHub() *notifyHub {
	return &notifyHub{
		listeners: make(map[string]map[*config.SessionRegistry]struct{}),
		pending:   make(map[*config.SessionRegistry][]Notification),
	}
}

// Listen registers sess's interest in channel. Idempotent: a second LISTEN on an
// already-listened channel is a no-op, matching PostgreSQL.
func (h *notifyHub) Listen(sess *config.SessionRegistry, channel string) {
	if sess == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.listeners[channel]
	if set == nil {
		set = make(map[*config.SessionRegistry]struct{})
		h.listeners[channel] = set
	}
	set[sess] = struct{}{}
}

// Unlisten removes sess's registration for channel (no-op if absent).
func (h *notifyHub) Unlisten(sess *config.SessionRegistry, channel string) {
	if sess == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.listeners[channel]; set != nil {
		delete(set, sess)
		if len(set) == 0 {
			delete(h.listeners, channel)
		}
	}
}

// UnlistenAll removes every channel registration for sess (UNLISTEN *).
func (h *notifyHub) UnlistenAll(sess *config.SessionRegistry) {
	if sess == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, set := range h.listeners {
		delete(set, sess)
		if len(set) == 0 {
			delete(h.listeners, ch)
		}
	}
}

// Notify enqueues a notification on channel to every currently-LISTENing
// session, including the notifying session itself when it listens on the
// channel (PostgreSQL delivers self-notifications). PostgreSQL also collapses
// duplicate (channel, payload) notifications emitted by the same transaction;
// goopg performs that de-duplication per publish batch at the call site
// (connTxState.pendingNotify), so Notify itself delivers exactly what it is
// given.
func (h *notifyHub) Notify(channel, payload string, srcPID uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.listeners[channel]
	if len(set) == 0 {
		return
	}
	n := Notification{PID: srcPID, Channel: channel, Payload: payload}
	for sess := range set {
		h.pending[sess] = append(h.pending[sess], n)
	}
}

// DrainPending removes and returns sess's queued notifications in FIFO order.
// Returns nil when none are pending (the common case, so callers can cheaply
// skip writing any 'A' frames).
func (h *notifyHub) DrainPending(sess *config.SessionRegistry) []Notification {
	if sess == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.pending[sess]
	if len(q) == 0 {
		return nil
	}
	delete(h.pending, sess)
	return q
}

// notifyQueueCapacity is the modelled maximum number of undelivered
// notifications the async-notification "queue" can hold, used solely to render
// pg_notification_queue_usage() as an occupied fraction in [0, 1]. PostgreSQL's
// async.c sizes the queue from max_notify_queue_pages × entries-per-SLRU-page;
// goopg keeps the in-memory inbox unbounded, so this constant is a presentation
// denominator only — the exact value is immaterial as long as a single pending
// entry yields a strictly-positive fraction (the property the async-notify spec
// asserts via `pg_notification_queue_usage() > 0`). M0118-0009.
const notifyQueueCapacity = 1 << 20

// QueueUsage returns the fraction of the async-notification queue currently
// occupied by undelivered notifications, in [0, 1] — the engine behind the
// pg_notification_queue_usage() SQL function. It sums the pending (delivered to
// a session's inbox but not yet drained to the client) notifications across all
// listeners and divides by notifyQueueCapacity. Returns 0.0 when nothing is
// pending (the common case). Mirrors PostgreSQL's asyncQueueUsage(). M0118-0009.
func (h *notifyHub) QueueUsage() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, q := range h.pending {
		total += len(q)
	}
	if total == 0 {
		return 0
	}
	usage := float64(total) / float64(notifyQueueCapacity)
	if usage > 1 {
		usage = 1
	}
	return usage
}

// isNotifyStmt reports whether stmt is a LISTEN/NOTIFY/UNLISTEN statement,
// which the dispatch layer routes through execNotifyStmt (and excludes from the
// plan cache, since the planner has no node for them). M0118-0009.
func isNotifyStmt(stmt parser.Stmt) bool {
	switch stmt.(type) {
	case *parser.ListenStmt, *parser.NotifyStmt, *parser.UnlistenStmt:
		return true
	}
	return false
}

// stmtTakesSnapshot reports whether stmt reads MVCC heap data and therefore
// causes PostgreSQL to acquire the transaction's snapshot. Used by the simple-
// query batch loop to pin a REPEATABLE READ / SERIALIZABLE transaction's
// snapshot at the first *data* statement of a batched `BEGIN ISOLATION LEVEL …`
// message (PG-correct timing), while NOT pinning it for a trailing utility
// statement such as SET/SHOW/RESET that takes no snapshot (e.g. a batched
// `BEGIN … SERIALIZABLE; SET debug_parallel_query = on;`). It is an allowlist
// of the data-reading statement kinds: a statement not listed simply falls back
// to the historical lazy pin (at the next separate-message statement), so a
// miss is conservative, never an SSI/RR correctness hazard. Design 0118-0105.
func stmtTakesSnapshot(stmt parser.Stmt) bool {
	switch stmt.(type) {
	case *parser.SelectStmt, *parser.InsertStmt, *parser.UpdateStmt,
		*parser.DeleteStmt, *parser.MergeStmt, *parser.DeclareCursorStmt:
		return true
	}
	return false
}

// execNotifyStmt handles LISTEN/NOTIFY/UNLISTEN at the server layer. Returns
// handled=false for any other statement so the normal planner path runs. LISTEN
// and UNLISTEN take effect immediately (PostgreSQL applies them at commit, but
// in goopg's common autocommit case that is equivalent — transaction-scoped
// LISTEN registration is a deferred refinement). NOTIFY buffers into connTx for
// publication at commit. M0118-0009 (async-notify).
func (s *Server) execNotifyStmt(w *protocol.FrameWriter, stmt parser.Stmt, connTx *connTxState) (bool, error) {
	if tag, handled := s.notifyStmtTag(stmt, connTx); handled {
		return true, w.WriteCommandComplete(tag)
	}
	return false, nil
}

// notifyStmtTag performs the LISTEN/NOTIFY/UNLISTEN server-layer work and
// returns the CommandComplete tag for the statement. handled=false for any
// other statement, leaving it for the normal planner path. It is the
// protocol-agnostic core of execNotifyStmt: the simple path renders the tag as
// a CommandComplete frame (execNotifyStmt), the extended path carries it in an
// extendedQueryResult (dispatch_extended.go). M0132-S12.
func (s *Server) notifyStmtTag(stmt parser.Stmt, connTx *connTxState) (string, bool) {
	switch n := stmt.(type) {
	case *parser.ListenStmt:
		if connTx != nil {
			s.notify.Listen(connTx.NotifySession, n.Channel)
		}
		return "LISTEN", true
	case *parser.NotifyStmt:
		if connTx != nil {
			connTx.bufferNotify(n.Channel, n.Payload, connTx.BackendPID)
		}
		return "NOTIFY", true
	case *parser.UnlistenStmt:
		if connTx != nil {
			if n.All {
				s.notify.UnlistenAll(connTx.NotifySession)
			} else {
				s.notify.Unlisten(connTx.NotifySession, n.Channel)
			}
		}
		return "UNLISTEN", true
	}
	return "", false
}

// publishPendingNotify publishes the NOTIFYs buffered by the just-committed
// transaction to the hub, making them visible to listeners. Called only after a
// successful COMMIT (autocommit or explicit). M0118-0009.
func (s *Server) publishPendingNotify(connTx *connTxState) {
	if connTx == nil {
		return
	}
	pending := connTx.takePendingNotify()
	// Model the async-queue SLRU page traffic: when a committing transaction
	// writes notification entries with at least one listener present, upstream
	// asyncQueueAddEntries() zeroes one or more pg_notify SLRU pages (counted by
	// pg_stat_slru.blks_zeroed). goopg's queue is in-memory, so we report the
	// entries' byte length to the SLRU-stats model, which counts page crossings.
	// Gated on a listener because PostgreSQL only writes to the shared queue when
	// a backend is listening. M0118-0009 (`stats`, SLRU rung).
	if len(pending) > 0 && s.notify != nil && s.notify.hasAnyListener() {
		var queueBytes int64
		for _, n := range pending {
			queueBytes += notifyEntryBytes(n.Channel, n.Payload)
		}
		executor.RecordNotifyQueueWrite(queueBytes)
	}
	for _, n := range pending {
		s.notify.Notify(n.Channel, n.Payload, n.PID)
	}
}

// notifyEntryBytes returns the modelled size of one async-queue entry: the
// fixed AsyncQueueEntry header plus the NUL-terminated channel and payload,
// MAXALIGN'd to 8 bytes — mirroring asyncQueueAddEntries' per-entry advance of
// QUEUE_HEAD. The exact constant is unimportant; only that a large pg_notify
// payload advances the modelled head across SLRU page boundaries. M0118-0009.
func notifyEntryBytes(channel, payload string) int64 {
	const headerBytes = 16 // offsetof(AsyncQueueEntry, data), approximately
	n := headerBytes + len(channel) + 1 + len(payload) + 1
	if rem := n % 8; rem != 0 { // MAXALIGN
		n += 8 - rem
	}
	return int64(n)
}

// hasAnyListener reports whether at least one session is currently LISTENing on
// any channel — the condition under which PostgreSQL writes committed
// notifications to the shared async queue. M0118-0009.
func (h *notifyHub) hasAnyListener() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.listeners {
		if len(set) > 0 {
			return true
		}
	}
	return false
}

// deliverNotifications drains this session's pending notifications and writes one
// 'A' NotificationResponse per entry. Called at a command boundary, just before
// ReadyForQuery — the point at which PostgreSQL delivers queued notifications to
// an otherwise-idle backend. A no-op (no frames) when nothing is pending, which
// is the common case. M0118-0009.
func (s *Server) deliverNotifications(w *protocol.FrameWriter, connTx *connTxState) error {
	if connTx == nil {
		return nil
	}
	for _, n := range s.notify.DrainPending(connTx.NotifySession) {
		if err := w.WriteNotificationResponse(n.PID, n.Channel, n.Payload); err != nil {
			return err
		}
	}
	return nil
}

// RemoveSession drops all of sess's registrations and any undelivered pending
// notifications. Called at connection teardown so a disconnected backend leaves
// no entries behind (PostgreSQL frees the listen state at backend exit).
func (h *notifyHub) RemoveSession(sess *config.SessionRegistry) {
	if sess == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, set := range h.listeners {
		delete(set, sess)
		if len(set) == 0 {
			delete(h.listeners, ch)
		}
	}
	delete(h.pending, sess)
}
