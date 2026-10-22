package testport

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestVisibilityMapSurvivesCrashFromPGRecords pins goopg's own crash recovery
// of the visibility map once the map is logged in PG's form: a VACUUM's set
// is restored from XLOG_HEAP2_VISIBLE, and an INSERT's clear from the heap
// record's XLH_INSERT_ALL_VISIBLE_CLEARED, which is now the clear's only
// record (the native rmgr-128 clear is no longer written). Each step crashes
// the server (SIGKILL, so no checkpoint saves the map) and reads the
// recovered fork after a CHECKPOINT.
func TestVisibilityMapSurvivesCrashFromPGRecords(t *testing.T) {
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}
	c := newCluster(t, "vm_crash_replay")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")
	runSQL := func(sql string) string {
		t.Helper()
		host, port, _ := splitHostPort(c.ListenAddr())
		res, err := util.RunCommand(util.CommandSpec{
			Name:    psqlBin,
			Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-tA", "-c", sql},
			Env:     []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("psql failed (exit=%d): %s\n%s\nSQL: %s", res.ExitCode, res.Stderr, res.Stdout, sql)
		}
		return strings.TrimSpace(res.Stdout)
	}
	crashAndRecover := func() {
		t.Helper()
		if err := c.Kill(); err != nil {
			t.Fatalf("kill: %v", err)
		}
		if err := c.Start(); err != nil {
			t.Fatalf("restart after crash: %v", err)
		}
		runSQL("CHECKPOINT") // saves the recovered map to its fork
	}
	vmBits := func(path string) uint8 {
		t.Helper()
		masks, err := storage.ReadVMFork(filepath.Join(c.DataDir(), path+"_vm"))
		if err != nil {
			t.Fatalf("read vm fork: %v", err)
		}
		if len(masks) == 0 {
			return 0
		}
		return masks[0]
	}

	runSQL("CREATE TABLE vmr (a int)")
	runSQL("INSERT INTO vmr SELECT generate_series(1, 10)")
	runSQL("CHECKPOINT")
	path := runSQL("SELECT pg_relation_filepath('vmr')")

	runSQL("VACUUM vmr")
	crashAndRecover()
	if got := vmBits(path); got&storage.VMAllVisible == 0 {
		t.Fatalf("after VACUUM and a crash, block 0 vm bits = %#x, want ALL_VISIBLE (from XLOG_HEAP2_VISIBLE)", got)
	}

	runSQL("INSERT INTO vmr VALUES (11)")
	crashAndRecover()
	if got := vmBits(path); got != 0 {
		t.Fatalf("after an INSERT and a crash, block 0 vm bits = %#x, want 0 (from XLH_INSERT_ALL_VISIBLE_CLEARED)", got)
	}
	if got := runSQL("SELECT count(*) FROM vmr"); got != "11" {
		t.Errorf("rows after recovery = %s, want 11", got)
	}
}
