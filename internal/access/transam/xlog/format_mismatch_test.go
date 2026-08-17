package xlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeLegacySeedSegment plants a pre-A9 legacy-format segment file in dir:
// an 8-byte length+CRC32-IEEE frame followed by payload bytes. The writer
// that produced this frame was retired in A9, so the bytes are hand-crafted
// here — all that matters for DetectWALFormat is that the first two bytes
// (the legacy length field's low bytes) are not XLOGPageMagic.
func writeLegacySeedSegment(t *testing.T, dir string) {
	t.Helper()
	seed := make([]byte, 72)
	seed[0] = 64 // legacy length field (LE): 64-byte payload
	for i := 8; i < len(seed); i++ {
		seed[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(filepath.Join(dir, formatSegmentName(0)), seed, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNewWriterRejectsLegacyDir pins the A9 fail-fast invariant: the legacy
// (length+CRC32-IEEE) frame is retired, so opening any writer against a data
// dir that still holds legacy WAL must surface ErrWALFormatMismatch instead
// of silently appending pg-compat bytes mid-stream. Re-init is the only
// upgrade path.
func TestNewWriterRejectsLegacyDir(t *testing.T) {
	dir := t.TempDir()
	writeLegacySeedSegment(t, dir)

	_, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if !errors.Is(err, ErrWALFormatMismatch) {
		t.Fatalf("got err=%v, want ErrWALFormatMismatch", err)
	}
}

// TestNewWriterAcceptsFreshDir: an empty data dir is unknown-format and the
// writer claims it.
func TestNewWriterAcceptsFreshDir(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
}

// TestNewWriterAcceptsMatchingFormat: a dir written by the pg-compat writer
// must reopen cleanly. Guards against an over-eager mismatch check.
func TestNewWriterAcceptsMatchingFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{WALDir: dir, SegmentSize: 4096, PageHeaders: true, TimelineID: 1}
	w1, _ := NewWriter(cfg)
	w1.Append([]byte("first"))
	w1.Close()

	w2, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("reopen pgcompat: %v", err)
	}
	w2.Close()
}

// TestWriterFormatExposesActiveMode pins the runtime observability
// surface (M0014 DoD #9): callers can ask the live writer which
// format it's emitting. A9: always WALFormatPGCompat, even for a
// default (PageHeaders unset) Config — normalization forces the
// pg-compat frame on.
func TestWriterFormatExposesActiveMode(t *testing.T) {
	t.Run("default_config", func(t *testing.T) {
		w, err := NewWriter(Config{WALDir: t.TempDir(), SegmentSize: 4096})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if got := w.Format(); got != WALFormatPGCompat {
			t.Errorf("Format() = %v, want WALFormatPGCompat", got)
		}
	})
	t.Run("pgcompat", func(t *testing.T) {
		w, err := NewWriter(Config{
			WALDir:      t.TempDir(),
			SegmentSize: 4096,
			PageHeaders: true,
			TimelineID:  1,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if got := w.Format(); got != WALFormatPGCompat {
			t.Errorf("Format() = %v, want WALFormatPGCompat", got)
		}
	})
}

// TestNewWriterIgnoresNonSegmentFiles guards the rollout-friendly
// behaviour: a stray .gitignore or README in pg_wal/ must not
// prevent the writer from opening (DetectWALFormat returns
// Unknown for dirs holding only non-segment names).
func TestNewWriterIgnoresNonSegmentFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		PageHeaders: true,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("got err=%v, want nil for dir with only non-segment files", err)
	}
	w.Close()
}
