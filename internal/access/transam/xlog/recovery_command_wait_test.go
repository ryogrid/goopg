package xlog

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestRecoveryCommandsReportWaitEvents pins pgstat_report_wait_start around
// the recovery shell commands: while restore_command, recovery_end_command
// or archive_cleanup_command runs, the process running it shows wait_event
// RestoreCommand / RecoveryEndCommand / ArchiveCleanupCommand (type IPC) in
// pg_stat_activity, and nothing once the command has exited. goopg reported
// no wait at all, so a checkpointer stuck in a slow archive_cleanup_command
// looked idle.
func TestRecoveryCommandsReportWaitEvents(t *testing.T) {
	cases := []struct {
		event string
		run   func() error
	}{
		{activity.WaitArchiveCleanupCommand, func() error {
			_, err := ExecuteRecoveryCommand("sleep 0.5", "archive_cleanup_command", 0, 1, DefaultSegmentSize)
			return err
		}},
		{activity.WaitRecoveryEndCommand, func() error {
			_, err := ExecuteRecoveryCommand("sleep 0.5", "recovery_end_command", 0, 1, DefaultSegmentSize)
			return err
		}},
		{activity.WaitRestoreCommand, func() error {
			return RestoreArchivedFile("sleep 0.5; touch %p", t.TempDir(), "000000010000000000000001")
		}},
	}
	for _, c := range cases {
		t.Run(c.event, func(t *testing.T) {
			reg := activity.NewActivityRegistry(4)
			pn := reg.RegisterBackground(activity.CheckpointerIdx, &activity.Backend{PID: "cp-0", BackendType: "checkpointer", State: "active"})
			waitOf := func() (string, string) {
				for _, b := range reg.Snapshot() {
					if b.BackendType == "checkpointer" {
						return b.WaitEventType, b.WaitEvent
					}
				}
				t.Fatal("checkpointer missing from the snapshot")
				return "", ""
			}
			done := make(chan error, 1)
			go func() {
				activity.SetCurrentGoroutine(reg, pn)
				defer activity.ClearCurrentGoroutine()
				done <- c.run()
			}()
			seen := false
			for deadline := time.Now().Add(5 * time.Second); !seen && time.Now().Before(deadline); {
				if typ, ev := waitOf(); typ == activity.WaitTypeIPC && ev == c.event {
					seen = true
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Errorf("wait event %s/%s never reported while the command ran", activity.WaitTypeIPC, c.event)
			}
			if typ, ev := waitOf(); ev != "" {
				t.Errorf("wait event %s/%s still set after the command exited", typ, ev)
			}
		})
	}
}
