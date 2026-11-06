package testport

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_GoopgSigquitIsImmediateShutdown pins SIGQUIT as the postmaster's
// immediate shutdown (pmdie → "received immediate shutdown request"; what
// pg_ctl stop -m immediate sends): the server exits 0 at once, writes no
// shutdown checkpoint — pg_control stays "in production" — and removes
// postmaster.pid; the next start recovers the committed data from WAL.
// goopg did not catch SIGQUIT, so Go's default action dumped every
// goroutine and exited 2 with postmaster.pid left behind.
func TestE2E_GoopgSigquitIsImmediateShutdown(t *testing.T) {
	c, err := cluster.New("sigquit", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := runSQLSimple(t, c, "CREATE TABLE q(a int); INSERT INTO q VALUES (42)"); err != nil {
		t.Fatal(err)
	}

	pidFile := filepath.Join(c.DataDir(), "postmaster.pid")
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.SplitN(string(raw), "\n", 2)[0])
	if err != nil {
		t.Fatalf("pid line: %v", err)
	}
	if err := syscall.Kill(pid, syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("server still running 20 s after SIGQUIT")
		}
		time.Sleep(50 * time.Millisecond)
	}
	logData, _ := os.ReadFile(c.LogPath())
	if !strings.Contains(string(logData), "received immediate shutdown request") {
		t.Errorf("server log lacks PG's \"received immediate shutdown request\"")
	}
	if strings.Contains(string(logData), "SIGQUIT: quit") || strings.Contains(string(logData), "goroutine 1 [") {
		t.Errorf("SIGQUIT hit Go's default action (goroutine dump)")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("postmaster.pid left behind after the immediate shutdown (err %v)", err)
	}
	cd, err := control.ReadControlFile(c.DataDir())
	if err != nil || cd == nil {
		t.Fatalf("pg_control: %v", err)
	}
	if cd.State != control.DBStateInProduction {
		t.Errorf("pg_control state %s after SIGQUIT, want in production (no shutdown checkpoint)", control.DBStateName(cd.State))
	}

	// The server is gone; clear the helper's process handle (Kill treats
	// an already-exited process group as success) before restarting.
	_ = c.Kill()
	if err := c.Start(); err != nil {
		t.Fatalf("restart after the immediate shutdown: %v", err)
	}
	if got := queryScalar(t, c, "SELECT a FROM q"); got != "42" {
		t.Errorf("after recovery: SELECT a FROM q = %q, want 42", got)
	}
}
