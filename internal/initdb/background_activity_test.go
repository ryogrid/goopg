package initdb

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestOpenRegistersDistinctWalWriterAndCheckpointerSlots is the M0122-0003
// writeback-simplification-3 regression test. Before this fix,
// cmd/goopg/main.go registered the checkpointer via act.Register(&Backend{
// PID: "cp-0", ...}) — the regular, numeric-PID-keyed path. procNumForPID
// treats any non-numeric PID as r.bgBase, the exact slot
// RegisterBackground(WalWriterIdx, ...) already claims for the WAL writer,
// so the checkpointer's registration silently clobbered the WAL writer's
// activitySlot (same procNum, last Register call wins). Open now
// pre-registers the checkpointer via RegisterBackground(CheckpointerIdx,
// ...), its own reserved slot, so both background workers coexist.
func TestOpenRegistersDistinctWalWriterAndCheckpointerSlots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	snap := rt.Activity.Snapshot()
	var sawWalWriter, sawCheckpointer bool
	for _, b := range snap {
		switch b.PID {
		case "wal-writer-0":
			sawWalWriter = true
			if b.BackendType != "walwriter" {
				t.Errorf("wal-writer-0 BackendType = %q, want walwriter", b.BackendType)
			}
		case "cp-0":
			sawCheckpointer = true
			if b.BackendType != "checkpointer" {
				t.Errorf("cp-0 BackendType = %q, want checkpointer", b.BackendType)
			}
		}
	}
	if !sawWalWriter {
		t.Error("Activity.Snapshot() has no wal-writer-0 entry — WAL writer slot missing or clobbered")
	}
	if !sawCheckpointer {
		t.Error("Activity.Snapshot() has no cp-0 entry — checkpointer slot missing")
	}
}

// TestOpenRegistersBgwriterBackgroundSlotWhenEnabled verifies the bgwriter
// goroutine gets its own registered background slot (BgwriterIdx) when
// enabled, using upstream's "background writer" backend_type literal
// (pg_stat_activity.backend_type / pg_stat_io, see executor/pgstat_io.go's
// ioBackendTypeNames) — previously the bgwriter had no registered slot at
// all (M0122-0003 writeback simplification 3).
func TestOpenRegistersBgwriterBackgroundSlotWhenEnabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{
		DataDir:          dir,
		PoolSlots:        64,
		BgwriterDelay:    50 * time.Millisecond,
		BgwriterMaxPages: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	snap := rt.Activity.Snapshot()
	var found bool
	for _, b := range snap {
		if b.PID == "bgwriter-0" {
			found = true
			if b.BackendType != "background writer" {
				t.Errorf("bgwriter-0 BackendType = %q, want %q", b.BackendType, "background writer")
			}
		}
	}
	if !found {
		t.Error("Activity.Snapshot() has no bgwriter-0 entry when BgwriterDelay/BgwriterMaxPages are set")
	}
}

// TestCheckpointerWritebackUsesActivityRegistryClock proves
// OnCheckpointerWritebackWait/Done now bracket the real
// ActivityRegistry.WaitEventStart/WaitEventEnd clock (gated by
// LookupTrackedGoroutine, exactly like OnBackendWritebackWait/Done) instead
// of the previous ad-hoc time.Now()/time.Since pair, by driving the hooks
// from the checkpointer's own registered goroutine identity — precisely
// what CheckpointerConfig's OnLoopStart does inside the real Run() loop.
func TestCheckpointerWritebackUsesActivityRegistryClock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64, TrackIOTiming: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if got := rt.Pool.CheckpointWritebackTimeNanos(); got != 0 {
		t.Fatalf("CheckpointWritebackTimeNanos() = %d before any writeback, want 0", got)
	}

	// Simulate what CheckpointerConfig.OnLoopStart does inside Run(): track
	// this goroutine as the pre-registered checkpointer background slot.
	activity.RegisterCurrentGoroutine(rt.Activity, "cp-0")
	defer activity.ClearCurrentGoroutine()

	rt.Pool.OnCheckpointerWritebackWait()
	time.Sleep(2 * time.Millisecond)
	rt.Pool.OnCheckpointerWritebackDone()

	if got := rt.Pool.CheckpointWritebackTimeNanos(); got <= 0 {
		t.Errorf("CheckpointWritebackTimeNanos() = %d after a bracketed wait, want > 0", got)
	}
}

// TestBgwriterWritebackUsesActivityRegistryClock is
// TestCheckpointerWritebackUsesActivityRegistryClock's bgwriter analogue.
func TestBgwriterWritebackUsesActivityRegistryClock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{
		DataDir:       dir,
		PoolSlots:     64,
		TrackIOTiming: true,
		// Disable the real ticker (delay=0 → Bgwriter.run returns
		// immediately) so this test drives the hooks deterministically
		// instead of racing a background tick.
		BgwriterDelay:    0,
		BgwriterMaxPages: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Bgwriter is disabled (delay=0), so its slot was never registered by
	// Open. Register it here the same way Open would, to exercise the
	// Pool hooks in isolation.
	act := rt.Activity
	procNum := act.RegisterBackground(activity.BgwriterIdx, &activity.Backend{
		PID:         "bgwriter-test",
		BackendType: "background writer",
		State:       "active",
	})
	act.UpdateTrackIOTiming(procNum, true)
	defer act.ReleaseBackground(activity.BgwriterIdx, "bgwriter-test")

	activity.SetCurrentGoroutine(act, procNum)
	defer activity.ClearCurrentGoroutine()

	if got := rt.Pool.BgwriterWritebackTimeNanos(); got != 0 {
		t.Fatalf("BgwriterWritebackTimeNanos() = %d before any writeback, want 0", got)
	}

	rt.Pool.OnBgwriterWritebackWait()
	time.Sleep(2 * time.Millisecond)
	rt.Pool.OnBgwriterWritebackDone()

	if got := rt.Pool.BgwriterWritebackTimeNanos(); got <= 0 {
		t.Errorf("BgwriterWritebackTimeNanos() = %d after a bracketed wait, want > 0", got)
	}
}
