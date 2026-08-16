package xlog

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// TestDetectWALFormatNonexistentDir: a path that doesn't exist
// returns (Unknown, nil) so the caller's fresh-cluster bootstrap
// doesn't have to special-case it.
func TestDetectWALFormatNonexistentDir(t *testing.T) {
	got, err := DetectWALFormat(filepath.Join(t.TempDir(), "no-such-dir"))
	if err != nil {
		t.Fatalf("err = %v, want nil for nonexistent dir", err)
	}
	if got != WALFormatUnknown {
		t.Errorf("got %v, want WALFormatUnknown", got)
	}
}

// TestDetectWALFormatEmptyDir: an existing but empty pg_wal/ ==
// fresh data dir, no WAL produced yet. (Unknown, nil).
func TestDetectWALFormatEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := DetectWALFormat(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != WALFormatUnknown {
		t.Errorf("got %v, want WALFormatUnknown", got)
	}
}

// TestDetectWALFormatIgnoresGarbageFiles: pg_wal/ with a
// `.gitignore` and other non-segment names returns Unknown — no
// false-positive Legacy classification on user-placed files.
func TestDetectWALFormatIgnoresGarbageFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".gitignore", "README.md", "junk.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DetectWALFormat(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != WALFormatUnknown {
		t.Errorf("got %v, want WALFormatUnknown", got)
	}
}

// TestDetectWALFormatLegacy: the legacy format starts every
// segment with an 8-byte length+CRC32-IEEE record frame. Synthesise
// such a frame as the first 40 bytes; detector must classify as
// Legacy. The key property: those bytes don't have XLOGPageMagic at
// offset 0..1, so the page-header decode returns
// ErrInvalidPageHeader and we route to Legacy.
func TestDetectWALFormatLegacy(t *testing.T) {
	dir := t.TempDir()
	// Legacy file name: 24 hex chars, flat counter.
	name := formatSegmentName(0)
	path := filepath.Join(dir, name)

	// Build a representative legacy frame: 4-byte little-endian
	// length, 4-byte CRC32-IEEE, payload bytes.
	payload := []byte("legacy-record-0")
	frame := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(payload))
	copy(frame[8:], payload)
	// Pad to >= 40 bytes so the detector's read succeeds.
	rest := make([]byte, 64)
	if err := os.WriteFile(path, append(frame, rest...), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DetectWALFormat(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != WALFormatLegacy {
		t.Errorf("got %v, want WALFormatLegacy", got)
	}
}

// TestDetectWALFormatPGCompat: a segment whose first 40 bytes
// encode a valid XLogLongPageHeader is classified as PGCompat.
// Uses the M0014-0001 helpers to build the bytes — pins the
// round-trip across detect→encode boundaries.
func TestDetectWALFormatPGCompat(t *testing.T) {
	dir := t.TempDir()
	// PG-compat filename: TLI=1, segno=0, 16 MiB segments.
	name := XLogFileName(1, 0, 16<<20)
	path := filepath.Join(dir, name)

	long := XLogLongPageHeader{
		Std: XLogPageHeader{
			Magic:    XLOGPageMagic,
			TLI:      1,
			PageAddr: 0,
		},
		SysID:      0xDEADBEEFCAFEBABE,
		SegSize:    16 << 20,
		XLogBlcksz: XLOGBlockSize,
	}
	buf := make([]byte, SizeOfXLogLongPHD+24)
	if err := EncodeXLogLongPageHeader(buf[:SizeOfXLogLongPHD], long); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DetectWALFormat(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != WALFormatPGCompat {
		t.Errorf("got %v, want WALFormatPGCompat", got)
	}
}

// TestDetectWALFormatTruncatedSegment: a segment file shorter
// than SizeOfXLogShortPHD (24 bytes) returns a clear error, not
// a panic or silent misclassification.
func TestDetectWALFormatTruncatedSegment(t *testing.T) {
	dir := t.TempDir()
	name := formatSegmentName(0)
	if err := os.WriteFile(filepath.Join(dir, name), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DetectWALFormat(dir)
	if err == nil {
		t.Error("expected error for 3-byte segment, got nil")
	}
}

// TestDetectWALFormatVersionString: log-line readability — pin
// the human-readable name for each variant.
func TestDetectWALFormatVersionString(t *testing.T) {
	cases := []struct {
		v    WALFormatVersion
		want string
	}{
		{WALFormatUnknown, "unknown"},
		{WALFormatLegacy, "legacy"},
		{WALFormatPGCompat, "pgcompat"},
	}
	for _, c := range cases {
		if got := c.v.String(); got != c.want {
			t.Errorf("(%d).String() = %q, want %q", int(c.v), got, c.want)
		}
	}
}
