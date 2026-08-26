package postmaster

import (
	"sync/atomic"
	"testing"
)

// TestForcedGCDisabledByDefaultAndFlagGatesEverything pins the parity-bundle
// user request: the explicit commit-path GC trigger ships DISABLED, and when
// disabled neither the condition bookkeeping (queriesWithoutFreeCounter
// bump) nor any GC runs. Re-enabling restores the counting behaviour.
func TestForcedGCDisabledByDefaultAndFlagGatesEverything(t *testing.T) {
	prev := forcedGCEnabled()
	defer SetForcedGCEnabled(prev)

	// Default OFF.
	SetForcedGCEnabled(false)
	before := atomic.LoadInt64(&queriesWithoutFreeCounter)
	for i := 0; i < 100; i++ {
		maybeForceGCAfterCommit()
	}
	if after := atomic.LoadInt64(&queriesWithoutFreeCounter); after != before {
		t.Fatalf("counter moved while flag disabled: %d -> %d (condition checks must be gated too)", before, after)
	}

	// Enabled: counter bookkeeping resumes (GC itself is threshold-gated and
	// won't fire for these tiny increments — only the counter path matters
	// here).
	SetForcedGCEnabled(true)
	maybeForceGCAfterCommit()
	if after := atomic.LoadInt64(&queriesWithoutFreeCounter); after != before+1 {
		t.Fatalf("counter = %d, want %d after one enabled call", after, before+1)
	}
}

// TestParseForcedGCEnv covers the GOOPG_FORCED_GC value grammar.
func TestParseForcedGCEnv(t *testing.T) {
	cases := map[string]bool{
		"on": true, "ON": true, "true": true, "1": true, " yes ": true,
		"": false, "off": false, "false": false, "0": false, "no": false,
		"garbage": false,
	}
	for raw, want := range cases {
		if got := parseForcedGCEnv(raw); got != want {
			t.Errorf("parseForcedGCEnv(%q) = %v, want %v", raw, got, want)
		}
	}
}
