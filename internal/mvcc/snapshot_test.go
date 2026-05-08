package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSnapshotHasInProgressLinear exercises the small-N linear-scan
// path (N ≤ snapshotLinearScanThreshold).
func TestSnapshotHasInProgressLinear(t *testing.T) {
	s := Snapshot{InProgress: []storage.TransactionID{10, 20, 30, 40}}
	cases := []struct {
		xid  storage.TransactionID
		want bool
	}{
		{5, false},
		{10, true},
		{15, false},
		{20, true},
		{30, true},
		{35, false},
		{40, true},
		{50, false},
	}
	for _, c := range cases {
		if got := s.HasInProgress(c.xid); got != c.want {
			t.Errorf("HasInProgress(%d) = %v, want %v", c.xid, got, c.want)
		}
	}
}

// TestSnapshotHasInProgressBinary exercises the large-N binary-search
// path (N > snapshotLinearScanThreshold). The InProgress slice is
// sorted ascending per Manager.captureSnapshotLocked.
func TestSnapshotHasInProgressBinary(t *testing.T) {
	const N = 64
	in := make([]storage.TransactionID, 0, N)
	for i := 0; i < N; i++ {
		in = append(in, storage.TransactionID(2*i+10)) // 10, 12, 14, ..., 136
	}
	s := Snapshot{InProgress: in}
	if !s.HasInProgress(10) {
		t.Errorf("HasInProgress(10) = false, want true")
	}
	if !s.HasInProgress(70) {
		t.Errorf("HasInProgress(70) = false, want true")
	}
	if !s.HasInProgress(136) {
		t.Errorf("HasInProgress(136) = false, want true")
	}
	if s.HasInProgress(11) {
		t.Errorf("HasInProgress(11) = true, want false")
	}
	if s.HasInProgress(9) {
		t.Errorf("HasInProgress(9) = true, want false")
	}
	if s.HasInProgress(137) {
		t.Errorf("HasInProgress(137) = true, want false")
	}
}

// BenchmarkSnapshotHasInProgress measures the lookup cost across the
// linear-scan and binary-search regimes. Run with `go test -bench=.`
// in this package.
func BenchmarkSnapshotHasInProgress(b *testing.B) {
	for _, n := range []int{1, 4, 16, 64, 256} {
		in := make([]storage.TransactionID, 0, n)
		for i := 0; i < n; i++ {
			in = append(in, storage.TransactionID(2*i+10))
		}
		s := Snapshot{InProgress: in}
		// Lookup a value that's present at the midpoint — worst case
		// for linear, average for binary.
		target := storage.TransactionID(2*(n/2) + 10)
		b.Run(benchName(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = s.HasInProgress(target)
			}
		})
	}
}

func benchName(n int) string {
	switch n {
	case 1:
		return "N=1"
	case 4:
		return "N=4"
	case 16:
		return "N=16"
	case 64:
		return "N=64"
	case 256:
		return "N=256"
	}
	return "N=?"
}
