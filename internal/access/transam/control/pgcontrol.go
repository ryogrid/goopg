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

// DBStateName renders a DBState the way pg_controldata does, so goopg's
// startup log lines are greppable against a real cluster's. The strings are
// upstream's dbState[] table (postgres/src/bin/pg_controldata/pg_controldata.c),
// including its "unrecognized status code" fallback. M0131-S20.1.
func DBStateName(state uint32) string {
	switch state {
	case DBStateShutdowned:
		return "shut down"
	case DBStateShutdownedInRecovery:
		return "shut down in recovery"
	case DBStateShutdowning:
		return "shutting down"
	case DBStateInCrashRecovery:
		return "in crash recovery"
	case DBStateInArchiveRecovery:
		return "in archive recovery"
	case DBStateInProduction:
		return "in production"
	default:
		return "unrecognized status code"
	}
}

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
	// M0131-S18.2: the remaining checkPointCopy members. Until this slice
	// they were never decoded, so they survived only by accident of
	// UpdateControlFile's read-modify-write cycle — any caller that wanted
	// to *set* one had no way to, and any future writer that assembled a
	// fresh buffer would have silently zeroed them. PG reads every one of
	// them at startup (InitWalRecovery / StartupMultiXact /
	// StartupCommitTs), so a zero here is not benign: oldestXid = 0 makes
	// SetTransactionIdLimit compute a bogus wrap-around horizon, and
	// oldestMulti = 0 trips "MultiXactId 0 does not exist".
	//
	// offset 76: checkPointCopy.nextMulti (MultiXactId = uint32)
	CheckPointCopyNextMulti uint32
	// offset 80: checkPointCopy.nextMultiOffset (MultiXactOffset = uint32)
	CheckPointCopyNextMultiOffset uint32
	// offset 84: checkPointCopy.oldestXid (TransactionId = uint32) —
	// cluster-wide minimum datfrozenxid
	CheckPointCopyOldestXid uint32
	// offset 88: checkPointCopy.oldestXidDB (Oid = uint32)
	CheckPointCopyOldestXidDB uint32
	// offset 92: checkPointCopy.oldestMulti (MultiXactId = uint32) —
	// cluster-wide minimum datminmxid
	CheckPointCopyOldestMulti uint32
	// offset 96: checkPointCopy.oldestMultiDB (Oid = uint32)
	CheckPointCopyOldestMultiDB uint32
	// offset 104: checkPointCopy.time (pg_time_t / int64)
	CheckPointCopyTime int64
	// offset 112: checkPointCopy.oldestCommitTsXid (TransactionId = uint32)
	CheckPointCopyOldestCommitTsXid uint32
	// offset 116: checkPointCopy.newestCommitTsXid (TransactionId = uint32)
	CheckPointCopyNewestCommitTsXid uint32
	// offset 120: checkPointCopy.oldestActiveXid (TransactionId = uint32).
	// Only meaningful for an ONLINE checkpoint under wal_level >= replica;
	// upstream stores InvalidTransactionId (0) otherwise (xlog.c:7050).
	CheckPointCopyOldestActiveXid uint32
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
		// M0131-S18.2: full checkPointCopy coverage.
		CheckPointCopyNextMulti:         le.Uint32(buf[76:]),
		CheckPointCopyNextMultiOffset:   le.Uint32(buf[80:]),
		CheckPointCopyOldestXid:         le.Uint32(buf[84:]),
		CheckPointCopyOldestXidDB:       le.Uint32(buf[88:]),
		CheckPointCopyOldestMulti:       le.Uint32(buf[92:]),
		CheckPointCopyOldestMultiDB:     le.Uint32(buf[96:]),
		CheckPointCopyTime:              int64(le.Uint64(buf[104:])),
		CheckPointCopyOldestCommitTsXid: le.Uint32(buf[112:]),
		CheckPointCopyNewestCommitTsXid: le.Uint32(buf[116:]),
		CheckPointCopyOldestActiveXid:   le.Uint32(buf[120:]),
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
	// M0131-S18.2: full checkPointCopy coverage. Safe to write
	// unconditionally because every ControlFileData reaching this function
	// came out of decodeControlFileData, so an untouched field round-trips
	// to the same bytes.
	le.PutUint32(buf[76:], cd.CheckPointCopyNextMulti)
	le.PutUint32(buf[80:], cd.CheckPointCopyNextMultiOffset)
	le.PutUint32(buf[84:], cd.CheckPointCopyOldestXid)
	le.PutUint32(buf[88:], cd.CheckPointCopyOldestXidDB)
	le.PutUint32(buf[92:], cd.CheckPointCopyOldestMulti)
	le.PutUint32(buf[96:], cd.CheckPointCopyOldestMultiDB)
	le.PutUint64(buf[104:], uint64(cd.CheckPointCopyTime))
	le.PutUint32(buf[112:], cd.CheckPointCopyOldestCommitTsXid)
	le.PutUint32(buf[116:], cd.CheckPointCopyNewestCommitTsXid)
	le.PutUint32(buf[120:], cd.CheckPointCopyOldestActiveXid)
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
	buf, err := BuildUpdatedControlImage(dataDir, fn)
	if err != nil {
		return err
	}
	// Write exactly PG_CONTROL_FILE_SIZE bytes, as upstream does. A longer
	// file (which PG never produces) keeps its tail rather than being
	// truncated away — O_RDWR overwrite semantics, not O_TRUNC.
	return writeControlFileDurably(filepath.Join(dataDir, pgControlFilePath), buf)
}

// BuildUpdatedControlImage is UpdateControlFile's read-decode-mutate-encode
// half without the write: it returns the 8192-byte pg_control image that
// UpdateControlFile *would* have stored, leaving the on-disk file untouched.
//
// M0131-S29. BASE_BACKUP needs a checkpoint-patched control image for the tar
// stream it ships, but patching the LIVE cluster's pg_control to get one is a
// bug: it publishes minRecoveryPoint/backupEndPoint into a running primary,
// and a crash in the window before the next checkpoint then makes the cluster
// FATAL "requested timeline %u does not contain minimum recovery point"
// (xlogrecovery.c:878-886) on a promoted (TLI >= 2) cluster. Upstream never
// touches the source either — basebackup.c:352-360 sends XLOG_CONTROL_FILE
// through plain sendFile().
func BuildUpdatedControlImage(dataDir string, fn func(*ControlFileData)) ([]byte, error) {
	path := filepath.Join(dataDir, pgControlFilePath)
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("control: read pg_control: %w", err)
	}
	if len(buf) < pgControlCRCOffset+4 {
		return nil, fmt.Errorf("control: pg_control too short (%d bytes)", len(buf))
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
	return buf[:pgControlFileSize], nil
}

// writeControlFileDurably overwrites pg_control in place and fsyncs it,
// mirroring upstream update_controlfile() with do_sync = true
// (src/common/controldata_utils.c:216-281).
//
// M0131-S18.1. The previous implementation was os.WriteFile, i.e.
// O_CREATE|O_WRONLY|O_TRUNC with no fsync. Two defects followed:
//
//   - O_TRUNC creates a window in which pg_control is ZERO BYTES long. A
//     SIGKILL (or host crash) inside it leaves a file PG refuses with
//     PANIC: could not read file "global/pg_control": read 0 of 296 — an
//     unrecoverable cluster, from a file whose whole purpose is to survive
//     crashes. Upstream never truncates: it opens O_RDWR and overwrites the
//     fixed-size image, so a torn write leaves a CRC mismatch (recoverable
//     via the operator-visible "incorrect checksum" path) rather than an
//     empty file.
//   - No fsync means an acknowledged checkpoint could be lost to the page
//     cache while the WAL it describes was already flushed, so recovery
//     would restart from an older redo point than pg_control claims.
//
// The write is a single 8192-byte WriteAt at offset 0 (upstream writes
// PG_CONTROL_FILE_SIZE bytes from a zero-padded buffer); the caller has
// already grown buf to that size, so no O_TRUNC is needed to keep the file
// from retaining a longer tail.
func writeControlFileDurably(path string, buf []byte) error {
	if len(buf) != pgControlFileSize {
		return fmt.Errorf("control: refusing to write %d-byte pg_control image (want %d)", len(buf), pgControlFileSize)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("control: open pg_control for update: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteAt(buf, 0); err != nil {
		return fmt.Errorf("control: write pg_control: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("control: fsync pg_control: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("control: close pg_control: %w", err)
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
