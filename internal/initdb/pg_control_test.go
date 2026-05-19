package initdb

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/config"
)

// TestBuildPgControlCheckpointFields verifies that buildPgControl fills the
// checkPoint and checkPointCopy fields with the correct initdb-time values
// from BootStrapXLOG (xlog.c:5115-5132). Spec: bootstrap-procedure/02.
//
// Offsets are for the x86_64 ControlFileData layout from pg_control.h.
func TestBuildPgControlCheckpointFields(t *testing.T) {
	const sysID = uint64(0xABCDEF0123456789)
	before := time.Now()
	buf := buildPgControl(sysID, before, nil)
	after := time.Now()

	le := binary.LittleEndian

	// checkPoint (XLogRecPtr): offset 32
	// Expected: wal_segment_size(16MiB=0x01000000) + SizeOfXLogLongPHD(40=0x28)
	checkPoint := le.Uint64(buf[32:])
	if want := pgInitCheckpointLSN; checkPoint != want {
		t.Errorf("checkPoint at offset 32: got 0x%016X, want 0x%016X", checkPoint, want)
	}

	// checkPointCopy starts at offset 40, 88 bytes total.
	// CheckPoint.redo (uint64) at offset 40
	redo := le.Uint64(buf[40:])
	if want := pgInitCheckpointLSN; redo != want {
		t.Errorf("checkPointCopy.redo at offset 40: got 0x%016X, want 0x%016X", redo, want)
	}

	// CheckPoint.ThisTimeLineID (uint32) at offset 48
	tli := le.Uint32(buf[48:])
	if tli != 1 {
		t.Errorf("checkPointCopy.ThisTimeLineID at offset 48: got %d, want 1", tli)
	}

	// CheckPoint.PrevTimeLineID (uint32) at offset 52
	prevTLI := le.Uint32(buf[52:])
	if prevTLI != 1 {
		t.Errorf("checkPointCopy.PrevTimeLineID at offset 52: got %d, want 1", prevTLI)
	}

	// CheckPoint.fullPageWrites (bool) at offset 56
	if buf[56] != 1 {
		t.Errorf("checkPointCopy.fullPageWrites at offset 56: got %d, want 1 (on)", buf[56])
	}

	// Padding bytes 57-59 must be zero
	for _, off := range []int{57, 58, 59} {
		if buf[off] != 0 {
			t.Errorf("padding byte at offset %d: got 0x%02X, want 0x00", off, buf[off])
		}
	}

	// CheckPoint.wal_level (int=uint32) at offset 60 — replica=1
	walLevel := le.Uint32(buf[60:])
	if walLevel != 1 {
		t.Errorf("checkPointCopy.wal_level at offset 60: got %d, want 1 (replica)", walLevel)
	}

	// CheckPoint.nextXid (FullTransactionId=uint64) at offset 64 — FirstNormalTransactionId=3
	nextXid := le.Uint64(buf[64:])
	if nextXid != pgFirstNormalXID {
		t.Errorf("checkPointCopy.nextXid at offset 64: got %d, want %d", nextXid, pgFirstNormalXID)
	}

	// CheckPoint.nextOid (uint32) at offset 72 — FirstGenbkiObjectId=10000
	nextOid := le.Uint32(buf[72:])
	if nextOid != pgFirstGenbkiOID {
		t.Errorf("checkPointCopy.nextOid at offset 72: got %d, want %d", nextOid, pgFirstGenbkiOID)
	}

	// CheckPoint.nextMulti (uint32) at offset 76 — FirstMultiXactId=1
	nextMulti := le.Uint32(buf[76:])
	if nextMulti != pgFirstMultiXact {
		t.Errorf("checkPointCopy.nextMulti at offset 76: got %d, want %d", nextMulti, pgFirstMultiXact)
	}

	// CheckPoint.nextMultiOffset (uint32) at offset 80 — 0
	nextMultiOff := le.Uint32(buf[80:])
	if nextMultiOff != 0 {
		t.Errorf("checkPointCopy.nextMultiOffset at offset 80: got %d, want 0", nextMultiOff)
	}

	// CheckPoint.oldestXid (uint32) at offset 84 — FirstNormalTransactionId=3
	oldestXid := le.Uint32(buf[84:])
	if want := uint32(pgFirstNormalXID); oldestXid != want {
		t.Errorf("checkPointCopy.oldestXid at offset 84: got %d, want %d", oldestXid, want)
	}

	// CheckPoint.oldestXidDB (uint32) at offset 88 — Template1DbOid=1
	oldestXidDB := le.Uint32(buf[88:])
	if oldestXidDB != pgTemplate1DbOID {
		t.Errorf("checkPointCopy.oldestXidDB at offset 88: got %d, want %d", oldestXidDB, pgTemplate1DbOID)
	}

	// CheckPoint.oldestMulti (uint32) at offset 92 — FirstMultiXactId=1
	oldestMulti := le.Uint32(buf[92:])
	if oldestMulti != pgFirstMultiXact {
		t.Errorf("checkPointCopy.oldestMulti at offset 92: got %d, want %d", oldestMulti, pgFirstMultiXact)
	}

	// CheckPoint.oldestMultiDB (uint32) at offset 96 — Template1DbOid=1
	oldestMultiDB := le.Uint32(buf[96:])
	if oldestMultiDB != pgTemplate1DbOID {
		t.Errorf("checkPointCopy.oldestMultiDB at offset 96: got %d, want %d", oldestMultiDB, pgTemplate1DbOID)
	}

	// Alignment padding bytes 100-103 must be zero
	for _, off := range []int{100, 101, 102, 103} {
		if buf[off] != 0 {
			t.Errorf("alignment pad byte at offset %d: got 0x%02X, want 0x00", off, buf[off])
		}
	}

	// CheckPoint.time (int64) at offset 104 — should equal now.Unix()
	checkpointTime := int64(le.Uint64(buf[104:]))
	if checkpointTime < before.Unix() || checkpointTime > after.Unix() {
		t.Errorf("checkPointCopy.time at offset 104: got %d, want between %d and %d",
			checkpointTime, before.Unix(), after.Unix())
	}

	// CheckPoint.oldestCommitTsXid (uint32) at offset 112 — InvalidTransactionId=0
	if v := le.Uint32(buf[112:]); v != 0 {
		t.Errorf("checkPointCopy.oldestCommitTsXid at offset 112: got %d, want 0", v)
	}

	// CheckPoint.newestCommitTsXid (uint32) at offset 116 — InvalidTransactionId=0
	if v := le.Uint32(buf[116:]); v != 0 {
		t.Errorf("checkPointCopy.newestCommitTsXid at offset 116: got %d, want 0", v)
	}

	// CheckPoint.oldestActiveXid (uint32) at offset 120 — InvalidTransactionId=0
	if v := le.Uint32(buf[120:]); v != 0 {
		t.Errorf("checkPointCopy.oldestActiveXid at offset 120: got %d, want 0", v)
	}

	// Tail padding bytes 124-127 must be zero
	for _, off := range []int{124, 125, 126, 127} {
		if buf[off] != 0 {
			t.Errorf("tail pad byte at offset %d: got 0x%02X, want 0x00", off, buf[off])
		}
	}
}

// TestBuildPgControlFileSize verifies the total file size and that zero-pad
// bytes beyond the active payload are all zero.
func TestBuildPgControlFileSize(t *testing.T) {
	buf := buildPgControl(0x1234567890ABCDEF, time.Now(), nil)
	if len(buf) != pgControlFileSize {
		t.Fatalf("buildPgControl: len=%d, want %d", len(buf), pgControlFileSize)
	}
	for i := pgControlDataSize; i < pgControlFileSize; i++ {
		if buf[i] != 0 {
			t.Errorf("zero-pad byte at offset %d: got 0x%02X, want 0x00", i, buf[i])
			break
		}
	}
}

// TestBuildPgControlCompatibilityFields verifies the compile-time constants that
// ReadControlFile validates with FATAL: pg_control_version, catalog_version_no,
// maxAlign, floatFormat, blcksz, relseg_size, xlog_blcksz, xlog_seg_size,
// nameDataLen, indexMaxKeys, toast_max_chunk_size, loblksize, float8ByVal.
func TestBuildPgControlCompatibilityFields(t *testing.T) {
	buf := buildPgControl(0, time.Now(), nil)
	le := binary.LittleEndian

	checks := []struct {
		offset int
		size   int
		want   uint64
		name   string
	}{
		{8, 4, pgControlVersion, "pg_control_version"},
		{12, 4, pgCatalogVersionNo, "catalog_version_no"},
		{204, 4, 8, "maxAlign"},
		{216, 4, 8192, "blcksz"},
		{220, 4, 131072, "relseg_size"},
		{224, 4, 8192, "xlog_blcksz"},
		{228, 4, 16 * 1024 * 1024, "xlog_seg_size"},
		{232, 4, 64, "nameDataLen"},
		{236, 4, 32, "indexMaxKeys"},
		{240, 4, 1996, "toast_max_chunk_size"},
		{244, 4, 2048, "loblksize"},
	}
	for _, c := range checks {
		var got uint64
		if c.size == 4 {
			got = uint64(le.Uint32(buf[c.offset:]))
		} else {
			got = le.Uint64(buf[c.offset:])
		}
		if got != c.want {
			t.Errorf("%s at offset %d: got %d, want %d", c.name, c.offset, got, c.want)
		}
	}

	// floatFormat (float64) at offset 208
	floatBits := le.Uint64(buf[208:])
	if floatBits != math.Float64bits(1234567.0) {
		t.Errorf("floatFormat at offset 208: got 0x%016X, want 0x%016X",
			floatBits, math.Float64bits(1234567.0))
	}

	// float8ByVal (bool) at offset 248 — true on 64-bit platforms
	if buf[248] != 1 {
		t.Errorf("float8ByVal at offset 248: got %d, want 1", buf[248])
	}
}

// TestBuildPgControlUnloggedLSN verifies that unloggedLSN at offset 128 is
// set to FirstNormalUnloggedLSN=1000, not zero. A zero value causes PG's
// CreateUnloggedFile to treat every unlogged relation as if it were at the
// start of the LSN space, which confuses recovery (xlogdefs.h:37).
func TestBuildPgControlUnloggedLSN(t *testing.T) {
	buf := buildPgControl(0, time.Now(), nil)
	le := binary.LittleEndian
	got := le.Uint64(buf[128:])
	if got != pgFirstNormalUnloggedLSN {
		t.Errorf("unloggedLSN at offset 128: got %d, want %d (FirstNormalUnloggedLSN)",
			got, pgFirstNormalUnloggedLSN)
	}
}

// TestBuildPgControlGUCWiring verifies that the six GUC echo fields in
// pg_control (wal_level, MaxConnections, max_worker_processes,
// max_wal_senders, max_prepared_xacts, max_locks_per_xact) are read from
// the supplied *config.Registry rather than hard-coded constants when a
// non-nil registry is passed. A standby's CheckRequiredParameterValues
// (xlog.c:5423) fatals if these values fall below the primary's.
func TestBuildPgControlGUCWiring(t *testing.T) {
	reg := config.BuildDefaultRegistry()
	// Set non-default values so the test would fail if defaults are used.
	for name, val := range map[string]string{
		"max_connections":         "200",
		"max_worker_processes":    "16",
		"max_wal_senders":         "20",
		"max_prepared_transactions": "5",
		"max_locks_per_transaction": "128",
		"wal_level":               "logical",
	} {
		if err := reg.Set(name, val, config.SourceCommandLine); err != nil {
			t.Fatalf("reg.Set(%q, %q): %v", name, val, err)
		}
	}

	buf := buildPgControl(0, time.Now(), reg)
	le := binary.LittleEndian

	checks := []struct {
		offset int
		want   uint32
		name   string
	}{
		{60, 2, "checkPointCopy.wal_level (logical=2)"},
		{172, 2, "wal_level (logical=2)"},
		{180, 200, "MaxConnections"},
		{184, 16, "max_worker_processes"},
		{188, 20, "max_wal_senders"},
		{192, 5, "max_prepared_xacts"},
		{196, 128, "max_locks_per_xact"},
	}
	for _, c := range checks {
		if got := le.Uint32(buf[c.offset:]); got != c.want {
			t.Errorf("%s at offset %d: got %d, want %d",
				c.name, c.offset, got, c.want)
		}
	}
}
