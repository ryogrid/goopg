package control

// pg_control runtime update helpers.
//
// This file defines ControlFileData (the mutable runtime subset of
// PostgreSQL's ControlFileData struct, pg_control.h:104) and the
// UpdateControlFile helper that mirrors upstream's update_controlfile()
// in src/common/controldata_utils.c:189.
//
// Only the fields that goopg mutates at runtime are decoded/encoded.
// Immutable fields (pg_control_version, catalog_version_no, block sizes,
// system_identifier, mock_nonce, checkPointCopy bootstrap values like
// nextXid/nextOid) are preserved in-place in the raw buffer.

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

const (
	pgControlFilePath  = "global/pg_control"
	pgControlFileSize  = 8192
	pgControlCRCOffset = 292 // offsetof(ControlFileData, crc) on x86_64
)

// DB state constants mirror PostgreSQL's DBState enum (pg_control.h).
const (
	DBStateShutdowned           = uint32(1)
	DBStateShutdownedInRecovery = uint32(2)
	DBStateShutdowning          = uint32(3)
	DBStateInCrashRecovery      = uint32(4)
	DBStateInArchiveRecovery    = uint32(5)
	DBStateInProduction         = uint32(6)
)

var pgCRCTable = crc32.MakeTable(crc32.Castagnoli)

// ControlFileData holds the mutable runtime fields of the PostgreSQL
// pg_control file. Field names and byte offsets match pg_control.h (PG18,
// x86_64 layout). Immutable build-constant fields are not exposed here.
type ControlFileData struct {
	// offset 0: system_identifier (uint64) — the cluster identity assigned
	// once at bootstrap (xlog.c:5099-5101 via InitControlFile) and never
	// mutated afterwards. DECODE-ONLY: encodeControlFileData deliberately
	// does not write it back, so UpdateControlFile's read-mutate-write cycle
	// preserves the original bytes and a caller that leaves this field zero
	// cannot clobber the cluster identity. M0131-S2.
	SystemIdentifier uint64
	// offset 16: DBState enum (uint32)
	State uint32
	// offset 24: pg_time_t / int64 — seconds since Unix epoch
	Time int64
	// offset 32: XLogRecPtr — location of most recent checkpoint record
	CheckPoint uint64
	// checkPointCopy substructure (CheckPoint struct, offset 40..128)
	// offset 40: checkPointCopy.redo (XLogRecPtr)
	CheckPointCopyRedo uint64
	// offset 48: checkPointCopy.ThisTimeLineID (uint32)
	CheckPointCopyThisTLI uint32
	// offset 52: checkPointCopy.PrevTimeLineID (uint32)
	CheckPointCopyPrevTLI uint32
	// offset 56: checkPointCopy.fullPageWrites (bool)
	CheckPointCopyFullPageWrites bool
	// offset 60: checkPointCopy.wal_level (int → uint32)
	CheckPointCopyWalLevel uint32
	// offset 64: checkPointCopy.nextXid (FullTransactionId = uint64).
	// PG18 stores epoch in the high 32 bits and the raw XID in the low
	// 32 bits; bootstrap value is FirstNormalTransactionId (3) with
	// epoch 0. M0106-0010 batched-45 wires the checkpointer to refresh
	// this from mvcc.Manager.NextXID() at every checkpoint so a
	// basebackup taken after user DDL carries the right starting XID.
	CheckPointCopyNextXid uint64
	// offset 72: checkPointCopy.nextOid (Oid = uint32).
	// Refreshed at every checkpoint from catalog.InMemory.NextOID()
	// so a crashed cluster can recover the OID counter from pg_control
	// without relying on pg_catalog.json. M0106-0013.
	CheckPointCopyNextOid uint32
	// offset 104: checkPointCopy.time (pg_time_t / int64)
	CheckPointCopyTime int64
	// offset 128: XLogRecPtr — fake LSN counter for unlogged relations
	UnloggedLSN uint64
	// offset 136: XLogRecPtr — minimum recovery point
	MinRecoveryPoint uint64
	// offset 144: TimeLineID (uint32)
	MinRecoveryPointTLI uint32
	// offset 152: XLogRecPtr — start of last base backup
	BackupStartPoint uint64
	// offset 160: XLogRecPtr — end of last base backup
	BackupEndPoint uint64
	// offset 168: bool — backup must end before recovery is consistent
	BackupEndRequired bool
	// GUC echo fields (offsets 172-200) — replicated from primary via
	// XLOG_PARAMETER_CHANGE; used by standby CheckRequiredParameterValues.
	// offset 172: wal_level (int → uint32)
	WalLevel uint32
	// offset 176: wal_log_hints (bool)
	WalLogHints bool
	// offset 180: MaxConnections (int → uint32)
	MaxConnections uint32
	// offset 184: max_worker_processes (int → uint32)
	MaxWorkerProcesses uint32
	// offset 188: max_wal_senders (int → uint32)
	MaxWalSenders uint32
	// offset 192: max_prepared_xacts (int → uint32)
	MaxPreparedXacts uint32
	// offset 196: max_locks_per_xact (int → uint32)
	MaxLocksPerXact uint32
	// offset 200: track_commit_timestamp (bool)
	TrackCommitTimestamp bool
	// offset 252: data_checksum_version (uint32). 0 ⇒ data-page checksums
	// disabled, 1 ⇒ enabled (PG_DATA_CHECKSUM_VERSION). Set by initdb
	// (--data-checksums) and never changed at runtime by goopg; the
	// storage Manager reads it to decide whether to checksum/verify every
	// block. See docs/design/0102-0019-initdb-data-checksums.md.
	DataChecksumVersion uint32
}

// decodeControlFileData reads the mutable runtime fields from a raw
// pg_control buffer (must be at least 296 bytes).
func decodeControlFileData(buf []byte) *ControlFileData {
	le := binary.LittleEndian
	return &ControlFileData{
		SystemIdentifier:             le.Uint64(buf[0:]),
		State:                        le.Uint32(buf[16:]),
		Time:                         int64(le.Uint64(buf[24:])),
		CheckPoint:                   le.Uint64(buf[32:]),
		CheckPointCopyRedo:           le.Uint64(buf[40:]),
		CheckPointCopyThisTLI:        le.Uint32(buf[48:]),
		CheckPointCopyPrevTLI:        le.Uint32(buf[52:]),
		CheckPointCopyFullPageWrites: buf[56] != 0,
		CheckPointCopyWalLevel:       le.Uint32(buf[60:]),
		CheckPointCopyNextXid:        le.Uint64(buf[64:]),
		CheckPointCopyNextOid:        le.Uint32(buf[72:]),
		CheckPointCopyTime:           int64(le.Uint64(buf[104:])),
		UnloggedLSN:                  le.Uint64(buf[128:]),
		MinRecoveryPoint:             le.Uint64(buf[136:]),
		MinRecoveryPointTLI:          le.Uint32(buf[144:]),
		BackupStartPoint:             le.Uint64(buf[152:]),
		BackupEndPoint:               le.Uint64(buf[160:]),
		BackupEndRequired:            buf[168] != 0,
		WalLevel:                     le.Uint32(buf[172:]),
		WalLogHints:                  buf[176] != 0,
		MaxConnections:               le.Uint32(buf[180:]),
		MaxWorkerProcesses:           le.Uint32(buf[184:]),
		MaxWalSenders:                le.Uint32(buf[188:]),
		MaxPreparedXacts:             le.Uint32(buf[192:]),
		MaxLocksPerXact:              le.Uint32(buf[196:]),
		TrackCommitTimestamp:         buf[200] != 0,
		DataChecksumVersion:          le.Uint32(buf[252:]),
	}
}

// encodeControlFileData writes the mutable runtime fields back into the raw
// pg_control buffer (must be at least 296 bytes). Does not recompute CRC.
func encodeControlFileData(buf []byte, cd *ControlFileData) {
	le := binary.LittleEndian
	le.PutUint32(buf[16:], cd.State)
	le.PutUint64(buf[24:], uint64(cd.Time))
	le.PutUint64(buf[32:], cd.CheckPoint)
	le.PutUint64(buf[40:], cd.CheckPointCopyRedo)
	le.PutUint32(buf[48:], cd.CheckPointCopyThisTLI)
	le.PutUint32(buf[52:], cd.CheckPointCopyPrevTLI)
	if cd.CheckPointCopyFullPageWrites {
		buf[56] = 1
	} else {
		buf[56] = 0
	}
	le.PutUint32(buf[60:], cd.CheckPointCopyWalLevel)
	le.PutUint64(buf[64:], cd.CheckPointCopyNextXid)
	le.PutUint32(buf[72:], cd.CheckPointCopyNextOid)
	le.PutUint64(buf[104:], uint64(cd.CheckPointCopyTime))
	le.PutUint64(buf[128:], cd.UnloggedLSN)
	le.PutUint64(buf[136:], cd.MinRecoveryPoint)
	le.PutUint32(buf[144:], cd.MinRecoveryPointTLI)
	le.PutUint64(buf[152:], cd.BackupStartPoint)
	le.PutUint64(buf[160:], cd.BackupEndPoint)
	if cd.BackupEndRequired {
		buf[168] = 1
	} else {
		buf[168] = 0
	}
	le.PutUint32(buf[172:], cd.WalLevel)
	if cd.WalLogHints {
		buf[176] = 1
	} else {
		buf[176] = 0
	}
	le.PutUint32(buf[180:], cd.MaxConnections)
	le.PutUint32(buf[184:], cd.MaxWorkerProcesses)
	le.PutUint32(buf[188:], cd.MaxWalSenders)
	le.PutUint32(buf[192:], cd.MaxPreparedXacts)
	le.PutUint32(buf[196:], cd.MaxLocksPerXact)
	if cd.TrackCommitTimestamp {
		buf[200] = 1
	} else {
		buf[200] = 0
	}
	le.PutUint32(buf[252:], cd.DataChecksumVersion)
}

// UpdateControlFile reads the on-disk pg_control file at
// <dataDir>/global/pg_control, decodes its mutable runtime fields into a
// ControlFileData, calls fn to apply mutations, re-encodes the (possibly
// modified) struct, recomputes CRC32C over the first 292 bytes, and
// writes the 8192-byte file back.
//
// Mirrors upstream update_controlfile() (src/common/controldata_utils.c:189).
// The fn receives the current decoded state; only the fields fn explicitly
// sets are changed — all other bytes in the buffer are preserved.
func UpdateControlFile(dataDir string, fn func(*ControlFileData)) error {
	path := filepath.Join(dataDir, pgControlFilePath)
	buf, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("control: read pg_control: %w", err)
	}
	if len(buf) < pgControlCRCOffset+4 {
		return fmt.Errorf("control: pg_control too short (%d bytes)", len(buf))
	}
	// Ensure we work on a full-size buffer (upstream always writes 8192 bytes).
	if len(buf) < pgControlFileSize {
		full := make([]byte, pgControlFileSize)
		copy(full, buf)
		buf = full
	}
	cd := decodeControlFileData(buf)
	fn(cd)
	encodeControlFileData(buf, cd)
	crc := crc32.Checksum(buf[:pgControlCRCOffset], pgCRCTable)
	binary.LittleEndian.PutUint32(buf[pgControlCRCOffset:], crc)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return fmt.Errorf("control: write pg_control: %w", err)
	}
	return nil
}

// ReadControlFile reads the current pg_control values from dataDir.
// Returns nil when the file does not exist (fresh cluster before initdb completes).
// Verifies the CRC32C over the first 292 bytes; a mismatch returns an error so
// callers (like LoadOrCreateTimelineID) can fall back to the secondary copy.
// Callers that need to compare GUC echo fields against live configuration
// use this to avoid a spurious read+write cycle via UpdateControlFile.
func ReadControlFile(dataDir string) (*ControlFileData, error) {
	path := filepath.Join(dataDir, pgControlFilePath)
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("control: read pg_control: %w", err)
	}
	if len(buf) < pgControlCRCOffset+4 {
		return nil, fmt.Errorf("control: pg_control too short (%d bytes)", len(buf))
	}
	// Verify CRC32C so a corrupt or placeholder pg_control (e.g. a test
	// stub filled with 0xC0) does not pass as valid. M0130-S8.
	stored := binary.LittleEndian.Uint32(buf[pgControlCRCOffset:])
	computed := crc32.Checksum(buf[:pgControlCRCOffset], pgCRCTable)
	if stored != computed {
		return nil, fmt.Errorf("control: pg_control CRC mismatch: stored=0x%08X computed=0x%08X", stored, computed)
	}
	return decodeControlFileData(buf), nil
}
