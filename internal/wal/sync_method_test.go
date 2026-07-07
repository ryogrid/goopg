package wal

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestSyncMethodDefaultsToFdatasync pins that an empty Config.SyncMethod
// (the common case — no caller sets it explicitly yet) resolves to
// "fdatasync" via withDefaults, matching upstream's Linux default
// (postgres/src/include/port/linux.h PLATFORM_DEFAULT_WAL_SYNC_METHOD).
func TestSyncMethodDefaultsToFdatasync(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if got := w.stateRef.cfg.SyncMethod; got != "fdatasync" {
		t.Fatalf("default SyncMethod = %q, want %q", got, "fdatasync")
	}
}

// TestSyncMethodFsyncAndFdatasyncBothFlush exercises both implemented
// wal_sync_method values end-to-end (Append + FlushUpTo + a fresh
// ReadAll re-read from disk), proving doSync's dispatch actually
// persists bytes under either setting rather than silently no-op'ing
// for one of the two branches.
func TestSyncMethodFsyncAndFdatasyncBothFlush(t *testing.T) {
	for _, method := range []string{"fsync", "fdatasync"} {
		t.Run(method, func(t *testing.T) {
			walDir := filepath.Join(t.TempDir(), "pg_wal")
			w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128, SyncMethod: method})
			if err != nil {
				t.Fatal(err)
			}
			if got := w.stateRef.cfg.SyncMethod; got != method {
				t.Fatalf("SyncMethod = %q, want %q", got, method)
			}
			_, end, err := w.Append([]byte("payload"))
			if err != nil {
				t.Fatal(err)
			}
			if err := w.FlushUpTo(end); err != nil {
				t.Fatalf("FlushUpTo under SyncMethod=%s: %v", method, err)
			}
			if got := w.FsyncCount(); got != 1 {
				t.Fatalf("FsyncCount under SyncMethod=%s = %d, want 1", method, got)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			recs, err := ReadAll(walDir, 128)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 1 || string(recs[0].Payload) != "payload" {
				t.Fatalf("ReadAll under SyncMethod=%s = %+v, want one %q record", method, recs, "payload")
			}
		})
	}
}

// TestSyncMethodUnsupportedRejected pins that NewWriter fails fast for
// wal_sync_method values the GUC accepts (for upstream SHOW/pg_settings
// parity — see internal/config/defaults.go's wal_sync_method
// registration) but this build doesn't implement yet, rather than
// silently falling back to fdatasync and masking a config mistake.
func TestSyncMethodUnsupportedRejected(t *testing.T) {
	for _, method := range []string{"open_sync", "open_datasync", "bogus"} {
		t.Run(method, func(t *testing.T) {
			walDir := filepath.Join(t.TempDir(), "pg_wal")
			_, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128, SyncMethod: method})
			if err == nil {
				t.Fatalf("NewWriter with SyncMethod=%s: want error, got nil", method)
			}
			if !errors.Is(err, ErrUnsupportedSyncMethod) {
				t.Fatalf("NewWriter with SyncMethod=%s: err = %v, want wrapping ErrUnsupportedSyncMethod", method, err)
			}
		})
	}
}
