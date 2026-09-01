package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkCaptureSnapshot guards review/260831 TA-1: a snapshot is taken at
// least once per statement, and it used to deep-copy the whole abortedXIDs
// list every time, so the cost of taking a snapshot grew with the number of
// aborts the manager had ever seen. insertSortedXID is copy-on-write now and
// the snapshot shares the slice, so this should stay flat in the abort count.
func BenchmarkCaptureSnapshot(b *testing.B) {
	for _, naborts := range []int{0, 100, 10000} {
		b.Run(abortsLabel(naborts), func(b *testing.B) {
			m := NewManager()
			for i := 1; i <= naborts; i++ {
				m.abortedXIDs = insertSortedXID(m.abortedXIDs, storage.TransactionID(i))
			}
			b.ReportAllocs()
			for b.Loop() {
				s := m.captureSnapshot()
				if len(s.Aborted) != naborts {
					b.Fatalf("Aborted = %d, want %d", len(s.Aborted), naborts)
				}
			}
		})
	}
}

func abortsLabel(n int) string {
	switch n {
	case 0:
		return "aborts=0"
	case 100:
		return "aborts=100"
	default:
		return "aborts=10000"
	}
}
