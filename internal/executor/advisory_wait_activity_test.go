package executor

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestAdvisoryWaitReportsLockAdvisory verifies the Lock:advisory probe around
// the advisoryManager.acquire park (docs/design/not_ralph/
// pg-stat-activity-probes): a goroutine blocked on the ready channel must be
// visible as wait_event_type=Lock / wait_event=advisory, and the event must
// clear when the holder releases.
func TestAdvisoryWaitReportsLockAdvisory(t *testing.T) {
	reg := activity.NewActivityRegistry(16)
	const pn = int32(5)
	reg.RegisterAt(pn, &activity.Backend{PID: "777", BackendType: "client backend", State: "active"})
	activity.SetGlobalRegistry(reg)
	activity.SetCurrentGoroutine(reg, pn)
	defer activity.ClearCurrentGoroutine()

	key := advisoryKey{hi: 1, lo: 2}
	holder := uintptr(0xAAAA)
	if !globalAdvisoryMgr.tryAcquire(key, holder, false, false, false) {
		t.Fatal("holder tryAcquire failed")
	}

	saw := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, b := range reg.Snapshot() {
				if b.PID == "777" && b.WaitEventType == "Lock" && b.WaitEvent == "advisory" {
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
		globalAdvisoryMgr.release(key, holder)
	}()

	if err := globalAdvisoryMgr.acquire(context.Background(), key, uintptr(0xBBBB), false, false, false); err != nil {
		t.Fatalf("acquire returned %v, want nil", err)
	}
	globalAdvisoryMgr.release(key, uintptr(0xBBBB))

	select {
	case <-saw:
	default:
		t.Fatal("acquire completed without Lock/advisory ever being observed")
	}

	for _, b := range reg.Snapshot() {
		if b.PID == "777" && b.WaitEventType != "" {
			t.Fatalf("stale wait event after acquire: %s/%s", b.WaitEventType, b.WaitEvent)
		}
	}
}
