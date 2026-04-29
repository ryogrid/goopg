//go:build linux

// Linux-only io_uring tests. Skip when the host kernel doesn't
// support io_uring (engine fell back to worker), so this file
// is also safe to run inside containers / restricted CI hosts.

package aio

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEngineIOUringReadWriteRoundTrip drives an actual
// pwrite-then-pread through the io_uring SQ/CQ. Skips when the
// engine fell back to worker (host doesn't honour io_uring) so
// the test passes on every Linux build path. Verifies (a) the
// bytes round-trip, (b) per-direction counters bump, (c) the
// engine reports method=io_uring.
func TestEngineIOUringReadWriteRoundTrip(t *testing.T) {
	e, err := NewEngine(EngineConfig{Method: MethodIOUring, MaxConcurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Method() != MethodIOUring {
		t.Skipf("io_uring unsupported on this host (engine.Method=%q)", e.Method())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "iouring_rw.dat")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if err := f.Truncate(64); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	want := []byte("io_uring works")
	wh := e.Submit(Op{File: f, Buffer: want, Offset: 8, Direction: DirWrite, Target: path})
	if r := wh.Wait(); r.Err != nil || r.N != len(want) {
		t.Fatalf("write Wait=%+v want N=%d nil", r, len(want))
	}

	got := make([]byte, len(want))
	rh := e.Submit(Op{File: f, Buffer: got, Offset: 8, Direction: DirRead, Target: path})
	if r := rh.Wait(); r.Err != nil || r.N != len(want) {
		t.Fatalf("read Wait=%+v want N=%d nil", r, len(want))
	}
	if string(got) != string(want) {
		t.Errorf("read=%q want %q", got, want)
	}

	st := e.Stats()
	if st.Submitted != 2 || st.Completed != 2 {
		t.Errorf("stats=%+v want submitted=2 completed=2", st)
	}
	if st.ReadCompleted != 1 || st.WriteCompleted != 1 {
		t.Errorf("per-direction completed=R%d/W%d want 1/1",
			st.ReadCompleted, st.WriteCompleted)
	}
	if st.InFlight != 0 {
		t.Errorf("InFlight=%d after Waits", st.InFlight)
	}

	// Per-target bytes should equal the bytes written + bytes
	// read (both ops pass len(want) bytes through the kernel).
	tgt := e.PerTarget()
	if len(tgt) != 1 || tgt[0].Target != path {
		t.Fatalf("PerTarget=%+v want one row for %q", tgt, path)
	}
	if tgt[0].Bytes != uint64(2*len(want)) {
		t.Errorf("Target Bytes=%d want %d", tgt[0].Bytes, 2*len(want))
	}
}

// TestEngineIOUringParallel: many concurrent writes against
// the same file, verify io_uring serialises them correctly via
// the per-handle user_data → Handle map. Skips on fallback.
func TestEngineIOUringParallel(t *testing.T) {
	e, err := NewEngine(EngineConfig{Method: MethodIOUring, MaxConcurrency: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Method() != MethodIOUring {
		t.Skipf("io_uring unsupported on this host (engine.Method=%q)", e.Method())
	}

	const ops = 64
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "iouring_par.dat"),
		os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if err := f.Truncate(int64(ops * 4)); err != nil {
		t.Fatal(err)
	}

	handles := make([]*Handle, ops)
	bufs := make([][]byte, ops)
	for i := 0; i < ops; i++ {
		bufs[i] = []byte{byte(i), byte(i >> 8), 0xCA, 0xFE}
		handles[i] = e.Submit(Op{
			File: f, Buffer: bufs[i], Offset: int64(i * 4),
			Direction: DirWrite,
		})
	}
	for i, h := range handles {
		if r := h.Wait(); r.Err != nil || r.N != 4 {
			t.Fatalf("op %d: Wait=%+v", i, r)
		}
	}
	if got := e.Stats().InFlight; got != 0 {
		t.Errorf("InFlight=%d after Waits, want 0", got)
	}

	// Read each block back to confirm no slot collisions.
	for i := 0; i < ops; i++ {
		got := make([]byte, 4)
		rh := e.Submit(Op{File: f, Buffer: got, Offset: int64(i * 4), Direction: DirRead})
		if r := rh.Wait(); r.Err != nil || r.N != 4 {
			t.Fatalf("read %d: Wait=%+v", i, r)
		}
		if got[0] != byte(i) || got[1] != byte(i>>8) || got[2] != 0xCA || got[3] != 0xFE {
			t.Errorf("block %d corrupt: %v", i, got)
		}
	}
}
