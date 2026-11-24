package xlog

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReadBackupLabel pins read_backup_label's fields and its FATAL on a
// label without the required lines. M0149-0021.
func TestReadBackupLabel(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadBackupLabel(dir); ok || err != nil {
		t.Fatalf("no label: ok=%v err=%v, want false, nil", ok, err)
	}
	label := "START WAL LOCATION: 0/20000A0 (file 000000030000000000000002)\n" +
		"CHECKPOINT LOCATION: 0/20000F8\n" +
		"BACKUP METHOD: streamed\n" +
		"BACKUP FROM: primary\n" +
		"START TIME: 2026-11-25 00:00:00 UTC\n" +
		"LABEL: test label\n" +
		"START TIMELINE: 3\n"
	if err := os.WriteFile(filepath.Join(dir, BackupLabelFile), []byte(label), 0o600); err != nil {
		t.Fatal(err)
	}
	bl, ok, err := ReadBackupLabel(dir)
	if !ok || err != nil {
		t.Fatalf("ReadBackupLabel: ok=%v err=%v", ok, err)
	}
	if bl.StartLSN != 0x20000A0 || bl.CheckpointLSN != 0x20000F8 || bl.TLI != 3 || bl.FromStandby {
		t.Fatalf("label = %+v", bl)
	}
	if got, want := bl.StartingBackupRecoveryMessage(), "starting backup recovery with redo LSN 0/20000A0, checkpoint LSN 0/20000F8, on timeline ID 3"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, BackupLabelFile), []byte("LABEL: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadBackupLabel(dir)
	var fatal *RecoveryFatalError
	if !errors.As(err, &fatal) || fatal.Msg != `invalid data in file "backup_label"` {
		t.Fatalf("invalid label: err = %v", err)
	}
}

// TestBackupEndRecordAndHistoryName pins XLOG_BACKUP_END's shape (main data
// = the start point) and BackupHistoryFileName. M0149-0021.
func TestBackupEndRecordAndHistoryName(t *testing.T) {
	dir := t.TempDir()
	segSize := int64(4 * XLOGBlockSize)
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeBackupEndPG(0x10000A0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadSegmentRecords(dir, 0, segSize)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.XLog != nil && r.XLog.Header.Rmid == RmgrXLog && r.XLog.Header.Info == xlogXLogBackupEnd {
			found = true
			if len(r.XLog.MainData) != 8 || binary.LittleEndian.Uint64(r.XLog.MainData) != 0x10000A0 {
				t.Errorf("main data = %x", r.XLog.MainData)
			}
		}
	}
	if !found {
		t.Fatalf("no XLOG_BACKUP_END among %d records", len(recs))
	}
	if got, want := BackupHistoryFileName(1, 0x10000A0, 16<<20), "000000010000000000000001.000000A0.backup"; got != want {
		t.Errorf("BackupHistoryFileName = %q, want %q", got, want)
	}
}

// TestXLogArchiveIsBusy pins xlogarchive.c's XLogArchiveIsBusy. M0149-0021.
func TestXLogArchiveIsBusy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, archiveStatusDir), 0o700); err != nil {
		t.Fatal(err)
	}
	name := "000000010000000000000001"
	if XLogArchiveIsBusy(dir, name) {
		t.Error("missing file: busy, want not busy")
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !XLogArchiveIsBusy(dir, name) {
		t.Error("file without status: not busy, want busy")
	}
	_ = writeArchiveStatus(dir, name, ".ready")
	if !XLogArchiveIsBusy(dir, name) {
		t.Error(".ready: not busy, want busy")
	}
	_ = writeArchiveStatus(dir, name, ".done")
	if XLogArchiveIsBusy(dir, name) {
		t.Error(".done: busy, want not busy")
	}
}
