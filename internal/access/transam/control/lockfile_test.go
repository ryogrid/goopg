package control

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// liveForeignPID starts a child that outlives the test body and returns its
// pid — a live process that is neither us, our parent nor
// PG_GRANDPARENT_PID, i.e. what a running server's lock file names.
func liveForeignPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// deadPID returns the pid of a child that has already exited and been
// reaped — a crashed server's leftover lock file.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	return cmd.Process.Pid
}

// TestCreateDataDirLockFile pins CreateLockFile's (miscinit.c) decisions
// for the data-directory lock: a live owner refuses with PG's
// errmsg/errhint, an empty file refuses, a dead owner's or our own pid's
// file is stale and replaced, and the created file names this process.
func TestCreateDataDirLockFile(t *testing.T) {
	mine := PIDFile{PID: os.Getpid(), StartedAt: time.UnixMilli(1700000000000)}
	write := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, PIDFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ownsLock := func(t *testing.T, dir string) {
		t.Helper()
		pf, err := ParsePIDFile(dir)
		if err != nil {
			t.Fatalf("parse created lock file: %v", err)
		}
		if pf.PID != os.Getpid() || pf.DataDir != dir || pf.ListenAddr != "" || pf.SocketPath != "" {
			t.Fatalf("lock file = %+v, want our pid, dir %q and empty listen/socket lines", pf, dir)
		}
	}

	t.Run("fresh", func(t *testing.T) {
		dir := t.TempDir()
		p := mine
		p.DataDir = dir
		if err := CreateDataDirLockFile(dir, p); err != nil {
			t.Fatal(err)
		}
		ownsLock(t, dir)
	})

	t.Run("live owner refuses", func(t *testing.T) {
		dir := t.TempDir()
		other := liveForeignPID(t)
		content := fmt.Sprintf("%d\n%s\n1700000000000\n127.0.0.1:5432\n/tmp/s\n", other, dir)
		write(t, dir, content)
		p := mine
		p.DataDir = dir
		err := CreateDataDirLockFile(dir, p)
		var le *LockFileError
		if !errors.As(err, &le) {
			t.Fatalf("err = %v, want *LockFileError", err)
		}
		if le.Message != `lock file "postmaster.pid" already exists` {
			t.Errorf("message = %q", le.Message)
		}
		wantHint := fmt.Sprintf(`Is another postmaster (PID %d) running in data directory "%s"?`, other, dir)
		if le.Hint != wantHint {
			t.Errorf("hint = %q, want %q", le.Hint, wantHint)
		}
		got, _ := os.ReadFile(filepath.Join(dir, PIDFileName))
		if string(got) != content {
			t.Errorf("live owner's lock file was modified: %q", got)
		}
	})

	t.Run("negative pid names a standalone postgres", func(t *testing.T) {
		dir := t.TempDir()
		other := liveForeignPID(t)
		write(t, dir, fmt.Sprintf("-%d\n%s\n", other, dir))
		err := CreateDataDirLockFile(dir, PIDFile{PID: os.Getpid(), DataDir: dir})
		var le *LockFileError
		if !errors.As(err, &le) || le.Hint != fmt.Sprintf(`Is another postgres (PID %d) running in data directory "%s"?`, other, dir) {
			t.Fatalf("err = %v (%+v)", err, le)
		}
	})

	t.Run("empty refuses", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "")
		err := CreateDataDirLockFile(dir, PIDFile{PID: os.Getpid(), DataDir: dir})
		var le *LockFileError
		if !errors.As(err, &le) || le.Message != `lock file "postmaster.pid" is empty` ||
			le.Hint != "Either another server is starting, or the lock file is the remnant of a previous server startup crash." {
			t.Fatalf("err = %v (%+v)", err, le)
		}
	})

	t.Run("bogus refuses", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "not-a-pid\n")
		err := CreateDataDirLockFile(dir, PIDFile{PID: os.Getpid(), DataDir: dir})
		if err == nil || err.Error() != `bogus data in lock file "postmaster.pid": "not-a-pid\n"` {
			t.Fatalf("err = %v", err)
		}
	})

	for name, pid := range map[string]func(t *testing.T) int{
		"dead owner is stale": deadPID,
		"own pid is stale":    func(*testing.T) int { return os.Getpid() },
		"parent pid is stale": func(*testing.T) int { return os.Getppid() },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, strconv.Itoa(pid(t))+"\n/elsewhere\n1\n\n\n")
			p := mine
			p.DataDir = dir
			if err := CreateDataDirLockFile(dir, p); err != nil {
				t.Fatal(err)
			}
			ownsLock(t, dir)
		})
	}

	t.Run("grandparent pid is stale", func(t *testing.T) {
		dir := t.TempDir()
		other := liveForeignPID(t)
		t.Setenv("PG_GRANDPARENT_PID", strconv.Itoa(other))
		write(t, dir, strconv.Itoa(other)+"\n")
		p := mine
		p.DataDir = dir
		if err := CreateDataDirLockFile(dir, p); err != nil {
			t.Fatal(err)
		}
		ownsLock(t, dir)
	})
}

// TestRemoveDataDirLockFileOnlyOwn pins the exit-time unlink: it removes the
// lock file only while it still names this process.
func TestRemoveDataDirLockFileOnlyOwn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PIDFileName)
	foreign := strconv.Itoa(liveForeignPID(t)) + "\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDataDirLockFile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("foreign lock file removed: %v", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDataDirLockFile(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own lock file still present: %v", err)
	}
	if err := RemoveDataDirLockFile(dir); err != nil {
		t.Fatalf("missing lock file: %v", err)
	}
}
