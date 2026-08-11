package pgcluster

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestClusterKillHard pins the M0131-S28.0 prerequisite: pgcluster needs a
// *true* SIGKILL of the postmaster process group, because the crash-interchange
// E2E (M0131-S28) must hand goopg the directory a power-loss leaves behind.
//
// The two subtests are a paired discovery probe:
//
//   - `killhard` asserts KillHard() leaves `postmaster.pid` in place (nothing
//     ran on_proc_exit → UnlinkLockFiles), kills every backend and not just the
//     postmaster, and leaves a directory PG itself can crash-recover from with
//     no committed rows lost.
//   - `pg_ctl_immediate` records why Kill() cannot stand in for it: SIGQUIT
//     still lets the postmaster unlink its lock file
//     (postgres/src/backend/utils/init/miscinit.c UnlinkLockFiles).
func TestClusterKillHard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	bin := upstreamBinDir(t)
	Available(t, bin)

	t.Run("killhard", func(t *testing.T) {
		c := startScratch(t, bin, "killhard")

		c.Exec(t, "CREATE TABLE crashy (id int)")
		c.Exec(t, "INSERT INTO crashy SELECT generate_series(1, 500)")

		// Pin a live backend so we can prove the group kill reached the
		// postmaster's children, not just the postmaster.
		db, err := c.OpenDB()
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		var backendPID int
		if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID); err != nil {
			t.Fatalf("pg_backend_pid: %v", err)
		}

		postmasterPID := c.postgresCmd.Process.Pid
		pgid, err := syscall.Getpgid(postmasterPID)
		if err != nil {
			t.Fatalf("Getpgid(%d): %v", postmasterPID, err)
		}
		if pgid != postmasterPID {
			t.Fatalf("postmaster pgid=%d want %d — Start() must set Setpgid so KillHard can group-kill",
				pgid, postmasterPID)
		}

		if err := c.KillHard(); err != nil {
			t.Fatalf("KillHard: %v", err)
		}

		// 1. The lock file survives — this is the whole point of the helper.
		pidPath := filepath.Join(c.DataDir(), "postmaster.pid")
		if _, err := os.Stat(pidPath); err != nil {
			t.Fatalf("postmaster.pid missing after KillHard (%v) — the postmaster ran its exit hooks, so this was not a SIGKILL", err)
		}

		// 2. Every process in the group is gone, backends included.
		for _, pid := range []int{postmasterPID, backendPID} {
			if err := waitProcessGone(pid, 10*time.Second); err != nil {
				t.Fatalf("pid %d still alive after KillHard: %v", pid, err)
			}
		}

		// 3. The directory is still a crash-recoverable cluster: PG replays
		//    its WAL and no committed row is lost. Start() must first evict
		//    the stale lock file the way an operator would — PG refuses to
		//    start while a postmaster.pid it cannot disprove is present.
		if err := os.Remove(pidPath); err != nil {
			t.Fatalf("remove stale postmaster.pid: %v", err)
		}
		if err := c.Start(); err != nil {
			t.Fatalf("restart after crash: %v", err)
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rcancel()
		if err := c.WaitReady(rctx, 30*time.Second); err != nil {
			t.Fatalf("WaitReady after crash recovery: %v", err)
		}
		if got := c.QueryScalar(t, "SELECT count(*) FROM crashy"); got != "500" {
			t.Fatalf("count after crash recovery = %q want 500", got)
		}
	})

	t.Run("pg_ctl_immediate", func(t *testing.T) {
		c := startScratch(t, bin, "immediate")

		if err := c.Kill(); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		// Documented contrast, not a wish: an immediate stop is SIGQUIT, so
		// the postmaster reaches UnlinkLockFiles and the crash evidence is
		// cleaned up behind it.
		pidPath := filepath.Join(c.DataDir(), "postmaster.pid")
		if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
			t.Fatalf("postmaster.pid still present after `pg_ctl -m immediate stop` (stat err=%v) — "+
				"upstream behaviour changed; re-check whether KillHard is still needed", err)
		}
		// Stop() must stay safe on an already-dead cluster (the deferred
		// teardown below calls it).
		c.postgresCmd = nil
	})
}

// startScratch brings up a throwaway upstream cluster and registers teardown.
func startScratch(t *testing.T, bin, name string) *Cluster {
	t.Helper()
	c, err := New(name, Options{
		BinDir:      bin,
		StartupWait: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Stop()
		_ = os.RemoveAll(filepath.Dir(c.DataDir()))
	})
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx, 30*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	return c
}

// waitProcessGone polls until pid no longer names a live process. A zombie
// still answers signal 0, so /proc state is consulted as well — the postmaster
// is our own child until KillHard reaps it, and its backends are re-parented.
func waitProcessGone(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err == nil && isZombie(pid) {
			return nil
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	if last == nil {
		last = syscall.EPERM
	}
	return last
}

func isZombie(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true // gone from /proc entirely
	}
	// state is the field after the (possibly space-containing) comm field.
	idx := strings.LastIndex(string(data), ") ")
	if idx < 0 || idx+2 >= len(data) {
		return false
	}
	return data[idx+2] == 'Z'
}
