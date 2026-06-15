// BASE_BACKUP wire-protocol command on the goopg primary (M0102-0002).
//
// Implements the upstream `pg_basebackup`-compatible replication
// command so a libpq client (PostgreSQL's pg_basebackup or any other
// BASE_BACKUP-speaking tool) can clone a goopg primary into a fresh
// data directory. Wire shape mirrors
// postgres/src/backend/backup/basebackup_copy.c's `bbsink_copystream`:
//
//	S → C  RowDescription("recptr", "tli")
//	S → C  DataRow(startLSN, startTLI)
//	S → C  CommandComplete("SELECT")
//	S → C  RowDescription("spcoid","spclocation","size")
//	S → C  DataRow(NULL, NULL, NULL)            -- single default tablespace
//	S → C  CommandComplete("SELECT")
//	S → C  CopyOutResponse(format=0, natts=0)
//	S → C  CopyData('n' archive-name "base.tar"\0 tablespace-path ""\0)
//	S → C  CopyData('d' <tar bytes>)+           -- many frames
//	S → C  CopyData('p' int8be bytes-done)*     -- progress reports
//	S → C  CopyDone
//	S → C  RowDescription("recptr","tli")
//	S → C  DataRow(stopLSN, stopTLI)
//	S → C  CommandComplete("SELECT")
//	S → C  ReadyForQuery
//
// The tar payload is a POSIX ustar archive containing every file
// under `<DataDir>` except the per-process state listed in
// `baseBackupExcluded`. A synthetic `backup_label` is prepended;
// `global/pg_control` is emitted last so a recovering standby sees a
// consistent control file (upstream invariant). Files matching
// `excludeFiles` and the contents of directories matching
// `excludeDirContents` are omitted; the excluded directories themselves
// are shipped as empty tar entries (upstream invariant). Tablespaces
// beyond the default are out of v0 scope — the tablespace list
// result-set always reports a single NULL/NULL row mirroring the
// upstream "default tablespace only" shape.
//
// Verification: `internal/server/basebackup_test.go` drives a server
// through BASE_BACKUP via the in-process protocol harness and asserts
// the tar parses cleanly with `archive/tar` and contains
// `backup_label` + `global/pg_control` + at least one base/<oid>/
// file. End-to-end interop with upstream pg_basebackup is exercised by
// M0102-0006/0007 once the E2E harness lands.
package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/wal"
)

// oidInt8 is pg_type.dat int8 OID; used for the BASE_BACKUP result
// sets that report start/stop TLI. Mirrors upstream's
// `TupleDescInitBuiltinEntry(..., INT8OID, ...)` call at
// postgres/src/backend/backup/basebackup_copy.c:358.
const oidInt8 = 20

// baseBackupArchive is the single archive name emitted in the
// CopyData 'n' frame. Upstream pg_basebackup parses this verbatim
// (postgres/src/bin/pg_basebackup/pg_basebackup.c:`GetBackupArchive`).
const baseBackupArchive = "base.tar"

// baseBackupChunkBytes caps the size of each CopyData 'd' payload.
// Upstream uses a 64 KiB buffer (`bbsink_copystream`'s
// `bbs_buffer_length`); matching that keeps client-side memory
// behavior predictable.
const baseBackupChunkBytes = 64 * 1024

// baseBackupProgressInterval matches upstream's
// PROGRESS_REPORT_BYTE_INTERVAL (1 MiB) — emit a 'p' frame every
// 1 MiB of tar bytes shipped.
const baseBackupProgressInterval = 1 << 20

// excludeFiles lists filenames (base name only) that are never streamed in
// a BASE_BACKUP. When prefix is true the name is a prefix (e.g.
// "pg_internal.init" matches "pg_internal.init", "pg_internal.init.1", …).
// Mirrors upstream's excludeFiles[] in
// postgres/src/backend/backup/basebackup.c:191-225.
var excludeFiles = []struct {
	name   string
	prefix bool
}{
	{"postmaster.pid", false},
	{"postmaster.opts", false},
	{".goopg.ctl.sock", false}, // goopg-specific runtime socket
	{"postgresql.auto.conf.tmp", false},
	{"current_logfiles.tmp", false},
	{"backup_label", false},
	{"tablespace_map", false},
	{"backup_manifest", false},
	{"pg_internal.init", true}, // relcache init file — standby rebuilds on first access
	// M0106-0010 batched-44: drop the legacy goopg "global/pg_xact" flat
	// file (1-byte-per-XID) from the basebackup payload. PG standby reads
	// commit status from the PG-canonical "pg_xact/" SLRU directory at the
	// data-dir root, which IS shipped. The directory check above uses
	// IsDir() before this list, so the top-level "pg_xact" directory is
	// unaffected — this entry only matches the regular file in global/.
	{"pg_xact", false},
}

// excludeDirContents lists directory base names whose contents are never
// streamed. The directory entry itself IS shipped as an empty tar directory
// so a standby's startup code can stat the path without error.
// Mirrors upstream's excludeDirContents[] in basebackup.c:151-186.
var excludeDirContents = map[string]struct{}{
	"pg_replslot":  {}, // slot files are primary-owned; standby rebuilds them
	"pg_stat_tmp":  {}, // per-process stats; recreated on standby start
	"pg_dynshmem":  {}, // cleared by dsm_cleanup_for_mmap
	"pg_notify":    {}, // cleared by AsyncShmemInit
	"pg_serial":    {}, // not required for replay
	"pg_snapshots": {}, // cleared by DeleteAllExportedSnapshotFiles
	"pg_subtrans":  {}, // zeroed by StartupSUBTRANS
}

// isExcludedFile reports whether the file with the given base name should be
// omitted from the backup tar stream.
func isExcludedFile(base string) bool {
	for _, e := range excludeFiles {
		if e.prefix {
			if strings.HasPrefix(base, e.name) {
				return true
			}
		} else if base == e.name {
			return true
		}
	}
	return false
}

// replyBaseBackup is the top-level handler invoked from
// handleReplicationCommand. `args` is the suffix after `BASE_BACKUP `
// (possibly empty for a bare `BASE_BACKUP` command, which upstream
// also accepts).
func (s *Server) replyBaseBackup(ctx context.Context, w *protocol.FrameWriter, args string) error {
	if s.cfg.DataDir == "" {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"BASE_BACKUP requires Config.DataDir to be set")
	}
	opts, err := parseBaseBackupOptions(args)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
	}
	if _, ok := parseManifestChecksumKind(opts.ManifestChecksums); !ok {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("BASE_BACKUP: unsupported manifest checksum type %q", opts.ManifestChecksums))
	}

	// Force a synchronous IMMEDIATE checkpoint so the start-LSN we
	// report names a record whose redo image is on disk. Upstream's
	// `do_pg_backup_start` does the same: a non-spread checkpoint is
	// mandatory before sampling startptr.
	if s.cfg.Checkpointer != nil {
		if err := s.cfg.Checkpointer.CheckpointNow(); err != nil {
			return s.writeQueryError(w, sqlstate.InternalError,
				fmt.Sprintf("BASE_BACKUP: checkpoint: %v", err))
		}
	}

	// Fetch the checkpoint REDO LSN. This is the point from which WAL
	// replay must begin for a consistent backup — pg_basebackup uses
	// it as the START_REPLICATION start position. Fall back to
	// WrittenLSN() if the checkpointer hasn't recorded a REDO yet
	// (e.g. no prior checkpoint).
	// goopg uses 1-based LSNs internally; PG expects 0-based in
	// pg_control and backup_label — subtract 1 for those artefacts.
	redoLSN := uint64(0)
	if s.cfg.Checkpointer != nil {
		redoLSN = s.cfg.Checkpointer.CheckpointRedoLSN()
	}
	if redoLSN == 0 && s.cfg.WAL != nil {
		redoLSN = s.cfg.WAL.WrittenLSN()
	}

	// startLSN is the WAL-range bookend pg_basebackup sends back as
	// the START_REPLICATION position. Must be the checkpoint REDO
	// LSN (not WrittenLSN) so the WAL sender streams from the
	// consistency point, covering every record between the checkpoint
	// and the backup end. M0105-0007.
	startLSN := redoLSN

	// M0102-0007: after the forced checkpoint, patch pg_control on disk
	// with the checkpoint REDO location so the resulting backup's
	// global/pg_control carries a valid checkpoint — PostgreSQL
	// standbys reject pg_control with a zero redo point.
	baseLSN0 := uint64(0)
	if redoLSN > 0 {
		baseLSN0 = redoLSN - 1
		_ = initdb.UpdateControlCheckpoint(s.cfg.DataDir, redoLSN)
	}
	startTLI, err := initdb.LoadOrCreateTimelineID(s.cfg.DataDir)
	if err != nil {
		return s.writeQueryError(w, sqlstate.InternalError,
			fmt.Sprintf("BASE_BACKUP: timeline: %v", err))
	}

	// --- result-set 1: start LSN + TLI ---
	if err := writeRecPtrResult(w, startLSN, startTLI); err != nil {
		return err
	}

	// --- result-set 2: tablespace list (one NULL row for default) ---
	if err := writeTablespaceList(w); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("SELECT"); err != nil {
		return err
	}

	// --- CopyOutResponse: open the archive stream ---
	if err := w.WriteCopyOutResponse(0, nil); err != nil {
		return err
	}
	if err := newArchiveFrame(w, baseBackupArchive, ""); err != nil {
		return err
	}

	// --- stream tar bytes through CopyData 'd' frames ---
	segSize := s.cfg.WALSegmentSize
	if segSize <= 0 {
		segSize = wal.DefaultSegmentSize
	}
	streamer := &baseBackupStreamer{
		w:                w,
		bytesDone:        0,
		nextProgressMark: baseBackupProgressInterval,
		ctx:              ctx,
	}
	wantManifest := opts.Manifest == "yes" || opts.Manifest == "force-encode"
	mck, _ := parseManifestChecksumKind(opts.ManifestChecksums) // validated at entry

	// -X fetch (BASE_BACKUP WAL): compute the inclusive [start,end] WAL
	// segment range to append to the tar. Mirrors basebackup.c:439-442
	// — XLByteToSeg(startptr) .. XLByteToPrevSeg(endptr). startptr is the
	// 0-based redo point (baseLSN0); endptr is the 0-based stop LSN sampled
	// now (emitBaseBackupTar reads files but writes no WAL, so WrittenLSN
	// is stable across the call).
	var walStartSeg, walEndSeg uint64
	if opts.IncludeWAL {
		walStartSeg = baseLSN0 / uint64(segSize)
		endLSN0 := baseLSN0
		if s.cfg.WAL != nil {
			if wlsn := s.cfg.WAL.WrittenLSN(); wlsn > 0 {
				endLSN0 = wlsn - 1
			}
		}
		if endLSN0 == 0 {
			walEndSeg = walStartSeg
		} else {
			walEndSeg = (endLSN0 - 1) / uint64(segSize)
		}
		if walEndSeg < walStartSeg {
			walEndSeg = walStartSeg
		}
	}

	entries, err := emitBaseBackupTar(ctx, streamer, s.cfg.DataDir, opts.Label, baseLSN0, startTLI, segSize, mck, opts.IncludeWAL, walStartSeg, walEndSeg)
	if err != nil {
		return s.writeStreamingError(w, sqlstate.InternalError,
			fmt.Sprintf("BASE_BACKUP: tar: %v", err))
	}
	// Final progress report at end-of-archive (upstream parity).
	if err := writeProgressFrame(w, streamer.bytesDone); err != nil {
		return err
	}

	// --- backup manifest (before CopyDone, matching upstream's
	// SendBackupManifest → bbsink_copystream_begin_manifest sequence).
	// The 'm' frame announces the manifest; its bytes then flow through
	// the same CopyData 'd' framing as the tar. stopLSN is the WAL-range
	// end the manifest records, so compute it before building.
	stopLSN := startLSN
	if s.cfg.WAL != nil {
		stopLSN = s.cfg.WAL.WrittenLSN()
	}
	if wantManifest {
		sysID, err := initdb.LoadOrCreateSystemID(s.cfg.DataDir)
		if err != nil {
			return s.writeStreamingError(w, sqlstate.InternalError,
				fmt.Sprintf("BASE_BACKUP: manifest system id: %v", err))
		}
		stopLSN0 := stopLSN
		if stopLSN0 > 0 {
			stopLSN0--
		}
		manifestBytes := buildBackupManifest(entries, sysID, startTLI, baseLSN0, stopLSN0, opts.Manifest == "force-encode")
		if err := streamBackupManifest(w, manifestBytes); err != nil {
			return s.writeStreamingError(w, sqlstate.InternalError,
				fmt.Sprintf("BASE_BACKUP: manifest: %v", err))
		}
	}

	if err := w.WriteCopyDone(); err != nil {
		return err
	}

	if err := writeRecPtrResult(w, stopLSN, startTLI); err != nil {
		return err
	}
	// EndReplicationCommand parity: upstream walsender wraps every
	// replication verb with a trailing `CommandComplete(<tag>)` via
	// `EndReplicationCommand` (postgres/src/backend/tcop/dest.c). For
	// BASE_BACKUP pg_basebackup reads this frame at line 2199 of
	// pg_basebackup.c and asserts PGRES_COMMAND_OK; omitting it
	// surfaces as "final receive failed: " (empty error) once the
	// stop-LSN row has been parsed because libpq returns NULL on the
	// next PQgetResult.
	if err := w.WriteCommandComplete("BASE_BACKUP"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// writeRecPtrResult emits the (recptr text, tli int8) one-row
// result-set used for both start and stop xlog locations. Mirrors
// `SendXlogRecPtrResult` in basebackup_copy.c.
func writeRecPtrResult(w *protocol.FrameWriter, lsn uint64, tli uint32) error {
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "recptr", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "tli", TypeOID: oidInt8, TypeSize: 8, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	row := [][]byte{
		[]byte(formatLSN(lsn)),
		[]byte(strconv.FormatUint(uint64(tli), 10)),
	}
	if err := w.WriteDataRow(row); err != nil {
		return err
	}
	return w.WriteCommandComplete("SELECT")
}

// writeTablespaceList emits the (spcoid oid, spclocation text, size
// int8) result with a single all-NULL row. The NULL/NULL/NULL shape
// signals "default tablespace only" — upstream's `SendTablespaceList`
// also emits a single row for the default tablespace where spcoid /
// spclocation are NULL.
//
// Note: this does NOT terminate with CommandComplete — caller
// follows with one shared CommandComplete("SELECT") to match
// upstream's `bbsink_copystream_begin_backup` framing.
func writeTablespaceList(w *protocol.FrameWriter) error {
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "spcoid", TypeOID: 26 /* oid */, TypeSize: 4, TypeModifier: -1, Format: 0},
		{Name: "spclocation", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "size", TypeOID: oidInt8, TypeSize: 8, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	return w.WriteDataRow([][]byte{nil, nil, nil})
}

// newArchiveFrame emits the CopyData 'n' header that names the
// upcoming archive. `path` is the absolute tablespace path; for the
// default tablespace this is empty (matching upstream's
// `ti->path == NULL ? "" : ti->path` choice).
func newArchiveFrame(w *protocol.FrameWriter, name, path string) error {
	buf := make([]byte, 0, 2+len(name)+len(path))
	buf = append(buf, 'n')
	buf = append(buf, []byte(name)...)
	buf = append(buf, 0)
	buf = append(buf, []byte(path)...)
	buf = append(buf, 0)
	return w.WriteCopyData(buf)
}

// writeProgressFrame emits the CopyData 'p' progress report.
// Payload is `'p' <int8 big-endian bytes-done>`.
func writeProgressFrame(w *protocol.FrameWriter, bytesDone uint64) error {
	var buf [9]byte
	buf[0] = 'p'
	binary.BigEndian.PutUint64(buf[1:], bytesDone)
	return w.WriteCopyData(buf[:])
}

// baseBackupStreamer adapts CopyData 'd' frame writing to an
// io.Writer interface so `archive/tar` can stream directly. It
// chunks writes to baseBackupChunkBytes and emits periodic 'p'
// progress reports — mirroring `bbsink_copystream_archive_contents`.
type baseBackupStreamer struct {
	w                *protocol.FrameWriter
	bytesDone        uint64
	nextProgressMark uint64
	ctx              context.Context
}

func (s *baseBackupStreamer) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if err := s.ctx.Err(); err != nil {
			return written, err
		}
		n := len(p)
		if n > baseBackupChunkBytes {
			n = baseBackupChunkBytes
		}
		// Build the CopyData payload: 'd' type byte + chunk.
		buf := make([]byte, 1+n)
		buf[0] = 'd'
		copy(buf[1:], p[:n])
		if err := s.w.WriteCopyData(buf); err != nil {
			return written, err
		}
		s.bytesDone += uint64(n)
		written += n
		p = p[n:]
		if s.bytesDone >= s.nextProgressMark {
			if err := writeProgressFrame(s.w, s.bytesDone); err != nil {
				return written, err
			}
			s.nextProgressMark = s.bytesDone + baseBackupProgressInterval
		}
	}
	return written, nil
}

// castagnoliTable is the CRC-32C (Castagnoli) table used for backup
// manifest per-file checksums. PG's default MANIFEST_CHECKSUMS is
// CRC32C; its INIT/FIN convention (init ^0, final XOR ^0) matches Go's
// crc32.Checksum, and pg_checksum_final memcpy's the native (little-
// endian on amd64) 4-byte value before hex-encoding — so the manifest
// hex is the little-endian byte image of the CRC32C value.
var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// manifestChecksumKind enumerates the per-file checksum algorithms the
// backup manifest can carry. Mirrors PG's pg_checksum_type.
type manifestChecksumKind int

const (
	mckCRC32C manifestChecksumKind = iota // PG default
	mckNone
	mckSHA224
	mckSHA256
	mckSHA384
	mckSHA512
)

// parseManifestChecksumKind maps a MANIFEST_CHECKSUMS option value
// (already upper-cased; "" means the client omitted it) to a kind.
// Returns false for an unrecognised algorithm.
func parseManifestChecksumKind(s string) (manifestChecksumKind, bool) {
	switch s {
	case "", "CRC32C":
		return mckCRC32C, true
	case "NONE":
		return mckNone, true
	case "SHA224":
		return mckSHA224, true
	case "SHA256":
		return mckSHA256, true
	case "SHA384":
		return mckSHA384, true
	case "SHA512":
		return mckSHA512, true
	default:
		return mckCRC32C, false
	}
}

// algoName returns the manifest "Checksum-Algorithm" string for a kind,
// matching PG's pg_checksum_type_name output.
func (k manifestChecksumKind) algoName() string {
	switch k {
	case mckCRC32C:
		return "CRC32C"
	case mckSHA224:
		return "SHA224"
	case mckSHA256:
		return "SHA256"
	case mckSHA384:
		return "SHA384"
	case mckSHA512:
		return "SHA512"
	default:
		return "NONE"
	}
}

// checksumFile computes the lowercase-hex per-file checksum for `data`
// under kind `k`. The bool reports whether a checksum is emitted at all
// (false for mckNone, so the caller omits the Checksum-Algorithm fields,
// matching PG's CHECKSUM_TYPE_NONE branch in AddFileToBackupManifest).
func (k manifestChecksumKind) checksumFile(data []byte) (string, bool) {
	switch k {
	case mckNone:
		return "", false
	case mckCRC32C:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], crc32.Checksum(data, castagnoliTable))
		return hex.EncodeToString(b[:]), true
	case mckSHA224:
		sum := sha256.Sum224(data)
		return hex.EncodeToString(sum[:]), true
	case mckSHA256:
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), true
	case mckSHA384:
		sum := sha512.Sum384(data)
		return hex.EncodeToString(sum[:]), true
	case mckSHA512:
		sum := sha512.Sum512(data)
		return hex.EncodeToString(sum[:]), true
	default:
		return "", false
	}
}

// manifestEntry is one Files[] record in the backup manifest: a file
// shipped in the base tar, with the metadata pg_verifybackup needs to
// cross-check it against the extracted backup.
type manifestEntry struct {
	path  string    // slash path relative to the data directory
	size  int64     // byte length as shipped
	mtime time.Time // tar-header mtime (reported as GMT in the manifest)
	algo  string    // "Checksum-Algorithm" value; "" if mckNone
	cksum string    // lowercase-hex digest; "" if mckNone
}

// emitBaseBackupTar walks `dataDir` and writes a POSIX ustar archive
// into `out`. Ordering: `backup_label` first, then everything except
// `global/pg_control` in lexical order, then `global/pg_control`
// last. Excluded entries (see `baseBackupExcluded`) are skipped.
//
// It also returns the per-file metadata for the backup manifest, in the
// same order the files are shipped (PG appends manifest entries as it
// streams). `mck` selects the per-file checksum algorithm. WAL segments
// under pg_wal/ are deliberately omitted from the manifest list — PG
// tracks needed WAL via "WAL-Ranges", never as Files[] entries, and a
// concurrent `-X stream` rewrites those segments on the client side,
// which would otherwise break checksum verification.
//
// pg_wal contents are never part of the directory walk — pg_wal is shipped
// as an empty directory (plus the archive_status/summaries empty subdirs PG
// emits), mirroring basebackup.c sendDir():1385-1407. When `includeWAL` is
// set (the BASE_BACKUP `WAL` option / `pg_basebackup -X fetch`), the in-range
// segments [walStartSeg, walEndSeg] are appended to the still-open tar after
// the data files, mirroring the basebackup.c:408-560 includewal block.
func emitBaseBackupTar(ctx context.Context, out io.Writer, dataDir, label string, startLSN uint64, tli uint32, segSize int64, mck manifestChecksumKind, includeWAL bool, walStartSeg, walEndSeg uint64) ([]manifestEntry, error) {
	tw := tar.NewWriter(out)

	var manifest []manifestEntry
	record := func(name string, data []byte, mtime time.Time) {
		slash := filepath.ToSlash(name)
		// WAL segments are manifest-tracked via WAL-Ranges, not Files[].
		if slash == "pg_wal" || strings.HasPrefix(slash, "pg_wal/") {
			return
		}
		algo, cksum := "", ""
		if name, ok := mck.checksumFile(data); ok {
			algo, cksum = mck.algoName(), name
		}
		manifest = append(manifest, manifestEntry{
			path:  slash,
			size:  int64(len(data)),
			mtime: mtime,
			algo:  algo,
			cksum: cksum,
		})
	}

	// 1. backup_label (synthetic).
	labelMTime := time.Now()
	labelBytes := buildBackupLabel(label, startLSN, tli, segSize)
	if err := writeTarFile(tw, "backup_label", labelBytes, labelMTime); err != nil {
		return nil, err
	}
	record("backup_label", labelBytes, labelMTime)

	// 2. Collect entries up-front so we can defer pg_control to the
	// end and skip excluded paths. We don't need to sort — tar
	// readers accept any ordering — but a deterministic walk makes
	// the unit test golden-file friendly.
	var pgControlPath string
	type entry struct {
		abs    string
		rel    string
		info   fs.FileInfo
		isFile bool
	}
	var entries []entry
	err := filepath.Walk(dataDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Defer pg_control to the end; record but don't queue.
		if rel == filepath.Join("global", "pg_control") {
			pgControlPath = path
			return nil
		}
		// pg_wal is never walked: ship it as an empty directory (plus the
		// archive_status/summaries empty subdirs PG also emits) so a
		// recovering standby can stat the paths. WAL segments are appended
		// separately, only when the client requested WAL inclusion
		// (-X fetch). Mirrors basebackup.c sendDir():1385-1407.
		if filepath.ToSlash(rel) == "pg_wal" && info.IsDir() {
			entries = append(entries, entry{abs: path, rel: rel, info: info, isFile: false})
			entries = append(entries, entry{abs: "", rel: filepath.Join("pg_wal", "archive_status"), info: info, isFile: false})
			entries = append(entries, entry{abs: "", rel: filepath.Join("pg_wal", "summaries"), info: info, isFile: false})
			return filepath.SkipDir
		}
		base := filepath.Base(rel)
		if info.IsDir() {
			if _, skip := excludeDirContents[base]; skip {
				// Ship the directory entry itself as an empty tar dir
				// so standby startup code can stat the path, but omit
				// all contents (slots, stats, etc. are primary-owned).
				entries = append(entries, entry{abs: path, rel: rel, info: info, isFile: false})
				return filepath.SkipDir
			}
		} else {
			if isExcludedFile(base) {
				return nil
			}
		}
		entries = append(entries, entry{abs: path, rel: rel, info: info, isFile: !info.IsDir()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.isFile {
			data, err := os.ReadFile(e.abs)
			if err != nil {
				return nil, err
			}
			if err := writeTarFileWithMode(tw, e.rel, data, e.info.ModTime(), e.info.Mode().Perm()); err != nil {
				return nil, err
			}
			record(e.rel, data, e.info.ModTime())
		} else {
			if err := writeTarDir(tw, e.rel+"/", e.info.ModTime(), e.info.Mode().Perm()); err != nil {
				return nil, err
			}
		}
	}

	// 3. pg_control last (upstream invariant for atomic recovery).
	if pgControlPath != "" {
		data, err := os.ReadFile(pgControlPath)
		if err != nil {
			return nil, err
		}
		pgControlMTime := time.Now()
		if err := writeTarFileWithMode(tw, filepath.Join("global", "pg_control"), data, pgControlMTime, 0o600); err != nil {
			return nil, err
		}
		record(filepath.Join("global", "pg_control"), data, pgControlMTime)
	}

	// 4. WAL inclusion (-X fetch): append the [walStartSeg, walEndSeg]
	// segments to the still-open tar under pg_wal/, plus any timeline
	// history files. Mirrors the basebackup.c:408-560 includewal block,
	// including its contiguity sanity check. Appended WAL is intentionally
	// NOT recorded in the manifest Files[] — PG tracks it via WAL-Ranges.
	if includeWAL {
		if err := appendWALSegments(tw, dataDir, tli, segSize, walStartSeg, walEndSeg); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// appendWALSegments scans <dataDir>/pg_wal and appends the WAL segments in
// the inclusive segment range [startSeg, endSeg] to the open tar, oldest
// first, followed by any timeline history files. It mirrors upstream's
// includewal logic: it enforces that the range is fully covered with no
// gaps (basebackup.c:484-520) and errors otherwise, since a backup that
// requested WAL but cannot supply the consistency range is unusable.
func appendWALSegments(tw *tar.Writer, dataDir string, tli uint32, segSize int64, startSeg, endSeg uint64) error {
	walDir := filepath.Join(dataDir, "pg_wal")
	des, err := os.ReadDir(walDir)
	if err != nil {
		return err
	}
	type walSeg struct {
		name  string
		segno uint64
	}
	var segs []walSeg
	var historyFiles []string
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if _, segno, ok := wal.ParseXLogFileName(name, segSize); ok {
			if segno >= startSeg && segno <= endSeg {
				segs = append(segs, walSeg{name: name, segno: segno})
			}
		} else if strings.HasSuffix(name, ".history") {
			historyFiles = append(historyFiles, name)
		}
	}
	// Oldest first, so a segment is shipped before it can be recycled
	// (compareWalFileNames in basebackup.c).
	sort.Slice(segs, func(i, j int) bool { return segs[i].segno < segs[j].segno })

	// Contiguity sanity check: the range must be fully covered.
	if len(segs) == 0 || segs[0].segno != startSeg {
		return fmt.Errorf("could not find WAL file %q", wal.XLogFileName(tli, startSeg, segSize))
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].segno != segs[i-1].segno+1 {
			return fmt.Errorf("could not find WAL file %q", wal.XLogFileName(tli, segs[i-1].segno+1, segSize))
		}
	}
	if segs[len(segs)-1].segno != endSeg {
		return fmt.Errorf("could not find WAL file %q", wal.XLogFileName(tli, endSeg, segSize))
	}

	for _, ws := range segs {
		data, err := os.ReadFile(filepath.Join(walDir, ws.name))
		if err != nil {
			return err
		}
		if err := writeTarFileWithMode(tw, "pg_wal/"+ws.name, data, time.Now(), 0o600); err != nil {
			return err
		}
	}
	// Timeline history files are tiny and useful for debugging; PG always
	// ships them all (basebackup.c:608-635). goopg single-timeline clusters
	// usually have none.
	sort.Strings(historyFiles)
	for _, h := range historyFiles {
		data, err := os.ReadFile(filepath.Join(walDir, h))
		if err != nil {
			return err
		}
		if err := writeTarFileWithMode(tw, "pg_wal/"+h, data, time.Now(), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeTarFile / writeTarFileWithMode / writeTarDir are thin
// helpers over archive/tar that pin the upstream POSIX-ustar
// conventions: uid/gid 0, sensible defaults for missing fields.
func writeTarFile(tw *tar.Writer, name string, content []byte, mtime time.Time) error {
	return writeTarFileWithMode(tw, name, content, mtime, 0o600)
}

func writeTarFileWithMode(tw *tar.Writer, name string, content []byte, mtime time.Time, mode os.FileMode) error {
	hdr := &tar.Header{
		Name:     filepath.ToSlash(name),
		Size:     int64(len(content)),
		Mode:     int64(mode),
		ModTime:  mtime,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

func writeTarDir(tw *tar.Writer, name string, mtime time.Time, mode os.FileMode) error {
	hdr := &tar.Header{
		Name:     filepath.ToSlash(name),
		Mode:     int64(mode),
		ModTime:  mtime,
		Typeflag: tar.TypeDir,
		Format:   tar.FormatUSTAR,
	}
	return tw.WriteHeader(hdr)
}

// buildBackupLabel constructs the synthetic backup_label file
// contents per upstream `build_backup_content` in
// postgres/src/backend/access/transam/xlogbackup.c. The CHECKPOINT
// LOCATION equals the start LSN — goopg's checkpointer reports the
// LSN of the redo point on completion, which is the start LSN we
// just sampled.
func buildBackupLabel(label string, startLSN uint64, tli uint32, segSize int64) []byte {
	if label == "" {
		label = "goopg base backup"
	}
	segno := startLSN / uint64(segSize)
	walFile := wal.XLogFileName(tli, segno, segSize)
	var b bytes.Buffer
	fmt.Fprintf(&b, "START WAL LOCATION: %s (file %s)\n", formatLSN(startLSN), walFile)
	fmt.Fprintf(&b, "CHECKPOINT LOCATION: %s\n", formatLSN(startLSN))
	fmt.Fprintf(&b, "BACKUP METHOD: streamed\n")
	fmt.Fprintf(&b, "BACKUP FROM: primary\n")
	fmt.Fprintf(&b, "START TIME: %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "LABEL: %s\n", label)
	fmt.Fprintf(&b, "START TIMELINE: %d\n", tli)
	return b.Bytes()
}

// buildBackupManifest renders a PG-version-2 backup manifest (the JSON
// document pg_basebackup writes to `backup_manifest` and pg_verifybackup
// validates). Byte-for-byte field ordering and whitespace mirror
// backup_manifest.c so the trailing SHA-256 "Manifest-Checksum" — which
// is computed over every byte up to but not including that field — comes
// out identical to upstream.
//
//	{ "PostgreSQL-Backup-Manifest-Version": 2,
//	"System-Identifier": <sysid>,
//	"Files": [
//	{ "Path": ..., "Size": ..., "Last-Modified": ..., "Checksum-Algorithm": ..., "Checksum": ... },
//	...
//	],
//	"WAL-Ranges": [
//	{ "Timeline": <tli>, "Start-LSN": "X/X", "End-LSN": "X/X" }
//	],
//	"Manifest-Checksum": "<sha256hex>"}
//
// forceEncode hex-encodes every path (PG's MANIFEST_OPTION_FORCE_ENCODE /
// --manifest-force-encode); otherwise valid-UTF-8 paths use "Path" and
// only non-UTF-8 paths fall back to "Encoded-Path".
func buildBackupManifest(entries []manifestEntry, sysID uint64, tli uint32, startLSN, endLSN uint64, forceEncode bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "{ \"PostgreSQL-Backup-Manifest-Version\": 2,\n")
	fmt.Fprintf(&b, "\"System-Identifier\": %d,\n", sysID)
	b.WriteString("\"Files\": [")
	for i, e := range entries {
		if i == 0 {
			b.WriteByte('\n')
		} else {
			b.WriteString(",\n")
		}
		if forceEncode || !utf8.ValidString(e.path) {
			fmt.Fprintf(&b, "{ \"Encoded-Path\": \"%s\", ", hex.EncodeToString([]byte(e.path)))
		} else {
			fmt.Fprintf(&b, "{ \"Path\": %s, ", encodeJSONString(e.path))
		}
		fmt.Fprintf(&b, "\"Size\": %d, ", e.size)
		fmt.Fprintf(&b, "\"Last-Modified\": \"%s\"", e.mtime.UTC().Format("2006-01-02 15:04:05")+" GMT")
		if e.algo != "" {
			fmt.Fprintf(&b, ", \"Checksum-Algorithm\": \"%s\", \"Checksum\": \"%s\"", e.algo, e.cksum)
		}
		b.WriteString(" }")
	}
	// Terminate Files[] and open WAL-Ranges (AddWALInfoToBackupManifest).
	b.WriteString("\n],\n")
	b.WriteString("\"WAL-Ranges\": [\n")
	fmt.Fprintf(&b, "{ \"Timeline\": %d, \"Start-LSN\": \"%s\", \"End-LSN\": \"%s\" }",
		tli, formatLSN(startLSN), formatLSN(endLSN))
	b.WriteString("\n],\n")

	// SHA-256 over everything written so far (always SHA-256 regardless
	// of the per-file checksum algorithm — SendBackupManifest invariant).
	sum := sha256.Sum256(b.Bytes())
	fmt.Fprintf(&b, "\"Manifest-Checksum\": \"%s\"}\n", hex.EncodeToString(sum[:]))
	return b.Bytes()
}

// encodeJSONString renders s as a JSON string literal (including the
// surrounding quotes), matching PG's escape_json for the manifest path.
func encodeJSONString(s string) string {
	buf, _ := json.Marshal(s)
	return string(buf)
}

// streamBackupManifest sends the manifest over the open CopyOut stream:
// first the 'm' begin-manifest marker (bbsink_copystream_begin_manifest),
// then the manifest bytes in 'd' CopyData frames chunked like the tar
// stream (bbsink_copystream_manifest_contents). No explicit terminator —
// the caller's CopyDone closes the stream.
func streamBackupManifest(w *protocol.FrameWriter, manifest []byte) error {
	if err := w.WriteCopyData([]byte{'m'}); err != nil {
		return err
	}
	for len(manifest) > 0 {
		n := len(manifest)
		if n > baseBackupChunkBytes {
			n = baseBackupChunkBytes
		}
		buf := make([]byte, 1+n)
		buf[0] = 'd'
		copy(buf[1:], manifest[:n])
		if err := w.WriteCopyData(buf); err != nil {
			return err
		}
		manifest = manifest[n:]
	}
	return nil
}

// baseBackupOptions is the parsed form of the optional `(...)` block.
type baseBackupOptions struct {
	Label    string
	Progress bool
	Manifest string // "yes" | "no" | "force-encode" | ""
	// ManifestChecksums names the per-file checksum algorithm the
	// client requested for the manifest (PG's MANIFEST_CHECKSUMS
	// option). Empty means the client did not send it; pg_basebackup
	// defaults to CRC32C. Recognised: "NONE", "CRC32C", "SHA224",
	// "SHA256", "SHA384", "SHA512" (case-insensitive).
	ManifestChecksums string
	Target            string // "client" by default; goopg ignores non-client targets
	Wait              int    // 0 means "don't wait for WAL"; goopg treats both the same
	// IncludeWAL is the upstream BASE_BACKUP `WAL` boolean. pg_basebackup
	// sets it for `-X fetch` (FETCH_WAL) — see pg_basebackup.c:1905-1906
	// (`AppendPlainCommandOption(&buf, ..., "WAL")`). When set the server
	// appends the in-range pg_wal segments to the backup tar instead of
	// relying on a concurrent `-X stream` walsender connection.
	IncludeWAL bool
}

// parseBaseBackupOptions parses upstream PG17+ grammar
// `BASE_BACKUP [( opt ['val'], ... )]`. Legacy keyword form
// (`BASE_BACKUP LABEL 'x' PROGRESS ...`) is also accepted because
// older clients still emit it; both shapes map onto the same struct.
func parseBaseBackupOptions(raw string) (baseBackupOptions, error) {
	var opts baseBackupOptions
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ";")
	if raw == "" {
		return opts, nil
	}
	if strings.HasPrefix(raw, "(") {
		end := strings.LastIndex(raw, ")")
		if end < 0 {
			return opts, errors.New("BASE_BACKUP: unterminated '(' in options")
		}
		body := raw[1:end]
		return parseBaseBackupOptionList(body)
	}
	return parseBaseBackupLegacyKeywords(raw)
}

func parseBaseBackupOptionList(body string) (baseBackupOptions, error) {
	var opts baseBackupOptions
	parts := splitOptionsCSV(body)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key, val := splitOptionKV(p)
		switch strings.ToUpper(key) {
		case "LABEL":
			opts.Label = trimSingleQuotes(val)
		case "PROGRESS":
			opts.Progress = true
		case "MANIFEST":
			opts.Manifest = strings.ToLower(trimSingleQuotes(val))
		case "TARGET":
			opts.Target = strings.ToLower(trimSingleQuotes(val))
		case "WAIT":
			if val != "" {
				n, err := strconv.Atoi(strings.TrimSpace(val))
				if err != nil {
					return opts, fmt.Errorf("BASE_BACKUP: invalid WAIT value %q", val)
				}
				opts.Wait = n
			}
		case "MANIFEST_CHECKSUMS":
			opts.ManifestChecksums = strings.ToUpper(trimSingleQuotes(val))
		case "WAL":
			// PG15+ sends `WAL` as a bare boolean option (no value) for
			// `-X fetch`. defGetBoolean treats a value-less option as true;
			// honour an explicit false ('f'/'off'/'0') for completeness.
			opts.IncludeWAL = parseOptionBool(val, true)
		case "CHECKPOINT", "TABLESPACE_MAP", "VERIFY_CHECKSUMS", "MAX_RATE",
			"NOVERIFY_CHECKSUMS", "COMPRESSION",
			"COMPRESSION_DETAIL", "INCREMENTAL":
			// Accept but ignore — they don't affect what the v0
			// emitter produces and rejecting them would break
			// vanilla pg_basebackup invocations.
		default:
			// Unknown keys are tolerated for forward compatibility
			// with upstream additions.
		}
	}
	return opts, nil
}

func parseBaseBackupLegacyKeywords(raw string) (baseBackupOptions, error) {
	var opts baseBackupOptions
	toks := tokenizeLegacyOptions(raw)
	for i := 0; i < len(toks); i++ {
		t := strings.ToUpper(toks[i])
		switch t {
		case "LABEL":
			if i+1 < len(toks) {
				opts.Label = trimSingleQuotes(toks[i+1])
				i++
			}
		case "PROGRESS":
			opts.Progress = true
		case "WAL":
			// Legacy walsender clients (`BASE_BACKUP ... WAL`) request
			// in-tar WAL with a bare keyword; it never carries a value.
			opts.IncludeWAL = true
		case "MANIFEST":
			if i+1 < len(toks) {
				opts.Manifest = strings.ToLower(trimSingleQuotes(toks[i+1]))
				i++
			}
		case "WAIT":
			if i+1 < len(toks) {
				if n, err := strconv.Atoi(toks[i+1]); err == nil {
					opts.Wait = n
				}
				i++
			}
		}
	}
	return opts, nil
}

// parseOptionBool interprets a BASE_BACKUP boolean option value the way
// upstream defGetBoolean does: an absent value means "present, so true",
// while an explicit value is parsed as a PG boolean literal.
func parseOptionBool(val string, defaultWhenEmpty bool) bool {
	val = strings.TrimSpace(trimSingleQuotes(val))
	if val == "" {
		return defaultWhenEmpty
	}
	switch strings.ToLower(val) {
	case "f", "false", "off", "0", "n", "no":
		return false
	default:
		return true
	}
}

// splitOptionsCSV splits at commas that aren't inside single quotes.
func splitOptionsCSV(body string) []string {
	var out []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch c {
		case '\'':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		case ',':
			if !inQuote && depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, body[start:])
	return out
}

func splitOptionKV(p string) (string, string) {
	// "KEY 'value'" or "KEY value" or bare "KEY".
	sp := strings.IndexAny(p, " \t")
	if sp < 0 {
		return p, ""
	}
	return strings.TrimSpace(p[:sp]), strings.TrimSpace(p[sp+1:])
}

func trimSingleQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

// tokenizeLegacyOptions splits on whitespace while keeping
// single-quoted strings together.
func tokenizeLegacyOptions(raw string) []string {
	var out []string
	inQuote := false
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
			cur.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}
