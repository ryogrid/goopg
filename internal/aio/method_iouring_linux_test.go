//go:build linux

// Linux-only io_uring tests. Skip when the host kernel doesn't
// support io_uring (engine fell back to worker), so this file
// is also safe to run inside containers / restricted CI hosts.

package aio

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
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

// checksumFile wraps *os.File with a trivial last-byte-XOR
// "checksum" and counts PrepareWrite/VerifyRead calls, so tests
// can prove the io_uring raw-fd path (fdHaver) actually invokes
// the ChecksumFile hooks instead of silently bypassing them —
// which is exactly what storage.relFile needs when data-page
// checksums are enabled (see ChecksumFile's doc comment in
// aio.go).
type checksumFile struct {
	*os.File
	prepareCalls atomic.Int32
	verifyCalls  atomic.Int32
}

func xorChecksum(payload []byte) byte {
	var c byte
	for _, b := range payload {
		c ^= b
	}
	return c
}

func (f *checksumFile) PrepareWrite(buf []byte, off int64) []byte {
	f.prepareCalls.Add(1)
	out := make([]byte, len(buf))
	copy(out, buf)
	if len(out) > 0 {
		out[len(out)-1] = xorChecksum(out[:len(out)-1])
	}
	return out
}

func (f *checksumFile) VerifyRead(buf []byte, off int64) error {
	f.verifyCalls.Add(1)
	if len(buf) == 0 {
		return nil
	}
	if buf[len(buf)-1] != xorChecksum(buf[:len(buf)-1]) {
		return errors.New("checksum mismatch")
	}
	return nil
}

// TestEngineIOUringChecksumFileHooks confirms the io_uring
// method's raw-fd submission calls PrepareWrite before handing
// bytes to the kernel for a write, and VerifyRead after a
// completed read — the two hooks storage.relFile relies on since
// this path bypasses File.WriteAt/ReadAt (and their inline
// checksum stamping/verification) entirely. Without this wiring,
// a checksummed cluster running io_method=io_uring would persist
// stale checksums on every AIO write and skip verification on
// every AIO read.
func TestEngineIOUringChecksumFileHooks(t *testing.T) {
	e, err := NewEngine(EngineConfig{Method: MethodIOUring, MaxConcurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Method() != MethodIOUring {
		t.Skipf("io_uring unsupported on this host (engine.Method=%q)", e.Method())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "iouring_cksum.dat")
	osf, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer osf.Close()
	if err := osf.Truncate(16); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cf := &checksumFile{File: osf}

	// Payload leaves buf's last byte zero; PrepareWrite's copy
	// stamps the real checksum there, so comparing the on-disk
	// last byte against a raw read (bypassing ChecksumFile)
	// proves the *stamped* bytes were what actually got written,
	// not the caller's untouched buf.
	buf := make([]byte, 16)
	copy(buf, "hello io_uring")
	wh := e.Submit(Op{File: cf, Buffer: buf, Offset: 0, Direction: DirWrite})
	if r := wh.Wait(); r.Err != nil || r.N != len(buf) {
		t.Fatalf("write Wait=%+v want N=%d nil", r, len(buf))
	}
	if got := cf.prepareCalls.Load(); got != 1 {
		t.Errorf("PrepareWrite calls=%d want 1", got)
	}
	if buf[15] != 0 {
		t.Fatalf("test invariant broken: buf mutated by PrepareWrite (must return a copy): %v", buf)
	}

	raw := make([]byte, 16)
	if _, err := osf.ReadAt(raw, 0); err != nil {
		t.Fatalf("raw ReadAt: %v", err)
	}
	if raw[15] != xorChecksum(raw[:15]) {
		t.Fatalf("stamped checksum not persisted to disk: %v", raw)
	}

	got := make([]byte, 16)
	rh := e.Submit(Op{File: cf, Buffer: got, Offset: 0, Direction: DirRead})
	if r := rh.Wait(); r.Err != nil || r.N != len(got) {
		t.Fatalf("read Wait=%+v want N=%d nil", r, len(got))
	}
	if got := cf.verifyCalls.Load(); got != 1 {
		t.Errorf("VerifyRead calls=%d want 1", got)
	}

	// Corrupt the on-disk bytes directly (bypassing ChecksumFile,
	// mimicking on-disk bit rot) and confirm the next io_uring
	// read surfaces VerifyRead's mismatch as the Result's error.
	corrupt := append([]byte(nil), raw...)
	corrupt[0] ^= 0xFF
	if _, err := osf.WriteAt(corrupt, 0); err != nil {
		t.Fatalf("corrupt WriteAt: %v", err)
	}
	got2 := make([]byte, 16)
	rh2 := e.Submit(Op{File: cf, Buffer: got2, Offset: 0, Direction: DirRead})
	if r := rh2.Wait(); r.Err == nil {
		t.Error("read of corrupted block: want a checksum-verification error, got nil")
	}
}
