package transam

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestSnapshotWaitsReportVirtualXIDLock pins M-NIGHTLY AI-20261020-013101-002:
// a backend parked in WaitForSlotsToCommit (CREATE / DROP / REINDEX INDEX
// CONCURRENTLY's wait for older transactions) or WaitForPinnedSnapshotsReleased
// (DETACH PARTITION CONCURRENTLY's) shows Lock:virtualxid in pg_stat_activity,
// as PG's WaitForOlderSnapshots does by sleeping in VirtualXactLock. Before,
// these waits reported nothing, so the isolation runner could only call such
// a step blocked by elapsed time.
func TestSnapshotWaitsReportVirtualXIDLock(t *testing.T) {
	for name, wait := range map[string]func(m *Manager, slots []int) error{
		"WaitForSlotsToCommit": func(m *Manager, slots []int) error {
			return m.WaitForSlotsToCommit(context.Background(), slots)
		},
		"WaitForPinnedSnapshotsReleased": func(m *Manager, slots []int) error {
			return m.WaitForPinnedSnapshotsReleased(context.Background(), slots)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewManager()
			holder, err := m.Begin(IsolationRepeatableRead, 1)
			if err != nil {
				t.Fatal(err)
			}
			m.procArray.slots[1].pinnedSnap.Store(true)

			reg := activity.NewActivityRegistry(16)
			reg.Register(&activity.Backend{PID: "5", State: "active"})
			const pn = int32(4) // PID "5" → (5-1)%16
			done := make(chan error, 1)
			go func() {
				activity.SetCurrentGoroutine(reg, pn)
				defer activity.ClearCurrentGoroutine()
				done <- wait(m, []int{1})
			}()

			waitEvent := func() (string, string) {
				for _, b := range reg.Snapshot() {
					if b.PID == "5" {
						return b.WaitEventType, b.WaitEvent
					}
				}
				return "", ""
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if typ, ev := waitEvent(); typ == "Lock" && ev == "virtualxid" {
					break
				}
				if time.Now().After(deadline) {
					typ, ev := waitEvent()
					t.Fatalf("waiting backend shows wait_event %q/%q, want Lock/virtualxid", typ, ev)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := m.Commit(holder); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if typ, ev := waitEvent(); typ != "" || ev != "" {
				t.Fatalf("wait_event %q/%q after the wait ended, want none", typ, ev)
			}
		})
	}
}
