package activity

import (
	"testing"
)

func TestRegisterAndUnregister(t *testing.T) {
	r := NewRegistry()
	r.Register(&Backend{
		PID:   "42",
		State: "active",
	})
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].PID != "42" {
		t.Errorf("PID = %q, want 42", snap[0].PID)
	}
	r.Unregister("42")
	snap = r.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("snapshot len = %d, want 0", len(snap))
	}
}

func TestUpdateState(t *testing.T) {
	r := NewRegistry()
	r.Register(&Backend{PID: "1", State: "active"})
	r.UpdateState("1", "idle", "SELECT 1")
	snap := r.Snapshot()
	if snap[0].State != "idle" {
		t.Errorf("state = %q, want idle", snap[0].State)
	}
	if snap[0].Query != "SELECT 1" {
		t.Errorf("query = %q, want SELECT 1", snap[0].Query)
	}
}

func TestUpdateStateNonExistent(t *testing.T) {
	r := NewRegistry()
	// Must not panic or error.
	r.UpdateState("999", "idle", "SELECT 1")
}

func TestBeginEndTransaction(t *testing.T) {
	r := NewRegistry()
	r.Register(&Backend{PID: "1", State: "active"})
	r.BeginTransaction("1")
	snap := r.Snapshot()
	if snap[0].State != "idle in transaction" {
		t.Errorf("state = %q, want idle in transaction", snap[0].State)
	}
	if snap[0].XactStart == "" {
		t.Error("xact_start is empty after BeginTransaction")
	}
	r.EndTransaction("1")
	snap = r.Snapshot()
	if snap[0].State != "idle" {
		t.Errorf("state = %q, want idle", snap[0].State)
	}
	if snap[0].XactStart != "" {
		t.Error("xact_start should be empty after EndTransaction")
	}
}

func TestWaitEventStartEnd(t *testing.T) {
	r := NewRegistry()
	r.Register(&Backend{PID: "1", State: "active"})
	r.WaitEventStart("1", "IO", "AIO")
	snap := r.Snapshot()
	if snap[0].WaitEventType != "IO" {
		t.Errorf("wait_event_type = %q, want IO", snap[0].WaitEventType)
	}
	if snap[0].WaitEvent != "AIO" {
		t.Errorf("wait_event = %q, want AIO", snap[0].WaitEvent)
	}
	r.WaitEventEnd("1")
	snap = r.Snapshot()
	if snap[0].WaitEventType != "" {
		t.Errorf("wait_event_type = %q, want empty after end", snap[0].WaitEventType)
	}
	if snap[0].WaitEvent != "" {
		t.Errorf("wait_event = %q, want empty after end", snap[0].WaitEvent)
	}
}

func TestWaitEventNonExistent(t *testing.T) {
	r := NewRegistry()
	// Must not panic or error.
	r.WaitEventStart("999", "IO", "test")
	r.WaitEventEnd("999")
}

// TestGoroutineIDProducesDistinctValues verifies the M0053-0006 fix to
// goroutineID(). The pre-fix implementation found the FIRST space in
// the runtime.Stack header — which is INSIDE "goroutine " — and so
// returned a constant value (the "e" / "" suffix of "goroutine") for
// every goroutine. That collapsed the per-goroutine goroutineMap into
// a single shared slot and let any one Register call shadow another.
// This test pins that distinct goroutines get distinct ID strings.
func TestGoroutineIDProducesDistinctValues(t *testing.T) {
	mainID := goroutineID()
	if mainID == "" || mainID == "0" {
		t.Fatalf("goroutineID() returned %q for main goroutine; want a numeric string", mainID)
	}

	// Spawn a child and capture its ID via a channel.
	ch := make(chan string, 1)
	go func() {
		ch <- goroutineID()
	}()
	childID := <-ch

	if childID == "" || childID == "0" {
		t.Fatalf("goroutineID() returned %q for child goroutine; want a numeric string", childID)
	}
	if childID == mainID {
		t.Errorf("main and child goroutines returned the same ID %q — per-goroutine tracking is broken", mainID)
	}
}

// TestRegisterCurrentGoroutineIsolatesPerGoroutine confirms that
// concurrent goroutines do not stomp on each other's
// RegisterCurrentGoroutine entries — the pre-M0053-0006 bug let the
// last writer win and shadowed the checkpointer registration.
func TestRegisterCurrentGoroutineIsolatesPerGoroutine(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&Backend{PID: "checkpointer-pid", BackendType: "checkpointer", State: "active"})
	reg.Register(&Backend{PID: "client-pid", BackendType: "client_backend", State: "active"})

	// Goroutine A pretends to be the checkpointer.
	doneA := make(chan string, 1)
	go func() {
		RegisterCurrentGoroutine(reg, "checkpointer-pid")
		// Don't clear — the checkpointer registration must persist for
		// the entire goroutine lifetime in production.
		_, pid := LookupGoroutine()
		doneA <- pid
	}()

	// Goroutine B pretends to be a connection handler.
	doneB := make(chan string, 1)
	go func() {
		RegisterCurrentGoroutine(reg, "client-pid")
		defer ClearCurrentGoroutine()
		_, pid := LookupGoroutine()
		doneB <- pid
	}()

	gotA := <-doneA
	gotB := <-doneB
	if gotA != "checkpointer-pid" {
		t.Errorf("goroutine A LookupGoroutine returned pid %q, want checkpointer-pid", gotA)
	}
	if gotB != "client-pid" {
		t.Errorf("goroutine B LookupGoroutine returned pid %q, want client-pid", gotB)
	}
}
