package framework

import (
	"testing"
	"time"
)

// TestAwaitStepOrBlockAsksTheProbe pins the decision rule that replaced the
// 300 ms timing guess (M-NIGHTLY-tuplelock-upgrade-reopen): a step that is
// still running past blockDetectWait is reported blocked only when the
// lock-wait probe says so; a slow step that is not in a lock wait is waited
// for; an unavailable probe falls back to the timing rule.
func TestAwaitStepOrBlockAsksTheProbe(t *testing.T) {
	never := func() (bool, bool) { return false, true }
	lockWait := func() (bool, bool) { return true, true }
	unknown := func() (bool, bool) { return false, false }

	// A slow step (completes at ~2x blockDetectWait) that never waits on a
	// lock completes normally — the old rule labelled it <waiting>.
	slow := make(chan stepOutcome, 1)
	go func() { time.Sleep(2 * blockDetectWait); slow <- stepOutcome{} }()
	if _, completed := awaitStepOrBlock(slow, blockDetectWait, never); !completed {
		t.Errorf("a slow step not in a lock wait was reported blocked")
	}

	// A step in a lock wait is reported blocked at the first probe.
	start := time.Now()
	if _, completed := awaitStepOrBlock(make(chan stepOutcome), blockDetectWait, lockWait); completed {
		t.Errorf("a step in a lock wait was reported completed")
	}
	if d := time.Since(start); d > blockDetectWait+blockProbePoll+500*time.Millisecond {
		t.Errorf("a lock-blocked step took %v to report", d)
	}

	// No probe: the timing rule.
	if _, completed := awaitStepOrBlock(make(chan stepOutcome), blockDetectWait, unknown); completed {
		t.Errorf("an unprobeable running step was reported completed")
	}

	// A step that never finishes and never reports a lock wait is treated
	// as blocked once blockProbeCap passes.
	defer func(prev time.Duration) { blockProbeCap = prev }(blockProbeCap)
	blockProbeCap = 400 * time.Millisecond
	start = time.Now()
	if _, completed := awaitStepOrBlock(make(chan stepOutcome), blockDetectWait, never); completed {
		t.Errorf("a hung step was reported completed")
	}
	if d := time.Since(start); d < blockProbeCap {
		t.Errorf("a hung step without a lock wait was reported blocked after %v, before the %v cap", d, blockProbeCap)
	}

	// A step that finishes before blockDetectWait never consults the probe.
	fast := make(chan stepOutcome, 1)
	fast <- stepOutcome{}
	called := false
	if _, completed := awaitStepOrBlock(fast, blockDetectWait, func() (bool, bool) { called = true; return true, true }); !completed || called {
		t.Errorf("a fast step: completed=%v, probe called=%v", completed, called)
	}
}

// TestSessionWaitIsBlockingMatchesIsolationTester pins the probe's verdict to
// pg_isolation_test_session_is_blocked (lockfuncs.c): a heavyweight lock wait
// or a READ ONLY DEFERRABLE transaction in GetSafeSnapshot (IPC:SafeSnapshot)
// blocks; I/O, LWLock and other IPC waits — a slow COMMIT's WAL flush — and no
// wait at all do not (M-NIGHTLY AI-20261020-013101-002).
func TestSessionWaitIsBlockingMatchesIsolationTester(t *testing.T) {
	for _, c := range []struct {
		typ, event string
		want       bool
	}{
		{"Lock", "transactionid", true},
		{"Lock", "virtualxid", true},
		{"Lock", "tuple", true},
		{"IPC", "SafeSnapshot", true},
		{"IPC", "SyncRep", false},
		{"IO", "WALSync", false},
		{"LWLock", "WALWrite", false},
		{"", "", false},
	} {
		if got := sessionWaitIsBlocking(c.typ, c.event); got != c.want {
			t.Errorf("sessionWaitIsBlocking(%q, %q) = %v, want %v", c.typ, c.event, got, c.want)
		}
	}
}
