// Archive-restore support for WAL recovery.
//
// RestoreArchivedFile mirrors upstream PostgreSQL's
// RestoreArchivedFile (xlogarchive.c): it shells out to the
// configured restore_command to fetch a single WAL segment from
// archival storage into the pg_wal directory.
//
// See docs/design/0130-0009-recovery-signal-archive-recovery.md.

package xlog

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// RestoreArchivedFile shells out to restoreCommand to fetch the
// named WAL segment into walDir. %f expands to the segment filename
// (e.g. "000000010000000000000001"), %p to the destination path
// (filepath.Join(walDir, segmentName)). Returns nil when the command
// exits zero AND the destination file exists afterward; returns an
// error describing the failure otherwise (non-zero exit, missing
// file, or I/O error).
//
// restoreCommand may be empty — callers should guard against that
// before calling (an empty command always errors, matching upstream's
// "restore_command not configured" guard).
func RestoreArchivedFile(restoreCommand, walDir, segmentName string) error {
	if restoreCommand == "" {
		return fmt.Errorf("wal: restore_command is not configured")
	}

	destPath := filepath.Join(walDir, segmentName)

	// Build the command: replace %f with segment filename and %p
	// with the destination path, matching upstream
	// BuildRestoreCommand in postgres/src/common/archive.c.
	cmd := restoreCommand
	cmd = strings.ReplaceAll(cmd, "%f", segmentName)
	cmd = strings.ReplaceAll(cmd, "%p", destPath)

	// Execute via sh -c so the operator can use pipes, redirects, etc.
	// This is what upstream's system() does.
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("wal: restore_command %q exited: %w", cmd, err)
	}

	// Upstream also verifies the file exists and has expectedSize,
	// but we don't know the expected size for an unfetched segment
	// (it could be short on the final segment). Just check existence.
	if _, err := os.Stat(destPath); err != nil {
		return fmt.Errorf("wal: restore_command succeeded but %s not found: %w", destPath, err)
	}
	return nil
}

// ReplacePercentPlaceholders is PG's replace_percent_placeholders
// (src/common/percentrepl.c): "%%" becomes "%", "%<letter>" becomes vals'
// entry for that letter, and anything else — an unknown letter, or a lone "%"
// at the end — is an invalid value for paramName (ERRCODE_INVALID_PARAMETER_VALUE
// in PG), reported with PG's message and detail.
func ReplacePercentPlaceholders(instr, paramName string, vals map[byte]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(instr); i++ {
		if instr[i] != '%' {
			b.WriteByte(instr[i])
			continue
		}
		if i+1 >= len(instr) {
			return "", fmt.Errorf("invalid value for parameter \"%s\": \"%s\" (String ends unexpectedly after escape character \"%%\".)", paramName, instr)
		}
		i++
		if instr[i] == '%' {
			b.WriteByte('%')
			continue
		}
		v, ok := vals[instr[i]]
		if !ok {
			return "", fmt.Errorf("invalid value for parameter \"%s\": \"%s\" (String contains unexpected placeholder \"%%%c\".)", paramName, instr, instr[i])
		}
		b.WriteString(v)
	}
	return b.String(), nil
}

// ExecuteRecoveryCommand is PG's ExecuteRecoveryCommand (xlogarchive.c) for
// archive_cleanup_command and recovery_end_command: %r becomes the WAL file
// holding the oldest restartpoint's redo (GetOldestRestartPoint — pg_control's
// checkPointCopy.redo and timeline), and the command runs through the shell as
// system() does. A failure returns an error and anySignal, which is PG's
// wait_result_is_any_signal(rc, true): the child died of a signal, or exited
// above 125 (the shell's report of a signalled or unrunnable command). The
// caller decides the level — WARNING, or FATAL on a signal when it passed
// failOnSignal (recovery_end_command). An invalid placeholder is an error
// before anything runs.
func ExecuteRecoveryCommand(command, commandName string, restartRedo uint64, restartTLI uint32, segSize int64) (anySignal bool, err error) {
	if segSize <= 0 {
		segSize = DefaultSegmentSize
	}
	fname := XLogFileName(restartTLI, restartRedo/uint64(segSize), segSize)
	cmd, err := ReplacePercentPlaceholders(command, commandName, map[byte]string{'r': fname})
	if err != nil {
		return false, err
	}
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return waitResultIsAnySignal(err), fmt.Errorf("%s \"%s\": %w", commandName, command, err)
	}
	return false, nil
}

// waitResultIsAnySignal is wait_result_is_any_signal(rc, true)
// (src/common/wait_error.c) for an exec error: a signalled child, or an exit
// status above 125.
func waitResultIsAnySignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return true
	}
	return ee.ExitCode() > 125
}
