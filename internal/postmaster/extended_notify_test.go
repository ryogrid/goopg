package postmaster

import (
	"bytes"
	"net"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// M0132-S12 — LISTEN/NOTIFY/UNLISTEN over the extended query protocol.
//
// The simple path intercepts these three statements at the server layer before
// planning (dispatch.go → execNotifyStmt) because the planner has no node for
// them; before this slice the extended path did not, so Parse/Bind/Execute of a
// LISTEN/NOTIFY/UNLISTEN failed with planner.go:289 "unsupported statement type
// %T" (0A000). These tests pin the fix: the statements execute, a NOTIFY sent
// down the extended path publishes to listeners, and a listener registered down
// the extended path receives notifications at its Sync command boundary.

// notifyPayload decodes a NotificationResponse ('A') frame's payload into its
// (channel, payload) pair. The wire shape is [uint32 backend PID][channel NUL]
// [payload NUL], per WriteNotificationResponse.
func notifyPayload(f libpq.Frame) (channel, payload string, ok bool) {
	if f.Type != libpq.MsgNotificationResponse || len(f.Payload) < 5 {
		return "", "", false
	}
	b := f.Payload[4:] // skip the int32 backend PID
	ch, rest, ok := bytes.Cut(b, []byte{0})
	if !ok {
		return "", "", false
	}
	p, _, ok := bytes.Cut(rest, []byte{0})
	if !ok {
		return "", "", false
	}
	return string(ch), string(p), true
}

// hasNotification reports whether frames contain a NotificationResponse for the
// given channel/payload pair.
func hasNotification(frames []libpq.Frame, channel, payload string) bool {
	for _, f := range frames {
		if ch, p, ok := notifyPayload(f); ok && ch == channel && p == payload {
			return true
		}
	}
	return false
}

// drainSimple runs one statement down the simple path on conn and returns the
// frames up to ReadyForQuery. `SELECT * FROM items` is used (not the `SELECT 1`
// fast path) so the statement reaches dispatch.go's full tail — the point that
// calls deliverNotifications before ReadyForQuery.
func drainSimple(t *testing.T, conn net.Conn, r *libpq.FrameReader) []libpq.Frame {
	t.Helper()
	return simpleStmt(t, conn, r, "SELECT * FROM items")
}

// TestM0132S12_NotifyOverExtendedReachesSimpleListener proves a NOTIFY sent down
// the EXTENDED path publishes to a listener registered on the SIMPLE path. The
// listener drains its inbox at its next command boundary and must see the 'A'
// NotificationResponse.
func TestM0132S12_NotifyOverExtendedReachesSimpleListener(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	lconn := dialAndComplete(t, addr)
	defer lconn.Close()
	lr := extendedReader(t, lconn)
	nconn := dialAndComplete(t, addr)
	defer nconn.Close()
	nr := extendedReader(t, nconn)

	if f := simpleStmt(t, lconn, lr, "LISTEN ch"); hasError(f) {
		t.Fatalf("simple LISTEN errored: %+v", f)
	}
	if f := extendedStmt(t, nconn, nr, "n_notify", "NOTIFY ch, 'hello'"); hasError(f) {
		t.Fatalf("extended NOTIFY errored: %+v", f)
	}
	frames := drainSimple(t, lconn, lr)
	if !hasNotification(frames, "ch", "hello") {
		t.Errorf("listener did not receive NotificationResponse(ch, hello): %+v", frameTypes(frames))
	}
}

// TestM0132S12_ListenOverExtendedReceivesNotification proves a LISTEN sent down
// the EXTENDED path registers the listener, so a NOTIFY from another connection
// reaches it at its Sync command boundary.
func TestM0132S12_ListenOverExtendedReceivesNotification(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	lconn := dialAndComplete(t, addr)
	defer lconn.Close()
	lr := extendedReader(t, lconn)
	nconn := dialAndComplete(t, addr)
	defer nconn.Close()
	nr := extendedReader(t, nconn)

	if f := extendedStmt(t, lconn, lr, "l_listen", "LISTEN ch"); hasError(f) {
		t.Fatalf("extended LISTEN errored: %+v", f)
	}
	// Cross-backend: a simple-path NOTIFY publishes to the extended listener.
	if f := simpleStmt(t, nconn, nr, "NOTIFY ch, 'x'"); hasError(f) {
		t.Fatalf("simple NOTIFY errored: %+v", f)
	}
	// The extended listener drains at its next Sync (the command boundary).
	frames := extendedStmt(t, lconn, lr, "l_drain", "SELECT * FROM items")
	if !hasNotification(frames, "ch", "x") {
		t.Errorf("extended listener did not receive cross-backend NotificationResponse(ch, x): %+v", frameTypes(frames))
	}
}

// TestM0132S12_UnlistenOverExtendedStopsDelivery proves an UNLISTEN sent down the
// EXTENDED path deregisters the listener: a subsequent NOTIFY no longer reaches
// it.
func TestM0132S12_UnlistenOverExtendedStopsDelivery(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	lconn := dialAndComplete(t, addr)
	defer lconn.Close()
	lr := extendedReader(t, lconn)
	nconn := dialAndComplete(t, addr)
	defer nconn.Close()
	nr := extendedReader(t, nconn)

	if f := extendedStmt(t, lconn, lr, "u_listen", "LISTEN ch"); hasError(f) {
		t.Fatalf("extended LISTEN errored: %+v", f)
	}
	if f := extendedStmt(t, lconn, lr, "u_unlisten", "UNLISTEN ch"); hasError(f) {
		t.Fatalf("extended UNLISTEN errored: %+v", f)
	}
	if f := simpleStmt(t, nconn, nr, "NOTIFY ch, 'after-unlisten'"); hasError(f) {
		t.Fatalf("simple NOTIFY errored: %+v", f)
	}
	frames := extendedStmt(t, lconn, lr, "u_drain", "SELECT * FROM items")
	if hasNotification(frames, "ch", "after-unlisten") {
		t.Errorf("listener received NotificationResponse after UNLISTEN: %+v", frameTypes(frames))
	}
}

// TestM0132S12_SelfNotifyOverExtended publishes a NOTIFY to a channel the same
// connection LISTENed on, all over the extended protocol. Out of block the NOTIFY
// Execute is the whole transaction, so the notification must publish at that
// statement's commit and reach the notifier itself at the same command boundary
// — PostgreSQL delivers self-notifications at the ReadyForQuery that closes the
// NOTIFY's own transaction (the async-notify spec relies on this: each step runs
// as one transaction and delivers at its end).
func TestM0132S12_SelfNotifyOverExtended(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := extendedStmt(t, conn, r, "s_listen", "LISTEN ch"); hasError(f) {
		t.Fatalf("extended LISTEN errored: %+v", f)
	}
	frames := extendedStmt(t, conn, r, "s_notify", "NOTIFY ch, 'self'")
	if !hasNotification(frames, "ch", "self") {
		t.Errorf("self-notify did not receive NotificationResponse(ch, self) at its own Sync: %+v", frameTypes(frames))
	}
}
