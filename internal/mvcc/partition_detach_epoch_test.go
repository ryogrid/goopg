package mvcc

import "testing"

// TestPartitionDetachEpochMonotonic verifies the global partition-detach epoch
// advances strictly and is observable without advancing (design 0118-0058).
func TestPartitionDetachEpochMonotonic(t *testing.T) {
	base := CurrentPartitionDetachEpoch()
	a := NextPartitionDetachEpoch()
	if a != base+1 {
		t.Fatalf("first Next = %d, want %d", a, base+1)
	}
	if got := CurrentPartitionDetachEpoch(); got != a {
		t.Fatalf("Current = %d, want %d (no advance)", got, a)
	}
	b := NextPartitionDetachEpoch()
	if b != a+1 {
		t.Fatalf("second Next = %d, want %d", b, a+1)
	}
}

// TestCaptureSnapshotRecordsPartitionDetachEpoch confirms a freshly captured
// snapshot carries the current epoch, so the planner can reconstruct a
// snapshot-consistent partition descriptor (design 0118-0058).
func TestCaptureSnapshotRecordsPartitionDetachEpoch(t *testing.T) {
	m := NewManager()
	NextPartitionDetachEpoch()
	want := CurrentPartitionDetachEpoch()
	snap := m.captureSnapshot()
	if snap.PartitionDetachEpoch != want {
		t.Fatalf("snapshot epoch = %d, want %d", snap.PartitionDetachEpoch, want)
	}
	// Clone preserves the epoch.
	if snap.Clone().PartitionDetachEpoch != want {
		t.Fatalf("clone dropped the epoch")
	}
}
