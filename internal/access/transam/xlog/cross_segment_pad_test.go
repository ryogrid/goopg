package xlog

import "testing"

// withCrossSegmentPad switches the writers back to the pre-M0149-0017d
// layout (a record that would cross a segment boundary re-landed after an
// XLOG_NOOP pad) for the duration of t: WAL written that way still exists on
// disk, and the readers must keep handling it. The caller must not be a
// parallel test.
func withCrossSegmentPad(t *testing.T) {
	t.Helper()
	prev := crossSegmentPadOn.Swap(true)
	t.Cleanup(func() { crossSegmentPadOn.Store(prev) })
}
