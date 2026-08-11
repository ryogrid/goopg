package control

// M0131-S18 guards for the pg_control writer.
//
// S18.1 — writeControlFileDurably replaced os.WriteFile (O_CREATE|O_TRUNC,
// no fsync) with upstream's O_RDWR overwrite + fsync
// (src/common/controldata_utils.c:216-281). The truncation window is the
// dangerous half: a SIGKILL inside it leaves a zero-length pg_control and PG
// PANICs "could not read file \"global/pg_control\": read 0 of 296".
//
// S18.2 — the nine checkPointCopy members between nextOid and unloggedLSN are
// now decoded AND encoded. Before this slice they survived only as untouched
// bytes in UpdateControlFile's read-modify-write buffer.

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// newTestControlFile writes a syntactically valid 8192-byte pg_control under
// dataDir/global and returns the data directory. Every field the S18.2 struct
// covers gets a distinct value so a dropped or mis-offset field is visible.
func newTestControlFile(t *testing.T) (dataDir string, want *ControlFileData) {
	t.Helper()
	dataDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	buf := make([]byte, pgControlFileSize)
	le := binary.LittleEndian
	le.PutUint64(buf[0:], 0x0123456789ABCDEF) // system_identifier
	le.PutUint32(buf[16:], DBStateInProduction)
	le.PutUint64(buf[24:], 1_700_000_000)
	le.PutUint64(buf[32:], 0x11110000) // checkPoint
	le.PutUint64(buf[40:], 0x11100000) // checkPointCopy.redo
	le.PutUint32(buf[48:], 3)          // ThisTimeLineID
	le.PutUint32(buf[52:], 2)          // PrevTimeLineID
	buf[56] = 1                        // fullPageWrites
	le.PutUint32(buf[60:], 2)          // wal_level = logical
	le.PutUint64(buf[64:], 0x0000000100000ABC)
	le.PutUint32(buf[72:], 24680)  // nextOid
	le.PutUint32(buf[76:], 4242)   // nextMulti
	le.PutUint32(buf[80:], 909091) // nextMultiOffset
	le.PutUint32(buf[84:], 777)    // oldestXid
	le.PutUint32(buf[88:], 16401)  // oldestXidDB
	le.PutUint32(buf[92:], 55)     // oldestMulti
	le.PutUint32(buf[96:], 16402)  // oldestMultiDB
	le.PutUint64(buf[104:], 1_700_000_001)
	le.PutUint32(buf[112:], 601) // oldestCommitTsXid
	le.PutUint32(buf[116:], 602) // newestCommitTsXid
	le.PutUint32(buf[120:], 603) // oldestActiveXid
	le.PutUint64(buf[128:], 0x2000)
	le.PutUint32(buf[252:], 1) // data_checksum_version
	crc := crc32.Checksum(buf[:pgControlCRCOffset], pgCRCTable)
	le.PutUint32(buf[pgControlCRCOffset:], crc)
	if err := os.WriteFile(filepath.Join(dataDir, pgControlFilePath), buf, 0o600); err != nil {
		t.Fatalf("seed pg_control: %v", err)
	}
	return dataDir, decodeControlFileData(buf)
}

// TestUpdateControlFileRoundTripsCheckPointCopy is the S18.2 guard: a no-op
// mutation must leave every checkPointCopy member byte-identical. Dropping any
// one of the nine new encode lines makes the corresponding subtest report a
// zero, which is exactly the corruption PG would see (oldestMulti = 0 trips
// "MultiXactId 0 does not exist" at StartupMultiXact).
func TestUpdateControlFileRoundTripsCheckPointCopy(t *testing.T) {
	dataDir, want := newTestControlFile(t)

	// A no-op mutation still rewrites the whole struct through
	// encodeControlFileData, so this exercises the encode side.
	if err := UpdateControlFile(dataDir, func(cd *ControlFileData) {}); err != nil {
		t.Fatalf("UpdateControlFile: %v", err)
	}
	got, err := ReadControlFile(dataDir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}

	for _, c := range []struct {
		name      string
		got, want uint32
	}{
		{"nextMulti", got.CheckPointCopyNextMulti, want.CheckPointCopyNextMulti},
		{"nextMultiOffset", got.CheckPointCopyNextMultiOffset, want.CheckPointCopyNextMultiOffset},
		{"oldestXid", got.CheckPointCopyOldestXid, want.CheckPointCopyOldestXid},
		{"oldestXidDB", got.CheckPointCopyOldestXidDB, want.CheckPointCopyOldestXidDB},
		{"oldestMulti", got.CheckPointCopyOldestMulti, want.CheckPointCopyOldestMulti},
		{"oldestMultiDB", got.CheckPointCopyOldestMultiDB, want.CheckPointCopyOldestMultiDB},
		{"oldestCommitTsXid", got.CheckPointCopyOldestCommitTsXid, want.CheckPointCopyOldestCommitTsXid},
		{"newestCommitTsXid", got.CheckPointCopyNewestCommitTsXid, want.CheckPointCopyNewestCommitTsXid},
		{"oldestActiveXid", got.CheckPointCopyOldestActiveXid, want.CheckPointCopyOldestActiveXid},
	} {
		// A zero here means either the seed is wrong (test bug) or the
		// DECODE line for this field is missing — in which case the encode
		// side has just written that zero over real data.
		if c.want == 0 {
			t.Fatalf("expected value for %s is zero: seed or decode is broken", c.name)
		}
		if c.got != c.want {
			t.Errorf("checkPointCopy.%s = %d after round-trip, want %d", c.name, c.got, c.want)
		}
	}

	// The system identifier stays decode-only (M0131-S2) and must survive.
	if got.SystemIdentifier != want.SystemIdentifier {
		t.Errorf("system_identifier = %#x, want %#x", got.SystemIdentifier, want.SystemIdentifier)
	}
}

// TestUpdateControlFileWritesCheckPointCopy proves the new fields are settable,
// not merely preserved — S18.4 and S20.4 both need to write them.
func TestUpdateControlFileWritesCheckPointCopy(t *testing.T) {
	dataDir, _ := newTestControlFile(t)
	if err := UpdateControlFile(dataDir, func(cd *ControlFileData) {
		cd.CheckPointCopyNextMulti = 9001
		cd.CheckPointCopyNextMultiOffset = 9002
		cd.CheckPointCopyOldestXid = 9003
		cd.CheckPointCopyOldestXidDB = 9004
		cd.CheckPointCopyOldestMulti = 9005
		cd.CheckPointCopyOldestMultiDB = 9006
		cd.CheckPointCopyOldestCommitTsXid = 9007
		cd.CheckPointCopyNewestCommitTsXid = 9008
		cd.CheckPointCopyOldestActiveXid = 9009
	}); err != nil {
		t.Fatalf("UpdateControlFile: %v", err)
	}
	got, err := ReadControlFile(dataDir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want uint32
	}{
		{"nextMulti", got.CheckPointCopyNextMulti, 9001},
		{"nextMultiOffset", got.CheckPointCopyNextMultiOffset, 9002},
		{"oldestXid", got.CheckPointCopyOldestXid, 9003},
		{"oldestXidDB", got.CheckPointCopyOldestXidDB, 9004},
		{"oldestMulti", got.CheckPointCopyOldestMulti, 9005},
		{"oldestMultiDB", got.CheckPointCopyOldestMultiDB, 9006},
		{"oldestCommitTsXid", got.CheckPointCopyOldestCommitTsXid, 9007},
		{"newestCommitTsXid", got.CheckPointCopyNewestCommitTsXid, 9008},
		{"oldestActiveXid", got.CheckPointCopyOldestActiveXid, 9009},
	} {
		if c.got != c.want {
			t.Errorf("checkPointCopy.%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestWriteControlFileDurablyDoesNotTruncate is the S18.1 guard. It writes a
// sentinel past PG_CONTROL_FILE_SIZE and asserts the update leaves it intact:
// an O_TRUNC writer (the previous os.WriteFile) shortens the file to 8192 and
// erases it. That is the observable proxy for "there is no window in which
// pg_control is zero bytes long".
func TestWriteControlFileDurablyDoesNotTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_control")
	const sentinelLen = 512
	orig := make([]byte, pgControlFileSize+sentinelLen)
	for i := pgControlFileSize; i < len(orig); i++ {
		orig[i] = 0xA5
	}
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	img := make([]byte, pgControlFileSize)
	for i := range img {
		img[i] = 0x5A
	}
	if err := writeControlFileDurably(path, img); err != nil {
		t.Fatalf("writeControlFileDurably: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(after) != len(orig) {
		t.Fatalf("file length = %d after update, want %d (writer truncated)", len(after), len(orig))
	}
	for i := range pgControlFileSize {
		if after[i] != 0x5A {
			t.Fatalf("byte %d = %#x, want 0x5A (image not written)", i, after[i])
		}
	}
	for i := pgControlFileSize; i < len(after); i++ {
		if after[i] != 0xA5 {
			t.Fatalf("sentinel byte %d = %#x, want 0xA5 (writer truncated the tail)", i, after[i])
		}
	}
}

// TestWriteControlFileDurablyRequiresExistingFile pins the no-O_CREATE
// contract: pg_control is created by initdb, never conjured by an update.
// os.WriteFile would have silently created a file here.
func TestWriteControlFileDurablyRequiresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent_pg_control")
	if err := writeControlFileDurably(path, make([]byte, pgControlFileSize)); err == nil {
		t.Fatal("writeControlFileDurably on a missing file: got nil error, want failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("writer created the file: stat err = %v", err)
	}
}

// TestWriteControlFileDurablyRejectsWrongSize keeps the fixed-size invariant
// upstream relies on (PG_CONTROL_FILE_SIZE bytes, zero-padded).
func TestWriteControlFileDurablyRejectsWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg_control")
	if err := os.WriteFile(path, make([]byte, pgControlFileSize), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeControlFileDurably(path, make([]byte, 296)); err == nil {
		t.Fatal("short image: got nil error, want failure")
	}
}
