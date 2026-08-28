package xlog

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestStripeInsertWaitReportsLWLockWALInsert pins the WAL-buffer insertion
// probe: when a backend's TryLock on its WAL-insert stripe misses and it
// parks behind another inserter, pg_stat_activity must show
// wait_event_type=LWLock / wait_event=WALInsert for that backend, cleared
// once acquired (upstream: WALInsertLock tranches,
// docs/design/not_ralph/pg-stat-activity-probes/03-design §4 amendment).
func TestStripeInsertWaitReportsLWLockWALInsert(t *testing.T) {
	reg := activity.NewActivityRegistry(16)
	const pn = int32(3)
	reg.RegisterAt(pn, &activity.Backend{PID: "888", BackendType: "client backend", State: "active"})
	activity.SetGlobalRegistry(reg)
	activity.SetCurrentGoroutine(reg, pn)
	defer activity.ClearCurrentGoroutine()

	var locks appendLockSet
	stripe := stripeForProcNum(pn)
	locks.locks[stripe].mu.Lock() // foreign holder keeps the stripe busy

	saw := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, b := range reg.Snapshot() {
				if b.PID == "888" && b.WaitEventType == "LWLock" && b.WaitEvent == "WALInsert" {
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
		locks.locks[stripe].mu.Unlock()
	}()

	lockStripeWithEvent(&locks.locks[stripe])
	locks.locks[stripe].mu.Unlock()

	select {
	case <-saw:
	default:
		t.Fatal("acquired stripe without LWLock/WALInsert ever being observed")
	}

	for _, b := range reg.Snapshot() {
		if b.PID == "888" && b.WaitEventType != "" {
			t.Fatalf("stale wait event after acquire: %s/%s", b.WaitEventType, b.WaitEvent)
		}
	}
}
