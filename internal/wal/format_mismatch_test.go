package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNewWriterRejectsLegacyDirInPGCompatMode pins the M0014-0004
// step-2 fail-fast invariant: opening a writer with
// `PageHeaders=true` against a data dir that already holds legacy
// (length+CRC32-IEEE) WAL must surface ErrWALFormatMismatch instead
// of silently appending pg-compat bytes mid-stream.
func TestNewWriterRejectsLegacyDirInPGCompatMode(t *testing.T) {
	dir := t.TempDir()

	// Seed the dir with a legacy-format segment: write through a
	// PageHeaders=false writer first, close, then try to reopen
	// with PageHeaders=true.
	wLegacy, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatalf("seed legacy writer: %v", err)
	}
	// Pad the seed so DetectWALFormat has its full 40-byte read
	// quorum (legacy frame: 8B header + payload). Production
	// segments are preallocated to 16 MiB so this is automatic;
	// tiny test segments need explicit padding.
	bigSeed := make([]byte, 64)
	for i := range bigSeed {
		bigSeed[i] = byte('a' + i%26)
	}
	if _, _, err := wLegacy.Append(bigSeed); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	if err := wLegacy.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	_, err = NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		PageHeaders: true,
		TimelineID:  1,
	})
	if !errors.Is(err, ErrWALFormatMismatch) {
		t.Fatalf("got err=%v, want ErrWALFormatMismatch", err)
	}
}

// TestNewWriterRejectsPGCompatDirInLegacyMode is the symmetric
// guard: a data dir holding pg-compat WAL must not be opened with
// PageHeaders=false.
func TestNewWriterRejectsPGCompatDirInLegacyMode(t *testing.T) {
	dir := t.TempDir()

	// Seed via pg-compat writer.
	wPG, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		PageHeaders: true,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("seed pgcompat writer: %v", err)
	}
	if _, _, err := wPG.Append([]byte("seed-pgcompat")); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	if err := wPG.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	_, err = NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if !errors.Is(err, ErrWALFormatMismatch) {
		t.Fatalf("got err=%v, want ErrWALFormatMismatch", err)
	}
}

// TestNewWriterAcceptsFreshDirInBothModes pins the rollout-friendly
// behaviour: an empty data dir is unknown-format and either mode
// can claim it.
func TestNewWriterAcceptsFreshDirInBothModes(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		dir := t.TempDir()
		w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	})
	t.Run("pgcompat", func(t *testing.T) {
		dir := t.TempDir()
		w, err := NewWriter(Config{
			WALDir:      dir,
			SegmentSize: 4096,
			PageHeaders: true,
			TimelineID:  1,
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	})
}

// TestNewWriterAcceptsMatchingFormat: the same writer mode that
// produced the existing WAL must reopen cleanly. Guards against
// an over-eager mismatch check.
func TestNewWriterAcceptsMatchingFormat(t *testing.T) {
	t.Run("legacy_legacy", func(t *testing.T) {
		dir := t.TempDir()
		w1, _ := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
		w1.Append([]byte("first"))
		w1.Close()

		w2, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
		if err != nil {
			t.Fatalf("reopen legacy: %v", err)
		}
		w2.Close()
	})
	t.Run("pgcompat_pgcompat", func(t *testing.T) {
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
	})
}

// TestWriterFormatExposesActiveMode pins the runtime observability
// surface (M0014 DoD #9): callers can ask the live writer which
// format it's emitting.
func TestWriterFormatExposesActiveMode(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		w, err := NewWriter(Config{WALDir: t.TempDir(), SegmentSize: 4096})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if got := w.Format(); got != WALFormatLegacy {
			t.Errorf("Format() = %v, want WALFormatLegacy", got)
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
