package initdb

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage/aio"
)

// checksumAIOFile is a storage.AIOFile that also carries relFile's
// PrepareWrite / VerifyRead hooks, so it stands in for the real relFile
// without dragging a data directory into the test.
type checksumAIOFile struct {
	prepareCalls int
	verifyCalls  int
	verifyErr    error
}

func (f *checksumAIOFile) ReadAt(p []byte, _ int64) (int, error)  { return len(p), nil }
func (f *checksumAIOFile) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }

func (f *checksumAIOFile) PrepareWrite(buf []byte, _ int64) []byte {
	f.prepareCalls++
	out := make([]byte, len(buf))
	copy(out, buf)
	if len(out) > 0 {
		out[len(out)-1] = 0xAB // the "stamped checksum"
	}
	return out
}

func (f *checksumAIOFile) VerifyRead(_ []byte, _ int64) error {
	f.verifyCalls++
	return f.verifyErr
}

// plainAIOFile has no checksum hooks — the *os.File / in-memory case.
type plainAIOFile struct{}

func (plainAIOFile) ReadAt(p []byte, _ int64) (int, error)  { return len(p), nil }
func (plainAIOFile) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }

// TestAIOFileAdapterForwardsChecksumHooks pins the fix for a live defect: the
// io_uring method's raw-fd fast path never calls ReadAt/WriteAt, so it takes
// checksum stamping and verification from aio.ChecksumFile instead — and it
// type-asserts that interface on `op.File`, which is aioFileAdapter, not the
// relFile underneath. Before this test's fix the adapter did not implement
// ChecksumFile, the assertion failed, and every FlushAllPaced /
// checkpointer write on an `io_method = io_uring` cluster with data checksums
// enabled was written with NO checksum stamped.
//
// The values suites cannot catch this: they run at the default
// `io_method = worker`, whose ReadAt/WriteAt path checksums correctly.
func TestAIOFileAdapterForwardsChecksumHooks(t *testing.T) {
	f := &checksumAIOFile{}
	a := aioFileAdapter{f: f}

	// The assertion the io_uring method actually performs.
	cf, ok := any(a).(aio.ChecksumFile)
	if !ok {
		t.Fatal("aioFileAdapter does not satisfy aio.ChecksumFile: the io_uring " +
			"raw-fd path would skip checksum stamping and verification entirely")
	}

	in := []byte{1, 2, 3, 4}
	out := cf.PrepareWrite(in, 0)
	if f.prepareCalls != 1 {
		t.Errorf("PrepareWrite forwarded %d times, want 1", f.prepareCalls)
	}
	if len(out) != len(in) || out[len(out)-1] != 0xAB {
		t.Errorf("PrepareWrite returned %#v, want the wrapped file's stamped copy", out)
	}
	if in[len(in)-1] == 0xAB {
		t.Error("PrepareWrite mutated the caller's buffer; it must return a copy")
	}

	if err := cf.VerifyRead(out, 0); err != nil {
		t.Errorf("VerifyRead forwarded an unexpected error: %v", err)
	}
	if f.verifyCalls != 1 {
		t.Errorf("VerifyRead forwarded %d times, want 1", f.verifyCalls)
	}

	// A mismatch must propagate, or the io_uring read path would accept a
	// corrupt page that the ReadAt path rejects.
	want := errors.New("checksum mismatch")
	f.verifyErr = want
	if got := cf.VerifyRead(out, 0); !errors.Is(got, want) {
		t.Errorf("VerifyRead swallowed the mismatch: got %v, want %v", got, want)
	}
}

// TestAIOFileAdapterChecksumHooksAreInertWithoutThem confirms the forwarding is
// conditional: a wrapped file with no hooks keeps exactly the behaviour it has
// on the ReadAt/WriteAt path — identity stamping and no verification — rather
// than acquiring checksum semantics it never had.
func TestAIOFileAdapterChecksumHooksAreInertWithoutThem(t *testing.T) {
	a := aioFileAdapter{f: plainAIOFile{}}
	cf, ok := any(a).(aio.ChecksumFile)
	if !ok {
		t.Fatal("aioFileAdapter must satisfy aio.ChecksumFile unconditionally")
	}

	in := []byte{9, 8, 7}
	out := cf.PrepareWrite(in, 0)
	if len(out) != len(in) || &out[0] != &in[0] {
		t.Errorf("PrepareWrite on a hookless file must return the caller's slice unchanged")
	}
	if err := cf.VerifyRead(in, 0); err != nil {
		t.Errorf("VerifyRead on a hookless file must no-op, got %v", err)
	}
}
