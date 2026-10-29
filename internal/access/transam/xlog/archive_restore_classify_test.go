package xlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestRestoreArchivedFileFailureClassification pins RestoreArchivedFile's
// (xlogarchive.c) three failure outcomes. A plain non-zero exit is the end
// of the archive. A child killed by SIGTERM, or the shell's exit 128+15, is
// a fast shutdown. Any other signal, or an exit status above 125 (command
// not found / not executable), is FATAL with wait_result_to_str's text —
// treating those as "not found" would end recovery early and promote.
func TestRestoreArchivedFileFailureClassification(t *testing.T) {
	const seg = "000000010000000000000001"
	cases := []struct {
		name, cmd string
		want      string // "end", "shutdown", or the FATAL message
	}{
		{"plain failure", "exit 1", "end"},
		{"sigterm", "kill -TERM $$", "shutdown"},
		{"exit 143", "exit 143", "shutdown"},
		{"command not found", "goopg_no_such_command_xyz %p", `could not restore file "` + seg + `" from archive: command not found`},
		{"not executable", "exit 126", `could not restore file "` + seg + `" from archive: command not executable`},
		{"sigkill", "kill -KILL $$", `could not restore file "` + seg + `" from archive: child process was terminated by signal 9: Killed`},
		{"exit 130", "exit 130", `could not restore file "` + seg + `" from archive: child process exited with exit code 130`},
		{"bad placeholder", "cp %z %p", `invalid value for parameter "restore_command": "cp %z %p" (String contains unexpected placeholder "%z".)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RestoreArchivedFileContext(context.Background(), c.cmd, t.TempDir(), seg, "000000010000000000000000")
			var fatal *RestoreFatalError
			switch {
			case err == nil:
				t.Fatalf("restore %q succeeded", c.cmd)
			case errors.Is(err, ErrRecoveryShutdown):
				if c.want != "shutdown" {
					t.Fatalf("got shutdown, want %q", c.want)
				}
			case errors.As(err, &fatal):
				if fatal.Msg != c.want {
					t.Fatalf("FATAL %q, want %q", fatal.Msg, c.want)
				}
			default:
				if c.want != "end" {
					t.Fatalf("got end-of-archive (%v), want %q", err, c.want)
				}
			}
		})
	}
}

// TestRestoreArchivedFilePlaceholders pins BuildRestoreCommand's expansion:
// %f, %p, %r (the last restartpoint's file) and %% — goopg substituted only
// %f and %p, leaving %r and %% verbatim.
func TestRestoreArchivedFilePlaceholders(t *testing.T) {
	dir := t.TempDir()
	const seg, rp = "000000010000000000000003", "000000010000000000000002"
	out := filepath.Join(dir, "expanded")
	cmd := `echo "%r|%%|%f" > ` + out + ` && touch %p`
	if err := RestoreArchivedFileContext(context.Background(), cmd, dir, seg, rp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := rp + "|%|" + seg + "\n"; string(got) != want {
		t.Fatalf("expanded %q, want %q", got, want)
	}
}

// TestRestoreArchivedFileShutdownKillsCommand pins the fast-shutdown path
// while restore_command runs (StartupProcShutdownHandler with
// in_restore_command): cancelling returns ErrRecoveryShutdown at once and
// the command's whole process group — the shell and what it waits on — is
// signalled, not left running.
func TestRestoreArchivedFileShutdownKillsCommand(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "sleep.pid")
	cmd := "sleep 30 & echo $! > " + pidFile + "; wait"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- RestoreArchivedFileContext(ctx, cmd, dir, "000000010000000000000001", "000000010000000000000000")
	}()
	var sleepPID int
	for deadline := time.Now().Add(10 * time.Second); sleepPID == 0; {
		if b, err := os.ReadFile(pidFile); err == nil {
			sleepPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("restore_command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRecoveryShutdown) {
			t.Fatalf("err = %v, want ErrRecoveryShutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restore did not return after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("restore ran %v — the command was not interrupted", elapsed)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(sleepPID, 0) == nil; {
		if time.Now().After(deadline) {
			_ = syscall.Kill(sleepPID, syscall.SIGKILL)
			t.Fatalf("restore_command's child %d survived the shutdown", sleepPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunArchiveRecoveryStopsOnFatalRestoreAndShutdown pins that archive
// recovery is aborted, not ended, by a FATAL restore_command failure, and
// stops without finishing on a shutdown request. Both used to read as "the
// archive is exhausted", after which goopg start promoted to a new timeline
// and abandoned the rest of the archive.
func TestRunArchiveRecoveryStopsOnFatalRestoreAndShutdown(t *testing.T) {
	newDataDir := func(t *testing.T) string {
		d := t.TempDir()
		if err := os.MkdirAll(filepath.Join(d, "pg_wal"), 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	run := func(ctx context.Context, dataDir, cmd string) error {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
		_, err := RunArchiveRecoveryWith(ctx, mgr, dataDir, func() string { return cmd }, 1<<20)
		return err
	}

	t.Run("command not found is FATAL", func(t *testing.T) {
		err := run(context.Background(), newDataDir(t), "goopg_no_such_command_xyz %f %p")
		var fatal *RestoreFatalError
		if !errors.As(err, &fatal) || !strings.Contains(fatal.Msg, "command not found") {
			t.Fatalf("err = %v, want a FATAL restore failure", err)
		}
	})
	t.Run("plain failure ends recovery", func(t *testing.T) {
		if err := run(context.Background(), newDataDir(t), "false"); err != nil {
			t.Fatalf("err = %v, want the normal end of the archive", err)
		}
	})
	t.Run("shutdown request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := run(ctx, newDataDir(t), "false"); !errors.Is(err, ErrRecoveryShutdown) {
			t.Fatalf("err = %v, want ErrRecoveryShutdown", err)
		}
	})
}
