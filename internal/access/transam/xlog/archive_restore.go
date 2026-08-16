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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
