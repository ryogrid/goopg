package transam

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestWaitForXIDReportsLockTransactionid verifies the pg_stat_activity probe
// added to WaitForXID (docs/design/not_ralph/pg-stat-activity-probes): while
// a goroutine is parked on commitCond waiting for another transaction, its
// registry slot must show wait_event_type=Lock / wait_event=transactionid,
// and the event must be cleared once the wait returns.
//
// The identity resolution runs on the CALLING goroutine (goroutine-ID map in
// tests; pprof labels/gls in the server), so the test goroutine registers
// itself and blocks synchronously while helper goroutines observe the
// snapshot and then commit the holder.
func TestWaitForXIDReportsLockTransactionid(t *testing.T) {
	m := NewManager()

	reg := activity.NewActivityRegistry(16)
	const pn = int32(3)
	reg.RegisterAt(pn, &activity.Backend{PID: "4242", BackendType: "client backend", State: "active"})
	activity.SetGlobalRegistry(reg)
	activity.SetCurrentGoroutine(reg, pn)
	defer activity.ClearCurrentGoroutine()

	txA, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin holder: %v", err)
	}
	xA, err := m.AssignXID(txA)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	if !m.IsXIDActive(xA) {
		t.Fatal("precondition: holder XID should be active")
	}

	saw := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, b := range reg.Snapshot() {
				if b.PID == "4242" && b.WaitEventType == "Lock" && b.WaitEvent == "transactionid" {
					close(saw)
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	go func() {
		select {
		case <-saw:
		case <-time.After(5 * time.Second):
		}
		if err := m.Commit(txA); err != nil {
			t.Errorf("Commit holder: %v", err)
		}
	}()

	waitErr := m.WaitForXID(context.Background(), xA)
	if waitErr != nil {
		t.Fatalf("WaitForXID returned %v, want nil", waitErr)
	}

	select {
	case <-saw:
	default:
		t.Fatal("wait completed without Lock/transactionid ever being observed")
	}

	// Balance check: no stale wait event after return.
	for _, b := range reg.Snapshot() {
		if b.PID == "4242" && b.WaitEventType != "" {
			t.Fatalf("stale wait event after WaitForXID: %s/%s", b.WaitEventType, b.WaitEvent)
		}
	}
}
