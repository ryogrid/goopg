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
