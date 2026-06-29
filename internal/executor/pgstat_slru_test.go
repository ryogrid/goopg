package executor

import "testing"

// TestSLRUNotifyBlksZeroed verifies the modelled notify-SLRU page-zeroing
// counter: a large queue write crosses page boundaries and bumps blks_zeroed,
// while a zero/negative write is a no-op. M0118-0009 (`stats`, SLRU rung).
func TestSLRUNotifyBlksZeroed(t *testing.T) {
	// Fresh manager so the test is independent of process-global state.
	old := slruStats
	slruStats = &slruStatsManager{}
	defer func() { slruStats = old }()

	if got := slruStats.snapshotAll()["notify"].blksZeroed; got != 0 {
		t.Fatalf("initial notify blks_zeroed = %d, want 0", got)
	}

	RecordNotifyQueueWrite(0)
	if got := slruStats.snapshotAll()["notify"].blksZeroed; got != 0 {
		t.Fatalf("after zero write, blks_zeroed = %d, want 0", got)
	}

	// 3 × ~4 KB entries = ~12 KB from an empty queue → spans pages 0 and 1, plus
	// the first-page zeroing → at least 2 pages zeroed.
	RecordNotifyQueueWrite(3 * 4128)
	first := slruStats.snapshotAll()["notify"].blksZeroed
	if first < 2 {
		t.Fatalf("after 12 KB write, blks_zeroed = %d, want >= 2", first)
	}

	// A second large write strictly increases the counter.
	RecordNotifyQueueWrite(3 * 4128)
	second := slruStats.snapshotAll()["notify"].blksZeroed
	if second <= first {
		t.Fatalf("after second write, blks_zeroed = %d, want > %d", second, first)
	}

	// Non-notify SLRUs stay zero.
	if got := slruStats.snapshotAll()["commit_timestamp"].blksZeroed; got != 0 {
		t.Fatalf("commit_timestamp blks_zeroed = %d, want 0", got)
	}
}
