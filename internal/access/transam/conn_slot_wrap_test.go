package transam

import (
	"math"
	"testing"
)

// TestAcquireConnSlotSurvivesCursorWraparound is the review/260831-2 TA-4
// guard. AcquireConnSlot's rotating cursor is a free-running atomic.Int32, so
// after 2^31 cumulative connections it wraps negative — and `1 + (start+off) %
// int32(sz-1)` is then zero or negative, which either hands out the reserved
// slot 0 or panics with a negative index. A long-lived server does reach that
// count.
func TestAcquireConnSlotSurvivesCursorWraparound(t *testing.T) {
	m := NewManager()
	// Park the cursor just below the wrap so the loop below crosses it.
	m.connSlotCursor.Store(math.MaxInt32 - 2)

	for i := 0; i < 8; i++ {
		p, err := m.AcquireConnSlot()
		if err != nil {
			t.Fatalf("acquire %d across the cursor wrap: %v", i, err)
		}
		if p <= 0 || int(p) >= len(m.procArray.slots) {
			t.Fatalf("acquire %d returned slot %d, outside the valid range [1, %d)", i, p, len(m.procArray.slots))
		}
		m.ReleaseConnSlot(p)
	}
}
