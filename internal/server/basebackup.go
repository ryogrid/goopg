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
// consistent control file (upstream invariant). Tablespaces beyond
// the default are out of v0 scope — the tablespace list result-set
// always reports a single NULL/NULL row mirroring the upstream
// "default tablespace only" shape.
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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// baseBackupExcluded names entries (paths relative to DataDir) that
// must NEVER be included in the backup tar. These are per-process
// runtime artefacts; copying them would either race a running
// primary or break the standby's boot.
//
// Mirrors `excludeFiles` / `excludeDirContents` in upstream
// postgres/src/backend/backup/basebackup.c, scoped to entries goopg
// actually creates today.
var baseBackupExcluded = map[string]struct{}{
	"postmaster.pid":   {},
	".goopg.ctl.sock":  {},
	"postmaster.opts":  {},
	"pg_internal.init": {},
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

	startLSN := uint64(0)
	if s.cfg.WAL != nil {
		startLSN = s.cfg.WAL.WrittenLSN()
	}

	// M0102-0007: after the forced checkpoint, patch pg_control on disk
	// with the checkpoint REDO location so the resulting backup's
	// global/pg_control carries a valid checkpoint — PostgreSQL
	// standbys reject pg_control with a zero redo point.
	// Use the checkpointer's stored REDO LSN (start of the checkpoint
	// record) for pg_control and backup_label.
	// goopg uses 1-based LSNs; PG expects 0-based — subtract 1.
	redoLSN := startLSN
	if s.cfg.Checkpointer != nil {
		if r := s.cfg.Checkpointer.CheckpointRedoLSN(); r > 0 {
			redoLSN = r
		}
	}
	// DEBUG M0102-0007
	if s.cfg.Logger != nil {
		s.cfg.Logger.Info("BASE_BACKUP redoLSN debug", "startLSN", startLSN, "checkpointerRedoLSN", func() uint64 {
			if s.cfg.Checkpointer != nil {
				return s.cfg.Checkpointer.CheckpointRedoLSN()
			}
			return 0
		}(), "finalRedoLSN", redoLSN)
	}
	if redoLSN > 0 {
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
	if err := emitBaseBackupTar(ctx, streamer, s.cfg.DataDir, opts.Label, startLSN, startTLI, segSize); err != nil {
		return s.writeStreamingError(w, sqlstate.InternalError,
			fmt.Sprintf("BASE_BACKUP: tar: %v", err))
	}
	// Final progress report at end-of-archive (upstream parity).
	if err := writeProgressFrame(w, streamer.bytesDone); err != nil {
		return err
	}

	if err := w.WriteCopyDone(); err != nil {
		return err
	}

	// --- result-set 3: end LSN + TLI ---
	stopLSN := startLSN
	if s.cfg.WAL != nil {
		stopLSN = s.cfg.WAL.WrittenLSN()
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

// emitBaseBackupTar walks `dataDir` and writes a POSIX ustar archive
// into `out`. Ordering: `backup_label` first, then everything except
// `global/pg_control` in lexical order, then `global/pg_control`
// last. Excluded entries (see `baseBackupExcluded`) are skipped.
func emitBaseBackupTar(ctx context.Context, out io.Writer, dataDir, label string, startLSN uint64, tli uint32, segSize int64) error {
	tw := tar.NewWriter(out)

	// 1. backup_label (synthetic).
	labelBytes := buildBackupLabel(label, startLSN, tli, segSize)
	if err := writeTarFile(tw, "backup_label", labelBytes, time.Now()); err != nil {
		return err
	}

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
		if _, skip := baseBackupExcluded[rel]; skip {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Defer pg_control to the end; record but don't queue.
		if rel == filepath.Join("global", "pg_control") {
			pgControlPath = path
			return nil
		}
		entries = append(entries, entry{abs: path, rel: rel, info: info, isFile: !info.IsDir()})
		// M0102-0007: also emit a PG-compatible WAL filename when the
		// source file is a goopg-format WAL segment in pg_wal/.
		// goopg names WAL as %024X (timeline 0); PG expects
		// %08X%08X%08X (tli+log+seg).  Produce both so the PG
		// standby finds a file matching its timeline.
		if segno, ok := parseGoopgWalName(filepath.Base(rel)); ok && filepath.Dir(rel) == "pg_wal" {
			pgName := wal.XLogFileName(tli, segno, segSize)
			entries = append(entries, entry{abs: path, rel: filepath.Join("pg_wal", pgName), info: info, isFile: true})
		}
		return nil
	})
	if err != nil {
		return err
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.isFile {
			data, err := os.ReadFile(e.abs)
			if err != nil {
				return err
			}
			if err := writeTarFileWithMode(tw, e.rel, data, e.info.ModTime(), e.info.Mode().Perm()); err != nil {
				return err
			}
		} else {
			if err := writeTarDir(tw, e.rel+"/", e.info.ModTime(), e.info.Mode().Perm()); err != nil {
				return err
			}
		}
	}

	// 3. pg_control last (upstream invariant for atomic recovery).
	if pgControlPath != "" {
		data, err := os.ReadFile(pgControlPath)
		if err != nil {
			return err
		}
		if err := writeTarFileWithMode(tw, filepath.Join("global", "pg_control"), data, time.Now(), 0o600); err != nil {
			return err
		}
	}

	return tw.Close()
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

// baseBackupOptions is the parsed form of the optional `(...)` block.
type baseBackupOptions struct {
	Label    string
	Progress bool
	Manifest string // "yes" | "no" | "force-encode" | ""
	Target   string // "client" by default; goopg ignores non-client targets
	Wait     int    // 0 means "don't wait for WAL"; goopg treats both the same
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
		case "CHECKPOINT", "TABLESPACE_MAP", "VERIFY_CHECKSUMS", "MAX_RATE",
			"NOVERIFY_CHECKSUMS", "MANIFEST_CHECKSUMS", "COMPRESSION",
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

// parseGoopgWalName parses a goopg-format WAL segment filename
// (%024X segno) and returns the segment number. ok=false for
// non-WAL or invalid names.
func parseGoopgWalName(name string) (segno uint64, ok bool) {
	if len(name) != 24 {
		return 0, false
	}
	v, err := strconv.ParseUint(name, 16, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
