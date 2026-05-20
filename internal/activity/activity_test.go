package activity

import (
	"testing"
)

// procNumOf returns the expected procNum for a numeric PID string.
// Mirrors ActivityRegistry.procNumForPID logic.
func procNumOf(pid uint32) int32 {
	return int32((uint64(pid) - 1) % 1024)
}

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
	// PID "1" → procNum 0.
	r.UpdateState(procNumOf(1), "idle", "SELECT 1")
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
	// Unregistered procNum — must not panic or error.
	r.UpdateState(procNumOf(999), "idle", "SELECT 1")
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
	// PID "1" → procNum 0; WaitEventStart is now an atomic store.
	pn := procNumOf(1)
	r.WaitEventStart(pn, "IO", "AIO")
	snap := r.Snapshot()
	if snap[0].WaitEventType != "IO" {
		t.Errorf("wait_event_type = %q, want IO", snap[0].WaitEventType)
	}
	if snap[0].WaitEvent != "AIO" {
		t.Errorf("wait_event = %q, want AIO", snap[0].WaitEvent)
	}
	r.WaitEventEnd(pn)
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
	// Unregistered procNum — must not panic or error.
	// cold == nil at this slot, so WaitEventStart just does an atomic store
	// and the wait event is ignored by Snapshot (slot has no cold).
	r.WaitEventStart(procNumOf(999), "IO", "test")
	r.WaitEventEnd(procNumOf(999))
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
// SetCurrentGoroutine entries — the pre-M0053-0006 bug let the
// last writer win and shadowed the checkpointer registration.
func TestRegisterCurrentGoroutineIsolatesPerGoroutine(t *testing.T) {
	reg := NewRegistry()
	// Use RegisterBackground for non-numeric PIDs to guarantee distinct slots.
	cpProcNum := reg.RegisterBackground(0, &Backend{PID: "checkpointer-pid", BackendType: "checkpointer", State: "active"})
	clProcNum := reg.RegisterBackground(1, &Backend{PID: "client-pid", BackendType: "client_backend", State: "active"})

	// Goroutine A pretends to be the checkpointer.
	doneA := make(chan string, 1)
	go func() {
		SetCurrentGoroutine(reg, cpProcNum)
		// Don't clear — the checkpointer registration must persist for
		// the entire goroutine lifetime in production.
		_, pid := LookupGoroutine()
		doneA <- pid
	}()

	// Goroutine B pretends to be a connection handler.
	doneB := make(chan string, 1)
	go func() {
		SetCurrentGoroutine(reg, clProcNum)
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
