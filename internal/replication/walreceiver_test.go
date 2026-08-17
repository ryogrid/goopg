package replication

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

func TestWalReceiverTrimsOverlappingRawWALData(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "src_pg_wal")
	srcWAL, err := xlog.NewWriter(xlog.Config{
		WALDir:             srcDir,
		SegmentSize:        xlog.DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter src: %v", err)
	}
	defer func() { _ = srcWAL.Close() }()

	if _, _, err := srcWAL.Append([]byte("alpha")); err != nil {
		t.Fatalf("src Append alpha: %v", err)
	}
	_, end, err := srcWAL.Append([]byte("beta"))
	if err != nil {
		t.Fatalf("src Append beta: %v", err)
	}
	if err := srcWAL.FlushUpTo(end); err != nil {
		t.Fatalf("src FlushUpTo: %v", err)
	}
	rawPath := filepath.Join(srcDir, xlog.XLogFileName(1, 0, xlog.DefaultSegmentSize))
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("ReadFile raw WAL: %v", err)
	}
	raw = raw[:srcWAL.WrittenLSN()]
	split := len(raw) / 2
	overlapOff := split / 2

	dstDir := filepath.Join(t.TempDir(), "dst_pg_wal")
	dstWAL, err := xlog.NewWriter(xlog.Config{
		WALDir:             dstDir,
		SegmentSize:        xlog.DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter dst: %v", err)
	}
	defer func() { _ = dstWAL.Close() }()

	rec := &WalReceiver{cfg: WalReceiverConfig{WAL: dstWAL}}
	if err := rec.handleCopyData(libpq.EncodeWALData(0, uint64(split), time.Now(), raw[:split])); err != nil {
		t.Fatalf("handleCopyData first frame: %v", err)
	}
	if err := rec.handleCopyData(libpq.EncodeWALData(uint64(overlapOff), uint64(len(raw)), time.Now(), raw[overlapOff:])); err != nil {
		t.Fatalf("handleCopyData overlapping frame: %v", err)
	}
	if got := dstWAL.WrittenLSN(); got != uint64(len(raw)) {
		t.Fatalf("dst WrittenLSN = %d, want %d", got, len(raw))
	}
	if err := dstWAL.FlushUpTo(dstWAL.WrittenLSN()); err != nil {
		t.Fatalf("dst FlushUpTo: %v", err)
	}
	gotRaw, err := os.ReadFile(filepath.Join(dstDir, xlog.XLogFileName(1, 0, xlog.DefaultSegmentSize)))
	if err != nil {
		t.Fatalf("ReadFile dst WAL: %v", err)
	}
	gotRaw = gotRaw[:len(raw)]
	if string(gotRaw) != string(raw) {
		t.Fatalf("dst raw WAL differs from source after overlap trimming")
	}
}

func TestCheckSSLMode(t *testing.T) {
	for _, mode := range []string{"", "disable", "allow", "prefer", "DISABLE", "  prefer  "} {
		if err := checkSSLMode(mode); err != nil {
			t.Errorf("checkSSLMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		if err := checkSSLMode(mode); err == nil {
			t.Errorf("checkSSLMode(%q) = nil, want an error (goopg has no TLS)", mode)
		}
	}
	if err := checkSSLMode("bogus"); err == nil {
		t.Error(`checkSSLMode("bogus") = nil, want an error (unknown sslmode)`)
	}
}

// TestDialWalReceiverRejectsUnsupportedSSLMode verifies that a
// primary_conninfo requesting sslmode=require is refused before any
// TCP dial happens, rather than silently connecting in plaintext.
// goopg has no TLS implementation, so honouring "require" would mean
// either failing (correct) or lying to the operator about encryption
// (wrong) — DialWalReceiver must choose the former.
func TestDialWalReceiverRejectsUnsupportedSSLMode(t *testing.T) {
	standbyDir := filepath.Join(t.TempDir(), "standby_pg_wal")
	standbyWAL, err := xlog.NewWriter(xlog.Config{WALDir: standbyDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = standbyWAL.Close() }()

	_, err = DialWalReceiver(context.Background(), WalReceiverConfig{
		// Deliberately an address nothing listens on: if
		// DialWalReceiver ever attempted the TCP dial despite the
		// bad sslmode, this test would hang/timeout instead of
		// failing fast, making the regression obvious.
		PrimaryAddr: "127.0.0.1:1",
		WAL:         standbyWAL,
		SSLMode:     "require",
	})
	if err == nil {
		t.Fatal("DialWalReceiver with sslmode=require: got nil error, want rejection (goopg has no TLS)")
	}
}
