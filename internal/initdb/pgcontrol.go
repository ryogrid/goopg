package initdb

// PostgreSQL pg_control file generation.
//
// Mirrors postgres/src/include/catalog/pg_control.h (ControlFileData).
// Written during initdb so pg_controldata, pg_checksums, and other PG
// client tools can inspect a goopg data directory. Goopg does not yet
// update pg_control at runtime (e.g. on checkpoint); the file reflects
// the cluster's initdb-time state only. Reading it conveys configuration
// and parameter settings, not live checkpoint progress.
//
// PG18 layout. PG_CONTROL_VERSION=1800, file size 8192 bytes,
// active payload 296 bytes followed by zero padding. CRC32C over the
// first 292 bytes (everything before the crc field). Little-endian
// on x86_64. M0095-0001. Design doc:
// docs/design/0095-0001-pg-control-file.md

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/config"
)

// pgControlFile is the path (relative to the data directory) for the
// PostgreSQL control file. Matches upstream's location.
const pgControlFile = "global/pg_control"

// PostgreSQL pg_control constants (PG18).
const (
	pgControlVersion      = 1800
	pgCatalogVersionNo    = 202506291
	pgControlFileSize     = 8192
	pgControlDataSize     = 296 // sizeof(ControlFileData) on x86_64
	pgControlCRCOffset    = 292 // offsetof(ControlFileData, crc) on x86_64
	pgControlMockNonceLen = 32

	// Initial checkpoint LSN: wal_segment_size(16 MiB) + SizeOfXLogLongPHD(40).
	// Segment 1 starts at byte 0x01000000; the first WAL record follows the
	// 40-byte XLogLongPageHeaderData, so the checkpoint record lands at 0x01000028.
	pgInitCheckpointLSN = uint64(16*1024*1024 + 40)

	// Bootstrap values from BootStrapXLOG (xlog.c:5115-5132).
	pgFirstNormalXID  = uint64(3)     // FirstNormalTransactionId
	pgFirstGenbkiOID  = uint32(10000) // FirstGenbkiObjectId
	pgFirstMultiXact  = uint32(1)     // FirstMultiXactId
	pgTemplate1DbOID  = uint32(1)     // Template1DbOid

	// pgFirstNormalUnloggedLSN mirrors FirstNormalUnloggedLSN
	// (src/include/access/xlogdefs.h:37). Unlogged relations use a
	// monotonically-increasing fake LSN space starting at 1000 so they
	// are always treated as "newer than any real WAL" by recovery logic.
	pgFirstNormalUnloggedLSN = uint64(1000)
)

// dbStateShutdowned mirrors PostgreSQL's DB_SHUTDOWNED enum value.
// A fresh goopg data directory is in a "clean shutdown" state by definition.
const dbStateShutdowned = 1

// crcCastagnoliTable is the CRC32C polynomial table used by
// PostgreSQL's pg_crc32c. Reused across calls.
var crcCastagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// writePgControl writes the PostgreSQL-format pg_control file at
// <dataDir>/global/pg_control. The file is 8192 bytes (PG_CONTROL_FILE_SIZE)
// with a 296-byte ControlFileData struct at the start and zeros elsewhere.
// systemID must match the cluster's system_identifier (see LoadOrCreateSystemID).
func writePgControl(dataDir string, systemID uint64, cfg *config.Registry) error {
	buf := buildPgControl(systemID, time.Now(), cfg)
	path := filepath.Join(dataDir, pgControlFile)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return fmt.Errorf("goopg: write pg_control: %w", err)
	}
	return nil
}

// gucInt reads an integer GUC from reg, returning def when reg is nil,
// the GUC is absent, or its value is unparseable. Mirrors upstream's
// C-side GUC variable access pattern (xlog.c:4223-4227).
func gucInt(reg *config.Registry, name string, def int32) int32 {
	if reg == nil {
		return def
	}
	v, ok := reg.Get(name)
	if !ok || v.Value == "" {
		return def
	}
	n, err := strconv.ParseInt(v.Value, 10, 32)
	if err != nil {
		return def
	}
	return int32(n)
}

// walLevelInt maps the wal_level GUC enum string to the C WAL_LEVEL_*
// integer stored in ControlFileData (xlog.c:4228):
//
//	minimal=0, replica=1, logical=2.
//
// Returns 1 (replica) when reg is nil or the GUC is absent.
func walLevelInt(reg *config.Registry) uint32 {
	if reg == nil {
		return 1
	}
	v, ok := reg.Get("wal_level")
	if !ok {
		return 1
	}
	switch strings.ToLower(v.Value) {
	case "minimal":
		return 0
	case "logical":
		return 2
	default:
		return 1 // replica
	}
}

// dbStateInProduction mirrors PostgreSQL's DB_IN_PRODUCTION enum value (6).
// DB_STARTUP=0, DB_SHUTDOWNED=1, DB_SHUTDOWNED_IN_RECOVERY=2,
// DB_SHUTDOWNING=3, DB_IN_CRASH_RECOVERY=4, DB_IN_ARCHIVE_RECOVERY=5,
// DB_IN_PRODUCTION=6.
const dbStateInProduction = 6

// UpdateControlCheckpoint overwrites the checkpoint-related fields in the
// on-disk pg_control file. Used by BASE_BACKUP after a forced checkpoint so
// a PostgreSQL standby booted from the backup sees a valid REDO location.
func UpdateControlCheckpoint(dataDir string, redoLSN uint64) error {
	path := filepath.Join(dataDir, pgControlFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("update pg_control: %w", err)
	}
	if len(body) < pgControlCRCOffset+4 {
		return fmt.Errorf("update pg_control: file too short (%d bytes)", len(body))
	}
	le := binary.LittleEndian
	now := time.Now()
	// goopg uses 1-based LSNs internally; PG expects 0-based.
	lsn0 := redoLSN - 1

	// ControlFileData layout (PG18, verified from pg_control.h):
	//   offset 16: state (uint32)
	//   offset 24: time (pg_time_t, int64)
	//   offset 32: checkPoint (XLogRecPtr, 8 bytes)
	//   offset 40: checkPointCopy (CheckPoint, 88 bytes) — no prevCheckPoint
	//     → offset 40: checkPointCopy.redo (XLogRecPtr, 8)
	//     → offset 48: checkPointCopy.ThisTimeLineID (uint32, 4)
	//     → offset 52: checkPointCopy.PrevTimeLineID (uint32, 4)
	//   offset 128: unloggedLSN (XLogRecPtr, 8 bytes)
	//   offset 136: minRecoveryPoint (XLogRecPtr, 8 bytes)
	//   offset 144: minRecoveryPointTLI (TimeLineID, 4 bytes)

	// state → DB_IN_PRODUCTION (taken from a running server)
	le.PutUint32(body[16:], dbStateInProduction)
	// time → now
	le.PutUint64(body[24:], uint64(now.Unix()))
	// checkPoint → redoLSN (0-based)
	le.PutUint64(body[32:], lsn0)
	// checkPointCopy.redo → redoLSN (0-based)
	le.PutUint64(body[40:], lsn0)
	// checkPointCopy.ThisTimeLineID → 1
	le.PutUint32(body[48:], 1)
	// checkPointCopy.PrevTimeLineID → 1 (match PG-generated backups)
	le.PutUint32(body[52:], 1)
	// checkPointCopy.fullPageWrites → on (1) at offset 56
	body[56] = 1
	// minRecoveryPoint → set to a small non-zero value so
	// CheckRecoveryConsistency doesn't bail out immediately
	// (XLogRecPtrIsInvalid(0) returns true, causing an early return).
	// Use 1 so minRecoveryPoint <= lastReplayedEndRecPtr is satisfied
	// after the first WAL record (the checkpoint) has been replayed.
	le.PutUint64(body[136:], 1)
	le.PutUint32(body[144:], 1) // minRecoveryPointTLI
	// backupEndPoint → redo LSN. read_backup_label sets
	// backupEndRequired=true, which blocks reachedConsistency in
	// CheckRecoveryConsistency. PG clears backupEndRequired only via
	// ReachedEndOfBackup(), which requires backupEndPoint (non-zero)
	// <= lastReplayedEndRecPtr. Set to the REDO LSN so the condition
	// is satisfied immediately after checkpoint replay.
	le.PutUint64(body[160:], lsn0)
	// backupEndRequired → false. Even if the control file has this as
	// true, read_backup_label overrides it. Left as false (zero) so
	// the initial state is clean. We clear it via backupEndPoint above.
	crc := crc32.Checksum(body[:pgControlCRCOffset], crcCastagnoliTable)
	le.PutUint32(body[pgControlCRCOffset:], crc)

	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("update pg_control: write: %w", err)
	}
	return nil
}

// buildPgControl renders a PG-compatible pg_control file image with
// the given system identifier and timestamp. The payload is 8192 bytes
// total; the first 296 bytes are the ControlFileData struct, the rest
// is zero padding (matching upstream's WriteControlFile).
func buildPgControl(systemID uint64, now time.Time, cfg *config.Registry) []byte {
	file := make([]byte, pgControlFileSize)
	hdr := file[:pgControlDataSize]

	// All multi-byte fields are little-endian on x86_64 (PostgreSQL writes
	// in native byte order; goopg matches the host's endianness).
	le := binary.LittleEndian

	// system_identifier: offset 0, uint64
	le.PutUint64(hdr[0:], systemID)
	// pg_control_version: offset 8, uint32
	le.PutUint32(hdr[8:], pgControlVersion)
	// catalog_version_no: offset 12, uint32
	le.PutUint32(hdr[12:], pgCatalogVersionNo)
	// state (DBState, 4 bytes): offset 16
	le.PutUint32(hdr[16:], dbStateShutdowned)
	// time (pg_time_t/int64): offset 24
	le.PutUint64(hdr[24:], uint64(now.Unix()))
	// checkPoint (XLogRecPtr): offset 32.
	// Initial LSN is wal_segment_size + SizeOfXLogLongPHD = 0x01000028.
	le.PutUint64(hdr[32:], pgInitCheckpointLSN)

	// checkPointCopy (CheckPoint struct, 88 bytes): offset 40..128.
	// Mirrors BootStrapXLOG (xlog.c:5115-5132).

	// offset 40: CheckPoint.redo (XLogRecPtr)
	le.PutUint64(hdr[40:], pgInitCheckpointLSN)
	// offset 48: CheckPoint.ThisTimeLineID (uint32) — BootstrapTimeLineID=1
	le.PutUint32(hdr[48:], 1)
	// offset 52: CheckPoint.PrevTimeLineID (uint32) — BootstrapTimeLineID=1
	le.PutUint32(hdr[52:], 1)
	// offset 56: CheckPoint.fullPageWrites (bool) — GUC default on
	hdr[56] = 1
	// offset 57-59: padding (zero)
	// offset 60: CheckPoint.wal_level (int) — live GUC
	le.PutUint32(hdr[60:], walLevelInt(cfg))
	// offset 64: CheckPoint.nextXid (FullTransactionId=uint64) — FirstNormalTransactionId=3
	le.PutUint64(hdr[64:], pgFirstNormalXID)
	// offset 72: CheckPoint.nextOid (Oid=uint32) — FirstGenbkiObjectId=10000
	le.PutUint32(hdr[72:], pgFirstGenbkiOID)
	// offset 76: CheckPoint.nextMulti (MultiXactId=uint32) — FirstMultiXactId=1
	le.PutUint32(hdr[76:], pgFirstMultiXact)
	// offset 80: CheckPoint.nextMultiOffset (MultiXactOffset=uint32) — 0
	le.PutUint32(hdr[80:], 0)
	// offset 84: CheckPoint.oldestXid (TransactionId=uint32) — FirstNormalTransactionId=3
	le.PutUint32(hdr[84:], uint32(pgFirstNormalXID))
	// offset 88: CheckPoint.oldestXidDB (Oid=uint32) — Template1DbOid=1
	le.PutUint32(hdr[88:], pgTemplate1DbOID)
	// offset 92: CheckPoint.oldestMulti (MultiXactId=uint32) — FirstMultiXactId=1
	le.PutUint32(hdr[92:], pgFirstMultiXact)
	// offset 96: CheckPoint.oldestMultiDB (Oid=uint32) — Template1DbOid=1
	le.PutUint32(hdr[96:], pgTemplate1DbOID)
	// offset 100-103: 4-byte alignment padding (zero)
	// offset 104: CheckPoint.time (pg_time_t=int64)
	le.PutUint64(hdr[104:], uint64(now.Unix()))
	// offset 112: CheckPoint.oldestCommitTsXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(hdr[112:], 0)
	// offset 116: CheckPoint.newestCommitTsXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(hdr[116:], 0)
	// offset 120: CheckPoint.oldestActiveXid (TransactionId=uint32) — InvalidTransactionId=0
	le.PutUint32(hdr[120:], 0)
	// offset 124-127: 4-byte tail padding (zero)

	// unloggedLSN (XLogRecPtr): offset 128 — FirstNormalUnloggedLSN=1000
	le.PutUint64(hdr[128:], pgFirstNormalUnloggedLSN)
	// minRecoveryPoint (XLogRecPtr): offset 136
	le.PutUint64(hdr[136:], 0)
	// minRecoveryPointTLI (TimeLineID/uint32): offset 144
	le.PutUint32(hdr[144:], 0)
	// pad 4: offset 148..152
	// backupStartPoint (XLogRecPtr): offset 152
	le.PutUint64(hdr[152:], 0)
	// backupEndPoint (XLogRecPtr): offset 160
	le.PutUint64(hdr[160:], 0)
	// backupEndRequired (bool): offset 168
	hdr[168] = 0
	// pad 3: 169..172
	// wal_level (int): offset 172 — live GUC (replica=1)
	le.PutUint32(hdr[172:], walLevelInt(cfg))
	// wal_log_hints (bool): offset 176
	hdr[176] = 0
	// pad 3: 177..180
	// MaxConnections (int): offset 180 — live GUC
	le.PutUint32(hdr[180:], uint32(gucInt(cfg, "max_connections", 100)))
	// max_worker_processes (int): offset 184 — live GUC
	le.PutUint32(hdr[184:], uint32(gucInt(cfg, "max_worker_processes", 8)))
	// max_wal_senders (int): offset 188 — live GUC
	le.PutUint32(hdr[188:], uint32(gucInt(cfg, "max_wal_senders", 10)))
	// max_prepared_xacts (int): offset 192 — live GUC (max_prepared_transactions)
	le.PutUint32(hdr[192:], uint32(gucInt(cfg, "max_prepared_transactions", 0)))
	// max_locks_per_xact (int): offset 196 — live GUC (max_locks_per_transaction)
	le.PutUint32(hdr[196:], uint32(gucInt(cfg, "max_locks_per_transaction", 64)))
	// track_commit_timestamp (bool): offset 200
	hdr[200] = 0
	// pad 3: 201..204
	// maxAlign (uint32): offset 204
	le.PutUint32(hdr[204:], 8)
	// floatFormat (double): offset 208 — must equal FLOATFORMAT_VALUE
	le.PutUint64(hdr[208:], math.Float64bits(1234567.0))
	// blcksz (uint32): offset 216
	le.PutUint32(hdr[216:], 8192)
	// relseg_size (uint32): offset 220 — 131072 blocks = 1 GiB at 8 KiB
	le.PutUint32(hdr[220:], 131072)
	// xlog_blcksz (uint32): offset 224
	le.PutUint32(hdr[224:], 8192)
	// xlog_seg_size (uint32): offset 228 — 16 MiB
	le.PutUint32(hdr[228:], 16*1024*1024)
	// nameDataLen (uint32): offset 232
	le.PutUint32(hdr[232:], 64)
	// indexMaxKeys (uint32): offset 236
	le.PutUint32(hdr[236:], 32)
	// toast_max_chunk_size (uint32): offset 240
	le.PutUint32(hdr[240:], 1996)
	// loblksize (uint32): offset 244
	le.PutUint32(hdr[244:], 2048)
	// float8ByVal (bool): offset 248 — true on 64-bit platforms
	hdr[248] = 1
	// pad 3: 249..252
	// data_checksum_version (uint32): offset 252 — no checksums
	le.PutUint32(hdr[252:], 0)
	// default_char_signedness (bool): offset 256 — true on x86_64/ARM64 Linux
	hdr[256] = 1
	// mock_authentication_nonce (char[32]): offset 257..289
	if _, err := rand.Read(hdr[257 : 257+pgControlMockNonceLen]); err != nil {
		// crypto/rand on Linux backed by getrandom(2) is reliable; if it
		// fails we still emit a valid (zero-nonce) file rather than abort.
		// pg_controldata accepts any nonce value.
		for i := 0; i < pgControlMockNonceLen; i++ {
			hdr[257+i] = 0
		}
	}
	// pad 3: 289..292
	// crc (uint32): offset 292 — computed over hdr[:292]
	crc := crc32.Checksum(hdr[:pgControlCRCOffset], crcCastagnoliTable)
	le.PutUint32(hdr[pgControlCRCOffset:], crc)

	return file
}
