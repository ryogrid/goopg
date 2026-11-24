package xlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackupLabelFile and BackupLabelOld are backup_label's name in the data
// directory and the name it is renamed to once recovery has used it
// (BACKUP_LABEL_FILE / BACKUP_LABEL_OLD, xlog.h).
const (
	BackupLabelFile = "backup_label"
	BackupLabelOld  = "backup_label.old"
)

// BackupLabel is what read_backup_label (xlogrecovery.c) takes from
// backup_label: the backup's redo start (START WAL LOCATION), its
// checkpoint record (CHECKPOINT LOCATION) and START TIMELINE, all PG
// (0-based) values. TLI is 0 when the label has no START TIMELINE line
// (labels from before PG 11), in which case the redo segment's timeline
// applies.
type BackupLabel struct {
	StartLSN      uint64
	CheckpointLSN uint64
	TLI           uint32
	FromStandby   bool
}

// ReadBackupLabel reads dataDir/backup_label. ok is false when there is
// none; a label that does not parse is read_backup_label's FATAL
// `invalid data in file "backup_label"`.
func ReadBackupLabel(dataDir string) (BackupLabel, bool, error) {
	var bl BackupLabel
	b, err := os.ReadFile(filepath.Join(dataDir, BackupLabelFile))
	if errors.Is(err, os.ErrNotExist) {
		return bl, false, nil
	}
	if err != nil {
		return bl, false, &RecoveryFatalError{Msg: fmt.Sprintf("could not read file \"%s\": %v", BackupLabelFile, err)}
	}
	invalid := &RecoveryFatalError{Msg: fmt.Sprintf("invalid data in file \"%s\"", BackupLabelFile)}
	var haveStart, haveCkpt bool
	for line := range strings.SplitSeq(string(b), "\n") {
		var hi, lo uint32
		var tli uint32
		var from string
		switch {
		case strings.HasPrefix(line, "START WAL LOCATION: "):
			// "START WAL LOCATION: %X/%X (file %08X%16s)"
			if _, err := fmt.Sscanf(line, "START WAL LOCATION: %X/%X (file %08X", &hi, &lo, &tli); err != nil {
				return bl, false, invalid
			}
			bl.StartLSN = uint64(hi)<<32 | uint64(lo)
			if bl.TLI == 0 {
				bl.TLI = tli
			}
			haveStart = true
		case strings.HasPrefix(line, "CHECKPOINT LOCATION: "):
			if _, err := fmt.Sscanf(line, "CHECKPOINT LOCATION: %X/%X", &hi, &lo); err != nil {
				return bl, false, invalid
			}
			bl.CheckpointLSN = uint64(hi)<<32 | uint64(lo)
			haveCkpt = true
		case strings.HasPrefix(line, "BACKUP FROM: "):
			if _, err := fmt.Sscanf(line, "BACKUP FROM: %s", &from); err == nil {
				bl.FromStandby = from == "standby"
			}
		case strings.HasPrefix(line, "START TIMELINE: "):
			if _, err := fmt.Sscanf(line, "START TIMELINE: %d", &tli); err != nil {
				return bl, false, invalid
			}
			// "START TIMELINE" must agree with the redo segment's timeline.
			if haveStart && tli != bl.TLI {
				return bl, false, invalid
			}
			bl.TLI = tli
		}
	}
	if !haveStart || !haveCkpt {
		return bl, false, invalid
	}
	return bl, true, nil
}

// StartingBackupRecoveryMessage is the LOG line InitWalRecovery writes when
// it starts from a backup_label.
func (bl BackupLabel) StartingBackupRecoveryMessage() string {
	return fmt.Sprintf("starting backup recovery with redo LSN %X/%X, checkpoint LSN %X/%X, on timeline ID %d",
		uint32(bl.StartLSN>>32), uint32(bl.StartLSN), uint32(bl.CheckpointLSN>>32), uint32(bl.CheckpointLSN), bl.TLI)
}

// EncodeBackupEndPG builds the RM_XLOG XLOG_BACKUP_END record
// do_pg_backup_stop writes on a primary: its main data is the backup's start
// point (an XLogRecPtr). Recovery from the backup is consistent once it
// replays this record (xlog_redo's backupStartPoint check).
func EncodeBackupEndPG(startLSN uint64) ([]byte, error) {
	main := make([]byte, 8)
	binary.LittleEndian.PutUint64(main, startLSN)
	return framedAssemble(RmgrXLog, xlogXLogBackupEnd, 0, main, nil)
}

// BackupHistoryFileName is BackupHistoryFileName (xlog_internal.h): the
// backup history file pg_backup_stop writes into pg_wal,
// "<tli><log><seg>.<offset>.backup" for the segment holding startLSN.
func BackupHistoryFileName(tli uint32, startLSN uint64, segSize int64) string {
	segNo := startLSN / uint64(segSize)
	return fmt.Sprintf("%s.%08X.backup", XLogFileName(tli, segNo, segSize), uint32(startLSN%uint64(segSize)))
}
