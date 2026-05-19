package wal

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/control"
)

// TestEncodeParameterChange verifies the byte layout of the canonical
// XLOG_PARAMETER_CHANGE payload produced by EncodeParameterChange.
func TestEncodeParameterChange(t *testing.T) {
	params := GUCParameters{
		MaxConnections:       200,
		MaxWorkerProcesses:   16,
		MaxWalSenders:        20,
		MaxPreparedXacts:     10,
		MaxLocksPerXact:      128,
		WalLevel:             1,
		WalLogHints:          true,
		TrackCommitTimestamp: false,
	}

	payload := EncodeParameterChange(params)

	// ---- RecordKindCanonical envelope (7 bytes) ----
	if payload[0] != RecordKindCanonical {
		t.Fatalf("payload[0] = 0x%02x, want RecordKindCanonical (0x%02x)", payload[0], RecordKindCanonical)
	}
	if payload[1] != uint8(RmgrXLog) {
		t.Fatalf("payload[1] (rmgr) = %d, want %d (RmgrXLog)", payload[1], uint8(RmgrXLog))
	}
	if payload[2] != xlogXLogParameterChange {
		t.Fatalf("payload[2] (info) = 0x%02x, want 0x%02x (XLOG_PARAMETER_CHANGE)", payload[2], xlogXLogParameterChange)
	}
	xid := binary.LittleEndian.Uint32(payload[3:7])
	if xid != 0 {
		t.Fatalf("payload[3:7] (xid) = %d, want 0", xid)
	}

	// ---- xlrBlockIDDataShort frame ----
	body := payload[7:]
	if body[0] != xlrBlockIDDataShort {
		t.Errorf("body[0] = 0x%02x, want xlrBlockIDDataShort (0x%02x)", body[0], xlrBlockIDDataShort)
	}
	if body[1] != 26 {
		t.Errorf("body[1] (length) = %d, want 26", body[1])
	}

	// Total: 7 (envelope) + 2 (short frame) + 26 (xl_parameter_change) = 35 bytes.
	const wantLen = 7 + 2 + 26
	if len(payload) != wantLen {
		t.Fatalf("len(payload) = %d, want %d", len(payload), wantLen)
	}

	// ---- xl_parameter_change data ----
	le := binary.LittleEndian
	data := body[2:]
	if v := int32(le.Uint32(data[0:4])); v != params.MaxConnections {
		t.Errorf("MaxConnections = %d, want %d", v, params.MaxConnections)
	}
	if v := int32(le.Uint32(data[4:8])); v != params.MaxWorkerProcesses {
		t.Errorf("MaxWorkerProcesses = %d, want %d", v, params.MaxWorkerProcesses)
	}
	if v := int32(le.Uint32(data[8:12])); v != params.MaxWalSenders {
		t.Errorf("MaxWalSenders = %d, want %d", v, params.MaxWalSenders)
	}
	if v := int32(le.Uint32(data[12:16])); v != params.MaxPreparedXacts {
		t.Errorf("MaxPreparedXacts = %d, want %d", v, params.MaxPreparedXacts)
	}
	if v := int32(le.Uint32(data[16:20])); v != params.MaxLocksPerXact {
		t.Errorf("MaxLocksPerXact = %d, want %d", v, params.MaxLocksPerXact)
	}
	if v := int32(le.Uint32(data[20:24])); v != params.WalLevel {
		t.Errorf("WalLevel = %d, want %d", v, params.WalLevel)
	}
	if gotHints := data[24] != 0; gotHints != params.WalLogHints {
		t.Errorf("WalLogHints = %v, want %v", gotHints, params.WalLogHints)
	}
	if gotTS := data[25] != 0; gotTS != params.TrackCommitTimestamp {
		t.Errorf("TrackCommitTimestamp = %v, want %v", gotTS, params.TrackCommitTimestamp)
	}
}

// TestReportParameters_NoOpWhenNilWriter verifies that ReportParameters with a
// nil WAL writer is a no-op (returns nil without touching pg_control).
func TestReportParameters_NoOpWhenNilWriter(t *testing.T) {
	if err := ReportParameters(t.TempDir(), nil, DefaultGUCParameters()); err != nil {
		t.Fatalf("unexpected error with nil writer: %v", err)
	}
}

// TestReportParameters_NoOpWhenNoDataDir verifies no-op with empty dataDir.
func TestReportParameters_NoOpWhenNoDataDir(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w := newPageHeadersWriter(t, walDir)
	if err := ReportParameters("", w, DefaultGUCParameters()); err != nil {
		t.Fatalf("unexpected error with empty dataDir: %v", err)
	}
}

// TestReportParameters_EmitOnFirstRun verifies that ReportParameters emits
// XLOG_PARAMETER_CHANGE and updates pg_control when called against a freshly
// initialised pg_control file with zeroed GUC fields.
func TestReportParameters_EmitOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	if err := writeMinimalPGControlForTest(t, dir); err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(dir, "pg_wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w := newPageHeadersWriter(t, walDir)

	params := DefaultGUCParameters()
	if err := ReportParameters(dir, w, params); err != nil {
		t.Fatalf("ReportParameters: %v", err)
	}

	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	if cd == nil {
		t.Fatal("pg_control not found after ReportParameters")
	}
	if cd.WalLevel != uint32(params.WalLevel) {
		t.Errorf("WalLevel = %d, want %d", cd.WalLevel, params.WalLevel)
	}
	if cd.MaxConnections != uint32(params.MaxConnections) {
		t.Errorf("MaxConnections = %d, want %d", cd.MaxConnections, params.MaxConnections)
	}
	if cd.MaxWorkerProcesses != uint32(params.MaxWorkerProcesses) {
		t.Errorf("MaxWorkerProcesses = %d, want %d", cd.MaxWorkerProcesses, params.MaxWorkerProcesses)
	}
}

// TestReportParameters_NoEmitWhenMatching verifies that ReportParameters emits
// no WAL when pg_control already contains the correct GUC values.
func TestReportParameters_NoEmitWhenMatching(t *testing.T) {
	dir := t.TempDir()
	params := DefaultGUCParameters()

	if err := writeMinimalPGControlForTest(t, dir); err != nil {
		t.Fatal(err)
	}
	// Pre-populate pg_control with the correct GUC values.
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.WalLevel = uint32(params.WalLevel)
		cd.MaxConnections = uint32(params.MaxConnections)
		cd.MaxWorkerProcesses = uint32(params.MaxWorkerProcesses)
		cd.MaxWalSenders = uint32(params.MaxWalSenders)
		cd.MaxPreparedXacts = uint32(params.MaxPreparedXacts)
		cd.MaxLocksPerXact = uint32(params.MaxLocksPerXact)
		cd.WalLogHints = params.WalLogHints
		cd.TrackCommitTimestamp = params.TrackCommitTimestamp
	}); err != nil {
		t.Fatal(err)
	}

	walDir := filepath.Join(dir, "pg_wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		t.Fatal(err)
	}
	w := newPageHeadersWriter(t, walDir)

	lsnBefore := w.WrittenLSN()
	if err := ReportParameters(dir, w, params); err != nil {
		t.Fatalf("ReportParameters: %v", err)
	}
	lsnAfter := w.WrittenLSN()

	// No WAL should have been written (values already match).
	if lsnAfter != lsnBefore {
		t.Errorf("unexpected WAL write: LSN advanced from %d to %d", lsnBefore, lsnAfter)
	}
}

// writeMinimalPGControlForTest writes a minimal valid pg_control so tests can
// use ReadControlFile and UpdateControlFile without running initdb.
func writeMinimalPGControlForTest(t *testing.T, dataDir string) error {
	t.Helper()
	globalDir := filepath.Join(dataDir, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		return err
	}
	// 8192-byte buffer; set DBState=InProduction so decoding doesn't see zeroes.
	buf := make([]byte, 8192)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(control.DBStateInProduction))
	path := filepath.Join(globalDir, "pg_control")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return err
	}
	// Trigger UpdateControlFile with a no-op fn to recompute the CRC.
	return control.UpdateControlFile(dataDir, func(_ *control.ControlFileData) {})
}

// newPageHeadersWriter creates a WAL writer in PG-compat PageHeaders mode for
// use in parameter_change tests. Uses a unique name to avoid conflict with
// iterator_test.go's newTestWriter helper.
func newPageHeadersWriter(t *testing.T, walDir string) *Writer {
	t.Helper()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: 1 << 20, // 1 MiB
		PageHeaders: true,
		SystemID:    12345,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}
