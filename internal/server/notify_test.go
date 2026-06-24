package server

import (
	"testing"

	"github.com/goopg/goopg/internal/config"
)

// TestNotifyHub exercises the cross-session LISTEN/NOTIFY exchange logic in
// isolation (no wire/server). M0118-0009 (async-notify).
func TestNotifyHub(t *testing.T) {
	h := newNotifyHub()
	s1 := config.NewSessionRegistry(nil)
	s2 := config.NewSessionRegistry(nil)

	// No listeners: NOTIFY drops on the floor.
	h.Notify("c1", "x", 100)
	if got := h.DrainPending(s1); got != nil {
		t.Fatalf("no-listener notify delivered: %+v", got)
	}

	// s2 listens on c1; a NOTIFY from PID 100 reaches s2 only.
	h.Listen(s2, "c1")
	h.Notify("c1", "hello", 100)
	if got := h.DrainPending(s1); got != nil {
		t.Fatalf("s1 (not listening) received: %+v", got)
	}
	got := h.DrainPending(s2)
	if len(got) != 1 || got[0].Channel != "c1" || got[0].Payload != "hello" || got[0].PID != 100 {
		t.Fatalf("s2 delivery = %+v, want one {100 c1 hello}", got)
	}
	// Drain is destructive.
	if again := h.DrainPending(s2); again != nil {
		t.Fatalf("second drain returned %+v, want nil", again)
	}

	// Self-notification: a session listening on a channel it notifies receives it.
	h.Listen(s1, "self")
	h.Notify("self", "echo", 7)
	if got := h.DrainPending(s1); len(got) != 1 || got[0].Payload != "echo" {
		t.Fatalf("self-notify = %+v, want one echo", got)
	}

	// UNLISTEN stops further delivery.
	h.Unlisten(s2, "c1")
	h.Notify("c1", "after-unlisten", 1)
	if got := h.DrainPending(s2); got != nil {
		t.Fatalf("delivered after UNLISTEN: %+v", got)
	}

	// UnlistenAll + RemoveSession cleanup.
	h.Listen(s2, "a")
	h.Listen(s2, "b")
	h.UnlistenAll(s2)
	h.Notify("a", "p", 1)
	h.Notify("b", "p", 1)
	if got := h.DrainPending(s2); got != nil {
		t.Fatalf("delivered after UNLISTEN *: %+v", got)
	}

	// RemoveSession drops registrations and queued notifications.
	h.Listen(s1, "z")
	h.Notify("z", "queued", 1)
	h.RemoveSession(s1)
	if got := h.DrainPending(s1); got != nil {
		t.Fatalf("RemoveSession left pending: %+v", got)
	}
	h.Notify("z", "after-remove", 1)
	if got := h.DrainPending(s1); got != nil {
		t.Fatalf("RemoveSession left registration: %+v", got)
	}
}

// TestConnTxBufferNotifyDedup verifies the per-transaction de-duplication of
// identical NOTIFYs (matching async.c). M0118-0009.
func TestConnTxBufferNotifyDedup(t *testing.T) {
	c := &connTxState{}
	c.bufferNotify("c", "p", 5)
	c.bufferNotify("c", "p", 5) // exact duplicate — collapsed
	c.bufferNotify("c", "q", 5) // different payload — kept
	c.bufferNotify("d", "p", 5) // different channel — kept
	out := c.takePendingNotify()
	if len(out) != 3 {
		t.Fatalf("buffered %d, want 3: %+v", len(out), out)
	}
	if again := c.takePendingNotify(); again != nil {
		t.Fatalf("take did not clear: %+v", again)
	}
}
