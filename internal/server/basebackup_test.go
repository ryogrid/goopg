package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// startBaseBackupTestServer brings up a Server with DataDir wired so
// BASE_BACKUP has files to stream. The server has no Catalog/Pool/TxnMgr
// (replication path only); we lay down a tiny set of files matching
// the upstream cluster layout: PG_VERSION, postgresql.conf, base/<oid>/<rel>,
// global/pg_control, plus a couple of empty subdirs.
func startBaseBackupTestServer(t *testing.T) (string, string, func()) {
	t.Helper()
	dataDir := t.TempDir()
	// Minimal cluster file set — enough to exercise the walker /
	// tar emitter / pg_control-last invariant.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("18\n"), 0o600))
	must(os.WriteFile(filepath.Join(dataDir, "postgresql.conf"), []byte("# goopg\n"), 0o600))
	must(os.MkdirAll(filepath.Join(dataDir, "base", "1"), 0o700))
	must(os.WriteFile(filepath.Join(dataDir, "base", "1", "1259"), bytes.Repeat([]byte{0x42}, 200*1024), 0o600))
	must(os.MkdirAll(filepath.Join(dataDir, "global"), 0o700))
	// 8 KiB pg_control stub — content doesn't matter for the test,
	// only that it's emitted last.
	must(os.WriteFile(filepath.Join(dataDir, "global", "pg_control"), bytes.Repeat([]byte{0xC0}, 8192), 0o600))

	// --- excludeFiles fixtures: must NOT appear in tar ---
	must(os.WriteFile(filepath.Join(dataDir, "postmaster.pid"), []byte("99999\n"), 0o600))
	must(os.WriteFile(filepath.Join(dataDir, ".goopg.ctl.sock"), []byte(""), 0o600))
	must(os.WriteFile(filepath.Join(dataDir, "postgresql.auto.conf.tmp"), []byte("x=1\n"), 0o600))
	must(os.WriteFile(filepath.Join(dataDir, "current_logfiles.tmp"), []byte("tmp\n"), 0o600))
	must(os.WriteFile(filepath.Join(dataDir, "backup_manifest"), []byte("{}\n"), 0o600))
	// pg_internal.init prefix match — file is inside base/1/ to prove
	// the check applies to the base name, not just the top-level path.
	must(os.WriteFile(filepath.Join(dataDir, "base", "1", "pg_internal.init"), []byte("init\n"), 0o600))

	// --- excludeDirContents fixtures: directory present, contents absent ---
	must(os.MkdirAll(filepath.Join(dataDir, "pg_replslot", "s1"), 0o700))
	must(os.WriteFile(filepath.Join(dataDir, "pg_replslot", "s1", "state"), []byte("slot\n"), 0o600))
	must(os.MkdirAll(filepath.Join(dataDir, "pg_stat_tmp"), 0o700))
	must(os.WriteFile(filepath.Join(dataDir, "pg_stat_tmp", "pgstat.stat"), []byte("stat\n"), 0o600))

	// Seed a timeline_id so the BASE_BACKUP reply doesn't need to
	// generate one mid-stream (which would race with another reader).
	must(initdb.WriteTimelineID(dataDir, 1))

	walDir := filepath.Join(dataDir, "pg_wal")
	must(os.MkdirAll(walDir, 0o700))
	walWriter, err := wal.NewWriter(wal.Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}

	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		DataDir:          dataDir,
		WAL:              walWriter,
		WALDirPath:       walDir,
		WALSegmentSize:   4096,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	addr := srv.Addr().String()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
		_ = walWriter.Close()
	}
	return addr, dataDir, stop
}

// TestBaseBackupWireProtocolFraming drives the BASE_BACKUP command end-
// to-end and validates the wire shape mirrors upstream's
// bbsink_copystream: two prelude result-sets + CommandComplete +
// CopyOutResponse + 'n' archive header + 'd' tar bytes + 'p' progress
// + CopyDone + trailing result-set + ReadyForQuery.
func TestBaseBackupWireProtocolFraming(t *testing.T) {
	addr, dataDir, stop := startBaseBackupTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, "BASE_BACKUP (LABEL 'unit-test', PROGRESS, TARGET 'client')")

	// Drain everything up to ReadyForQuery.
	frames := readUntilReadyForQuery(t, r)
	if len(frames) < 10 {
		t.Fatalf("frame count = %d, want at least 10 (types=%s)",
			len(frames), replicationFrameTypes(frames))
	}

	// ---- prelude: start-LSN result-set ----
	if frames[0].Type != protocol.MsgRowDescription {
		t.Fatalf("frame[0] = %c, want T", frames[0].Type)
	}
	if frames[1].Type != protocol.MsgDataRow {
		t.Fatalf("frame[1] = %c, want D", frames[1].Type)
	}
	if frames[2].Type != protocol.MsgCommandComplete {
		t.Fatalf("frame[2] = %c, want C", frames[2].Type)
	}

	// ---- tablespace list ----
	if frames[3].Type != protocol.MsgRowDescription {
		t.Fatalf("frame[3] = %c, want T (tablespace list)", frames[3].Type)
	}
	if frames[4].Type != protocol.MsgDataRow {
		t.Fatalf("frame[4] = %c, want D (tablespace row)", frames[4].Type)
	}
	tsCells := decodeDataRow(t, frames[4].Payload)
	if len(tsCells) != 3 {
		t.Fatalf("tablespace row columns = %d, want 3", len(tsCells))
	}
	for i, c := range tsCells {
		if c != nil {
			t.Errorf("tablespace cell[%d] = %q, want NULL", i, c)
		}
	}
	if frames[5].Type != protocol.MsgCommandComplete {
		t.Fatalf("frame[5] = %c, want C", frames[5].Type)
	}

	// ---- CopyOutResponse + CopyData stream + CopyDone ----
	if frames[6].Type != protocol.MsgCopyOutResponse {
		t.Fatalf("frame[6] = %c, want H (CopyOutResponse)", frames[6].Type)
	}
	// Iterate CopyData / CopyDone frames, then assert trailing
	// result-set + ReadyForQuery.
	var (
		archiveName string
		archivePath string
		gotProgress int
		tarBytes    bytes.Buffer
		copyDoneIdx = -1
	)
	for i := 7; i < len(frames); i++ {
		f := frames[i]
		if f.Type == protocol.MsgCopyData {
			if len(f.Payload) == 0 {
				t.Fatalf("CopyData frame[%d] has empty payload", i)
			}
			switch f.Payload[0] {
			case 'n':
				// archive_name\0 tablespace_path\0
				rest := f.Payload[1:]
				zero := bytes.IndexByte(rest, 0)
				if zero < 0 {
					t.Fatalf("malformed 'n' frame: no NUL after name")
				}
				archiveName = string(rest[:zero])
				rest = rest[zero+1:]
				zero = bytes.IndexByte(rest, 0)
				if zero >= 0 {
					archivePath = string(rest[:zero])
				}
			case 'd':
				tarBytes.Write(f.Payload[1:])
			case 'p':
				if len(f.Payload) != 9 {
					t.Errorf("'p' frame size = %d, want 9", len(f.Payload))
				}
				gotProgress++
				// Sanity: bytes-done is non-decreasing — caller
				// gets a chance to drive a UI. We don't enforce
				// strict equality; just that the field decodes.
				_ = binary.BigEndian.Uint64(f.Payload[1:])
			default:
				t.Errorf("unexpected CopyData type byte %q", f.Payload[0])
			}
			continue
		}
		if f.Type == protocol.MsgCopyDone {
			copyDoneIdx = i
			break
		}
		t.Fatalf("frame[%d] = %c during copy-stream (want d/n/p or CopyDone)", i, f.Type)
	}
	if copyDoneIdx < 0 {
		t.Fatalf("never saw CopyDone")
	}
	if archiveName != "base.tar" {
		t.Errorf("archive name = %q, want base.tar", archiveName)
	}
	if archivePath != "" {
		t.Errorf("archive path = %q, want empty (default tablespace)", archivePath)
	}
	if gotProgress == 0 {
		t.Errorf("no 'p' progress frames received (want >= 1 at end-of-archive)")
	}

	// ---- trailer: end-LSN result-set, BASE_BACKUP CommandComplete, ReadyForQuery.
	// Upstream emits T/D/C for the stop-LSN row (SendXlogRecPtrResult)
	// then walsender wraps the command with EndReplicationCommand
	// (dest.c) producing a trailing CommandComplete("BASE_BACKUP")
	// before ReadyForQuery. pg_basebackup line 2199 reads that final
	// 'C' as PGRES_COMMAND_OK; missing it surfaces as
	// "final receive failed: " (empty error).
	tail := frames[copyDoneIdx+1:]
	if len(tail) != 5 {
		t.Fatalf("trailer frame count = %d, want 5 (got types=%s)",
			len(tail), replicationFrameTypes(tail))
	}
	if tail[0].Type != protocol.MsgRowDescription ||
		tail[1].Type != protocol.MsgDataRow ||
		tail[2].Type != protocol.MsgCommandComplete ||
		tail[3].Type != protocol.MsgCommandComplete ||
		tail[4].Type != protocol.MsgReadyForQuery {
		t.Errorf("trailer types = %s, want T/D/C/C/Z", replicationFrameTypes(tail))
	}

	// ---- tar contents: backup_label present, pg_control LAST,
	// excluded files absent, excluded-dir-contents absent but dirs present.
	tr := tar.NewReader(&tarBytes)
	var (
		names            []string
		sawLabel         bool
		sawPgControl     bool
		sawBase1_1259    bool
		sawPgReplslotDir bool
		sawPgStatTmpDir  bool
		lastFile         string
	)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		names = append(names, hdr.Name)
		if hdr.Typeflag == tar.TypeReg {
			lastFile = hdr.Name
		}
		switch hdr.Name {
		case "backup_label":
			sawLabel = true
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read backup_label: %v", err)
			}
			text := string(data)
			for _, sub := range []string{
				"START WAL LOCATION:",
				"CHECKPOINT LOCATION:",
				"BACKUP METHOD: streamed",
				"BACKUP FROM: primary",
				"LABEL: unit-test",
				"START TIMELINE: 1",
			} {
				if !strings.Contains(text, sub) {
					t.Errorf("backup_label missing %q\nfull text:\n%s", sub, text)
				}
			}
		case "global/pg_control":
			sawPgControl = true
		case "base/1/1259":
			sawBase1_1259 = true
		case "pg_replslot/":
			sawPgReplslotDir = true
		case "pg_stat_tmp/":
			sawPgStatTmpDir = true

		// --- excludeFiles: these must never appear ---
		case "postmaster.pid", "postmaster.opts", ".goopg.ctl.sock",
			"postgresql.auto.conf.tmp", "current_logfiles.tmp",
			"backup_manifest", "tablespace_map":
			t.Errorf("tar contains excluded file %q", hdr.Name)
		}
		// pg_internal.init prefix match — covers any base name beginning
		// with "pg_internal.init" at any path depth.
		if strings.HasSuffix(hdr.Name, "pg_internal.init") ||
			strings.Contains(hdr.Name, "pg_internal.init.") {
			t.Errorf("tar contains excluded pg_internal.init* entry %q", hdr.Name)
		}
		// excludeDirContents: contents must not appear.
		for _, dirPrefix := range []string{"pg_replslot/s1", "pg_stat_tmp/pgstat.stat"} {
			if hdr.Name == dirPrefix || strings.HasPrefix(hdr.Name, dirPrefix+"/") {
				t.Errorf("tar contains excluded dir-content entry %q", hdr.Name)
			}
		}
	}
	if !sawLabel {
		t.Error("tar missing backup_label")
	}
	if !sawPgControl {
		t.Error("tar missing global/pg_control")
	}
	if !sawBase1_1259 {
		t.Error("tar missing base/1/1259 (sample relfile)")
	}
	if !sawPgReplslotDir {
		t.Error("tar missing pg_replslot/ directory entry (standby startup requires it)")
	}
	if !sawPgStatTmpDir {
		t.Error("tar missing pg_stat_tmp/ directory entry (excludeDirContents ships dir, not contents)")
	}
	if lastFile != "global/pg_control" {
		t.Errorf("last regular file in tar = %q, want global/pg_control "+
			"(upstream invariant: pg_control emitted last for atomic recovery)\n"+
			"all entries: %v", lastFile, names)
	}

	// Confirm data dir didn't move; the server should not have
	// touched it (only read).
	if _, err := os.Stat(filepath.Join(dataDir, "PG_VERSION")); err != nil {
		t.Errorf("PG_VERSION vanished from data dir: %v", err)
	}
}

// TestBaseBackupRejectsWithoutDataDir confirms the handler emits a
// clean ErrorResponse + ReadyForQuery when DataDir is unset (the
// in-process test config used elsewhere in this package).
func TestBaseBackupRejectsWithoutDataDir(t *testing.T) {
	addr, _, stop := startReplicationTestServer(t)
	defer stop()
	conn, r, w := dialReplication(t, addr)
	defer conn.Close()

	sendQuery(t, w, "BASE_BACKUP")
	frames := readUntilReadyForQuery(t, r)
	if len(frames) < 2 {
		t.Fatalf("frame count = %d, want >= 2", len(frames))
	}
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("frame[0] = %c, want E (ErrorResponse)", frames[0].Type)
	}
	if frames[len(frames)-1].Type != protocol.MsgReadyForQuery {
		t.Errorf("tail = %c, want Z", frames[len(frames)-1].Type)
	}
}

// TestBaseBackupParseOptions exercises both PG17+ parenthesized
// option grammar and the legacy whitespace-separated form.
func TestBaseBackupParseOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want baseBackupOptions
	}{
		{
			name: "empty",
			in:   "",
			want: baseBackupOptions{},
		},
		{
			name: "parenthesized",
			in:   "(LABEL 'pg_basebackup base backup', PROGRESS, WAIT 0, TARGET 'client', MANIFEST 'yes')",
			want: baseBackupOptions{Label: "pg_basebackup base backup", Progress: true, Manifest: "yes", Target: "client", Wait: 0},
		},
		{
			name: "legacy",
			in:   "LABEL 'tag' PROGRESS",
			want: baseBackupOptions{Label: "tag", Progress: true},
		},
		{
			name: "unknown_key_tolerated",
			in:   "(LABEL 'x', SOMETHING_NEW 'y')",
			want: baseBackupOptions{Label: "x"},
		},
		{
			// pg_basebackup -X fetch sends a bare `WAL` boolean option.
			name: "wal_fetch_new_syntax",
			in:   "(LABEL 'x', WAL, MANIFEST 'no')",
			want: baseBackupOptions{Label: "x", Manifest: "no", IncludeWAL: true},
		},
		{
			// Legacy walsender clients send `WAL` as a bare keyword.
			name: "wal_fetch_legacy",
			in:   "LABEL 'tag' PROGRESS WAL",
			want: baseBackupOptions{Label: "tag", Progress: true, IncludeWAL: true},
		},
		{
			// An explicit false value disables WAL inclusion.
			name: "wal_explicit_false",
			in:   "(WAL 'f')",
			want: baseBackupOptions{IncludeWAL: false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBaseBackupOptions(tc.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Errorf("parseBaseBackupOptions(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
