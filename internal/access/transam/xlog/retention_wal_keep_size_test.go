package xlog

import "testing"

// TestApplyWalKeepSize is KeepLogSeg's wal_keep_size clamp with 16 MB
// segments: wal_keep_size = 64 MB keeps 4 segments behind the checkpoint
// record's segment, never pulls the horizon forward, and stops at segment 1.
// 026_overwrite_contrecord sets wal_keep_size = 1GB so the standby made from
// a cold copy still finds the primary's WAL. M0149-0017.
func TestApplyWalKeepSize(t *testing.T) {
	const seg = 16 << 20
	w, err := NewWriter(Config{WALDir: t.TempDir(), SegmentSize: seg})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r := &SlotAwareRetainer{Writer: w, KeepMB: 64}
	cases := []struct {
		name            string
		recptr, keep    uint64
		want            uint64
	}{
		{"pulled back to currSeg-4", 10*seg + 100, 10*seg + 100, 6 * seg},
		{"already older stays", 10*seg + 100, 2 * seg, 2 * seg},
		{"floor at segment 1", 3*seg + 5, 3*seg + 5, 1 * seg},
	}
	for _, c := range cases {
		if got := r.applyWalKeepSize(c.recptr, c.keep); got != c.want {
			t.Errorf("%s: keep %#x, want %#x", c.name, got, c.want)
		}
	}
	r.KeepMB = 0
	if got := r.applyWalKeepSize(10*seg, 10*seg); got != 10*seg {
		t.Errorf("wal_keep_size=0 moved the horizon to %#x", got)
	}
}
