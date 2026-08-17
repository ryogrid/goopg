package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/utils/activity"
)

// TestBackendGoroutineDoesNotFsync is the M0042-0004 regression test.
//
// It verifies that executing a normal client transaction — including a
// write and a synchronous commit — does NOT call Pool.FlushAll or
// Pool.FlushAllPaced. Those operations are reserved for the checkpointer
// goroutine; a client goroutine must never drive full-buffer I/O directly.
//
// The test wires Pool.OnFlushAll to record any call and its caller's
// registered BackendType, then runs a CREATE TABLE + INSERT sequence as
// a client goroutine. Any FlushAll invocation from a non-checkpointer
// goroutine would constitute an invariant violation.
func TestBackendGoroutineDoesNotFsync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Intercept FlushAll calls and record caller BackendType.
	type flushCall struct {
		backendType string
	}
	var calls []flushCall

	// Override the assertion hook with one that records instead of panicking,
	// so the test can report details rather than crashing.
	// M0107-0005: use LookupCurrentGoroutine (procNum) + GetBackendType (int32).
	rt.Pool.OnFlushAll = func() {
		reg, procNum, ok := activity.LookupCurrentGoroutine()
		bt := ""
		if ok {
			bt = reg.GetBackendType(procNum)
		}
		calls = append(calls, flushCall{backendType: bt})
	}

	// Register the current goroutine as a "client" backend so the activity
	// registry identifies it properly.
	// M0107-0005: use RegisterBackground with idx=2 (beyond WalWriter=0,
	// Checkpointer=1) so this test backend gets a distinct slot.
	act := rt.Activity
	clientPID := "test-client-0"
	var testProcNum int32 = -1
	if act != nil {
		testProcNum = act.RegisterBackground(2, &activity.Backend{
			PID:         clientPID,
			BackendType: "client backend",
			State:       "active",
		})
		activity.SetCurrentGoroutine(act, testProcNum)
		defer func() {
			activity.ClearCurrentGoroutine()
			act.ReleaseBackground(2, clientPID)
		}()
	}

	// Execute a client transaction: CREATE TABLE + commit.
	// This exercises the commit path (synchronous WAL flush) but must
	// NOT trigger Pool.FlushAll.
	conn := newTxnConn(rt, t)
	conn.run("CREATE TABLE goroutine_test (id int4, val text)")

	// Verify no FlushAll was called from a client goroutine.
	for _, c := range calls {
		if c.backendType == "client backend" {
			t.Errorf("Pool.FlushAll called from %q goroutine — M0042-0004 invariant violated", c.backendType)
		}
	}
}

// TestCheckpointerFlushAllIsAllowed verifies that the OnFlushAll hook
// does NOT fire the assertion when called from the checkpointer goroutine.
// This is the happy-path companion to TestBackendGoroutineDoesNotFsync.
func TestCheckpointerFlushAllIsAllowed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Register the current goroutine as a "checkpointer" so it's an
	// allowed caller. M0107-0005: use RegisterBackground(CheckpointerIdx=1).
	act := rt.Activity
	cpPID := "test-checkpointer-0"
	if act != nil {
		cpProcNum := act.RegisterBackground(activity.CheckpointerIdx, &activity.Backend{
			PID:         cpPID,
			BackendType: "checkpointer",
			State:       "active",
		})
		activity.SetCurrentGoroutine(act, cpProcNum)
		defer func() {
			activity.ClearCurrentGoroutine()
			act.ReleaseBackground(activity.CheckpointerIdx, cpPID)
		}()
	}

	// FlushAll from checkpointer should not panic.
	if err := rt.Pool.FlushAll(); err != nil {
		t.Fatalf("FlushAll from checkpointer: %v", err)
	}
}
