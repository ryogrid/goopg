package wal

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/control"
)

// GUCParameters holds the 8 GUC fields that PostgreSQL broadcasts via
// XLOG_PARAMETER_CHANGE so standbys can enforce compatibility via
// CheckRequiredParameterValues (src/backend/access/transam/xlog.c).
// Mirrors `xl_parameter_change` in src/include/access/xlog_internal.h.
type GUCParameters struct {
	MaxConnections       int32
	MaxWorkerProcesses   int32
	MaxWalSenders        int32
	MaxPreparedXacts     int32
	MaxLocksPerXact      int32
	// WalLevel: 0=minimal, 1=replica, 2=logical (matches PG's WalLevel enum).
	WalLevel             int32
	WalLogHints          bool
	TrackCommitTimestamp bool
}

// DefaultGUCParameters returns the standard GUC values for a goopg primary
// that matches a PG18 initdb configuration. A PG18 standby's
// CheckRequiredParameterValues will accept these as compatible defaults.
func DefaultGUCParameters() GUCParameters {
	return GUCParameters{
		MaxConnections:       100,
		MaxWorkerProcesses:   8,
		MaxWalSenders:        10,
		MaxPreparedXacts:     0,
		MaxLocksPerXact:      64,
		WalLevel:             1, // replica — minimum for streaming replication
		WalLogHints:          false,
		TrackCommitTimestamp: false,
	}
}

// ReportParameters is the primary-side equivalent of PG's XLogReportParameters
// (src/backend/access/transam/xlog.c:8147-8199). It compares the given GUC
// params against the current GUC echo fields in pg_control and, when any field
// differs, emits an XLOG_PARAMETER_CHANGE WAL record and updates pg_control.
//
// Call this after the WAL writer is ready (postmaster start). A PG18 standby
// that receives the XLOG_PARAMETER_CHANGE record will update its own pg_control
// via xlog_redo → replayXLogParameterChange, satisfying the compatibility check
// performed in CheckRequiredParameterValues before entering hot-standby mode.
//
// ReportParameters is a no-op when dataDir is empty or walWriter is nil
// (test configurations without a full cluster on disk).
func ReportParameters(dataDir string, walWriter *Writer, params GUCParameters) error {
	if dataDir == "" || walWriter == nil {
		return nil
	}

	// Read current pg_control GUC echo fields to detect mismatches.
	cd, err := control.ReadControlFile(dataDir)
	if err != nil {
		return fmt.Errorf("wal: ReportParameters: %w", err)
	}

	needsUpdate := cd == nil ||
		cd.MaxConnections != uint32(params.MaxConnections) ||
		cd.MaxWorkerProcesses != uint32(params.MaxWorkerProcesses) ||
		cd.MaxWalSenders != uint32(params.MaxWalSenders) ||
		cd.MaxPreparedXacts != uint32(params.MaxPreparedXacts) ||
		cd.MaxLocksPerXact != uint32(params.MaxLocksPerXact) ||
		cd.WalLevel != uint32(params.WalLevel) ||
		cd.WalLogHints != params.WalLogHints ||
		cd.TrackCommitTimestamp != params.TrackCommitTimestamp

	if !needsUpdate {
		return nil
	}

	// Emit XLOG_PARAMETER_CHANGE WAL record so standbys can replay it.
	// Must come BEFORE the pg_control update (mirrors PG's XLogReportParameters:
	// XLogInsert first, UpdateControlFile second).
	payload := EncodeParameterChange(params)
	if _, _, err := walWriter.Append(payload); err != nil {
		return fmt.Errorf("wal: XLOG_PARAMETER_CHANGE emit: %w", err)
	}

	// Persist the new GUC echo values in pg_control.
	return control.UpdateControlFile(dataDir, func(cfd *control.ControlFileData) {
		cfd.WalLevel = uint32(params.WalLevel)
		cfd.WalLogHints = params.WalLogHints
		cfd.MaxConnections = uint32(params.MaxConnections)
		cfd.MaxWorkerProcesses = uint32(params.MaxWorkerProcesses)
		cfd.MaxWalSenders = uint32(params.MaxWalSenders)
		cfd.MaxPreparedXacts = uint32(params.MaxPreparedXacts)
		cfd.MaxLocksPerXact = uint32(params.MaxLocksPerXact)
		cfd.TrackCommitTimestamp = params.TrackCommitTimestamp
	})
}

// EncodeParameterChange encodes a PG-canonical XLOG_PARAMETER_CHANGE WAL
// record payload wrapped in the RecordKindCanonical (0xFE) envelope.
// The result is suitable for direct submission to wal.Writer.Append.
//
// On-disk layout of the embedded xl_parameter_change (26 bytes, little-endian):
//
//	offset  0: MaxConnections       int32
//	offset  4: MaxWorkerProcesses   int32
//	offset  8: MaxWalSenders        int32
//	offset 12: MaxPreparedXacts     int32
//	offset 16: MaxLocksPerXact      int32
//	offset 20: WalLevel             int32
//	offset 24: WalLogHints          bool (1 byte)
//	offset 25: TrackCommitTimestamp bool (1 byte)
//
// Matches src/include/access/xlog_internal.h::xl_parameter_change.
func EncodeParameterChange(params GUCParameters) []byte {
	// Build the 26-byte xl_parameter_change body.
	mainData := make([]byte, 26)
	le := binary.LittleEndian
	le.PutUint32(mainData[0:4], uint32(params.MaxConnections))
	le.PutUint32(mainData[4:8], uint32(params.MaxWorkerProcesses))
	le.PutUint32(mainData[8:12], uint32(params.MaxWalSenders))
	le.PutUint32(mainData[12:16], uint32(params.MaxPreparedXacts))
	le.PutUint32(mainData[16:20], uint32(params.MaxLocksPerXact))
	le.PutUint32(mainData[20:24], uint32(params.WalLevel))
	if params.WalLogHints {
		mainData[24] = 1
	}
	if params.TrackCommitTimestamp {
		mainData[25] = 1
	}

	// Wrap body in xlrBlockIDDataShort frame so wrapXLogMainData (format.go)
	// returns it verbatim through the RecordKindCanonical pass-through path.
	body := make([]byte, 2+len(mainData))
	body[0] = xlrBlockIDDataShort // 0xFF
	body[1] = byte(len(mainData)) // 26
	copy(body[2:], mainData)

	// Outer RecordKindCanonical (0xFE) envelope:
	//   [0]    RecordKindCanonical
	//   [1]    rmgr = RmgrXLog (0)
	//   [2]    info = xlogXLogParameterChange (0x60)
	//   [3..6] xid = 0 (XLOG_PARAMETER_CHANGE carries no XID)
	//   [7..]  body (xlrBlockIDDataShort + length + xl_parameter_change)
	//
	// classifyXLogRecord reads [1..6] to produce the correct XLogRecord
	// header (Rmid=0, Info=0x60); wrapXLogMainData returns body[7:] verbatim.
	// A PG18 standby decodes this as XLOG_PARAMETER_CHANGE and calls xlog_redo.
	out := make([]byte, 7+len(body))
	out[0] = RecordKindCanonical
	out[1] = uint8(RmgrXLog)           // 0
	out[2] = xlogXLogParameterChange   // 0x60
	// out[3:7] = xid = 0 (zero-initialized)
	copy(out[7:], body)
	return out
}
