package xlog

import (
	"encoding/binary"
	"errors"
	"testing"
)

// TestParameterChangeMinimalFatalInArchiveRecovery pins XLogReportParameters'
// record shape (xl_parameter_change, 28 bytes of main data) and xlog_redo's
// archive-recovery check: replaying a wal_level=minimal PARAMETER_CHANGE
// while ArchiveRecoveryRequested raises the FATAL, and does not otherwise.
// M0149-0020.
func TestParameterChangeMinimalFatalInArchiveRecovery(t *testing.T) {
	dir := t.TempDir()
	segSize := int64(4 * XLOGBlockSize)
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultGUCParameters()
	p.WalLevel = 0
	p.MaxWalSenders = 0
	p.TrackCommitTimestamp = true
	payload, err := EncodeParameterChangePG(p)
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
	var rec *XLogDecodedRecord
	for _, r := range recs {
		if r.XLog != nil && r.XLog.Header.Rmid == RmgrXLog && r.XLog.Header.Info == xlogXLogParameterChange {
			rec = r.XLog
		}
	}
	if rec == nil {
		t.Fatalf("no XLOG_PARAMETER_CHANGE record among %d records", len(recs))
	}
	if len(rec.MainData) != sizeOfXlParameterChange {
		t.Fatalf("main data = %d bytes, want %d", len(rec.MainData), sizeOfXlParameterChange)
	}
	le := binary.LittleEndian
	if got := int32(le.Uint32(rec.MainData[0:])); got != p.MaxConnections {
		t.Errorf("MaxConnections = %d, want %d", got, p.MaxConnections)
	}
	if got := le.Uint32(rec.MainData[20:]); got != 0 {
		t.Errorf("wal_level = %d, want 0 (minimal)", got)
	}
	if rec.MainData[24] != 0 || rec.MainData[25] != 1 {
		t.Errorf("wal_log_hints/track_commit_timestamp = %d/%d, want 0/1", rec.MainData[24], rec.MainData[25])
	}

	defer SetArchiveRecoveryRequested(false)
	SetArchiveRecoveryRequested(false)
	if _, err := replayXLogParameterChange(nil, rec); err != nil {
		t.Fatalf("crash recovery: replay error %v, want none", err)
	}
	SetArchiveRecoveryRequested(true)
	_, err = replayXLogParameterChange(nil, rec)
	var fatal *RecoveryFatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("archive recovery: replay error %v, want *RecoveryFatalError", err)
	}
	if want := `WAL was generated with "wal_level=minimal", cannot continue recovering`; fatal.Msg != want {
		t.Errorf("Msg = %q, want %q", fatal.Msg, want)
	}
}
