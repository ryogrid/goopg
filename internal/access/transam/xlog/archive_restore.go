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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrRecoveryShutdown reports that archive recovery stopped for a shutdown
// request: goopg start's context was cancelled (SIGTERM/SIGINT), or the
// restore_command child died of SIGTERM. Upstream's startup process
// proc_exit(1)s in both cases (StartupProcShutdownHandler while
// in_restore_command, and RestoreArchivedFile's wait_result_is_signal(rc,
// SIGTERM) arm) — recovery is not completed and nothing is promoted.
var ErrRecoveryShutdown = errors.New("archive recovery interrupted by a shutdown request")

// RestoreFatalError is a restore_command failure RestoreArchivedFile
// reports at FATAL rather than as the end of the archive: the child died of
// a signal other than SIGTERM, or the shell reported an unrunnable command
// (exit status above 125, wait_result_is_any_signal(rc, true)). Upstream
// aborts recovery there because returning "not found" would make the
// caller assume recovery is complete and start up the database.
type RestoreFatalError struct{ Msg string }

func (e *RestoreFatalError) Error() string { return e.Msg }

// RestoreArchivedFile is RestoreArchivedFileContext without cancellation
// and with %r expanding to the zero restart-point file name.
func RestoreArchivedFile(restoreCommand, walDir, segmentName string) error {
	return RestoreArchivedFileContext(context.Background(), restoreCommand, walDir, segmentName, XLogFileName(0, 0, DefaultSegmentSize))
}

// RestoreArchivedFileContext shells out to restoreCommand to fetch the
// named WAL file into walDir, as upstream's RestoreArchivedFile
// (xlogarchive.c) does. BuildRestoreCommand's placeholders apply: %f is the
// file name, %p the destination path, %r lastRestartPointFname (the file
// holding the oldest restartpoint's redo), %% a literal percent; any other
// placeholder is a FATAL invalid value.
//
// It returns nil when the command exits zero and the file exists. A
// failure is classified as upstream does:
//   - ctx cancelled while the command runs, or the child killed by SIGTERM
//     (or the shell's exit 128+SIGTERM): ErrRecoveryShutdown. Cancelling
//     sends SIGTERM to the command's process group, as the postmaster
//     signals the startup process's group on a fast shutdown.
//   - any other signal, or an exit status above 125 (command not found /
//     not executable): *RestoreFatalError, "could not restore file ... from
//     archive: <wait_result_to_str>".
//   - otherwise (the archive does not have the file): a plain error — the
//     normal end of archive recovery.
func RestoreArchivedFileContext(ctx context.Context, restoreCommand, walDir, segmentName, lastRestartPointFname string) error {
	if restoreCommand == "" {
		return fmt.Errorf("wal: restore_command is not configured")
	}
	if ctx.Err() != nil {
		return ErrRecoveryShutdown
	}
	destPath := filepath.Join(walDir, segmentName)
	cmd, err := ReplacePercentPlaceholders(restoreCommand, "restore_command", map[byte]string{
		'f': segmentName,
		'p': destPath,
		'r': lastRestartPointFname,
	})
	if err != nil {
		return &RestoreFatalError{Msg: err.Error()}
	}

	// Execute via sh -c so the operator can use pipes, redirects, etc.
	// This is what upstream's system() does. The child gets its own
	// process group so a shutdown can signal the shell and whatever it
	// is waiting on.
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second
	runErr := c.Run()
	if ctx.Err() != nil {
		return ErrRecoveryShutdown
	}
	if runErr == nil {
		if _, err := os.Stat(destPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("wal: restore_command succeeded but %s not found: %w", destPath, err)
			}
			return &RestoreFatalError{Msg: fmt.Sprintf("could not stat file \"%s\": %v", destPath, err)}
		}
		return nil
	}
	if waitResultIsSignal(runErr, syscall.SIGTERM) {
		return ErrRecoveryShutdown
	}
	if waitResultIsAnySignal(runErr) {
		return &RestoreFatalError{Msg: fmt.Sprintf("could not restore file \"%s\" from archive: %s", segmentName, waitResultToStr(runErr))}
	}
	return fmt.Errorf("wal: could not restore file \"%s\" from archive: %s", segmentName, waitResultToStr(runErr))
}

// waitResultIsSignal is wait_result_is_signal (src/common/wait_error.c):
// the child died of signum, or the shell exited 128+signum reporting it.
func waitResultIsSignal(err error, signum syscall.Signal) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal() == signum
	}
	return ee.ExitCode() == 128+int(signum)
}

// waitResultToStr is wait_result_to_str (src/common/wait_error.c).
func waitResultToStr(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err.Error()
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return fmt.Sprintf("child process was terminated by signal %d: %s", int(ws.Signal()), signalDescription(ws.Signal()))
	}
	switch code := ee.ExitCode(); code {
	case 126:
		return "command not executable"
	case 127:
		return "command not found"
	default:
		return fmt.Sprintf("child process exited with exit code %d", code)
	}
}

// signalDescription is pg_strsignal's text: strsignal(3) on glibc, which
// capitalises the first letter (Go's Signal.String is lower case).
func signalDescription(sig syscall.Signal) string {
	d := sig.String()
	if d == "" {
		return d
	}
	return strings.ToUpper(d[:1]) + d[1:]
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
