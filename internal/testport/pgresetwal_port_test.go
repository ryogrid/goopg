package testport

// Port of the CLI-only tier of postgres/src/bin/pg_resetwal/t/001_basic.pl into
// a Go test (M0110-0004).
//
// 001_basic.pl has two tiers:
//
//  1. A command-line-handling tier: --help / --version / options handling, the
//     "too many command-line arguments" / "no data directory specified" /
//     "could not read permissions of directory" cases, and the large block of
//     option-argument validation cases for -c/-e/-l/-m/-o/-O/-u/-x/
//     --wal-segsize/--char-signedness. Every one of these is decided inside
//     pg_resetwal's getopt_long loop (or the immediately-following argument
//     count / DataDir==NULL checks) BEFORE the data directory is ever opened
//     (GetDataDirectoryCreatePerm / read_controlfile run only afterwards — see
//     postgres/src/bin/pg_resetwal/pg_resetwal.c main()). So this tier needs no
//     goopg server and no valid data directory; it is the tier ported here.
//
//  2. A server-dependent tier (upstream lines 15-56, 184-246): it inits a
//     cluster, runs `pg_resetwal -n/--pgdata/--force`, starts the server and
//     SELECTs, then drives the SLRU-derived control-override options
//     (--commit-timestamp-ids / --multixact-ids / --multixact-offset /
//     --oldest-transaction-id / --next-transaction-id, etc.) computed from the
//     real pg_commit_ts / pg_multixact / pg_xact segment files. That tier
//     exercises pg_control read/write round-trips and the on-disk SLRU layout;
//     it is deferred under CSV row RW-002 pending pg_control byte-level
//     compatibility (M0106) plus SLRU-segment-layout parity.
//
// The option-validation cases below pass a deliberately nonexistent data
// directory: the upstream test passes a real `$node->data_dir`, but since the
// option error is emitted during getopt before any directory access, the
// observable result (non-zero exit + the same error text) is identical while
// keeping the port server-free. The two cases that the upstream test expects to
// SUCCEED with a real directory (`-m 0,10`, and the `-c`/`-m`/... overrides that
// rewrite the control file) belong to the deferred server tier, not here.
//
// The port drives the upstream pg_resetwal shipped in
// postgres/local_install/bin (which goopg reuses unchanged); pg_resetwal does
// not link libpq (it never connects to a server), so the plain runTool helper
// suffices — no LD_LIBRARY_PATH shim is needed.
//
// CSV row: RW-001 (the CLI tier of 001_basic.pl). The server tier and
// 002_corrupted.pl remain deferred under row RW-002.
// Design doc: docs/design/0110-0004-pg-resetwal-tap-port.md (M0110-0004).
//
// UPDATE (M0110-0004 loop #45): the pg_control read/write round-trip half of
// the server tier is now ported in TestPort_PgResetwal001BasicServer below.
// The prerequisite — goopg's clean shutdown leaving pg_control State =
// DB_SHUTDOWNED so pg_resetwal accepts the cluster without --force — landed in
// this loop (wal.Checkpointer.CheckpointShutdown, wired into Runtime.Close).
// What stays deferred under RW-002:
//   - the "database server was not shut down cleanly" + --force pair: goopg's
//     stop is always a graceful checkpoint (no crash state in v0), so the
//     unclean-shutdown branch cannot be reproduced;
//   - the SLRU-derived control overrides (--commit-timestamp-ids /
//     --multixact-ids / --multixact-offset / --oldest-transaction-id /
//     --next-transaction-id) and 002_corrupted.pl, which need
//     track_commit_timestamp segment files + exact pg_commit_ts / pg_multixact
//     / pg_xact on-disk segment layout parity.

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgResetwal001Basic ports the CLI-only tier of
// postgres/src/bin/pg_resetwal/t/001_basic.pl.
func TestPort_PgResetwal001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_resetwal/t/001_basic.pl
	bin := clientToolBin(t, "pg_resetwal")
	if bin == "" {
		t.Skip("pg_resetwal not in PATH or postgres/local_install/bin")
	}

	// --- Basic checks: help / version / options handling -----------------
	programHelpOk(t, bin, "pg_resetwal")
	programVersionOk(t, bin, "pg_resetwal")
	programOptionsHandlingOk(t, bin, "pg_resetwal")

	// --- command-line argument handling (no valid data dir required) -----
	// "too many command-line arguments": two bare args, the second is extra.
	commandFailsContaining(t, bin, []string{"foo", "bar"},
		"too many command-line arguments",
		"pg_resetwal: too many command-line arguments")
	// "no data directory specified": no args at all. (PGDATA is not consulted
	// by pg_resetwal, matching the upstream comment "# not used".)
	commandFailsContaining(t, bin, []string{},
		"no data directory specified",
		"pg_resetwal: no data directory specified")
	// "could not read permissions of directory": a nonexistent data directory.
	commandFailsContaining(t, bin, []string{"foo"},
		"could not read permissions of directory",
		"pg_resetwal: nonexistent data directory")

	// --- option-argument validation (decided during getopt) --------------
	// A nonexistent data-dir path is supplied as the positional argument; the
	// option error fires before it is ever touched, so the test stays
	// server-free. Each want string is the meaningful payload of the upstream
	// qr/.../ regex for that case.
	noDir := filepath.Join(t.TempDir(), "nonexistent")
	cases := []struct {
		args []string
		want string
		desc string
	}{
		// -c (commit-timestamp ids: "old,new")
		{[]string{"-c", "foo", noDir}, "invalid argument for option -c", "-c foo"},
		{[]string{"-c", "10,bar", noDir}, "invalid argument for option -c", "-c 10,bar"},
		{[]string{"-c", "1,10", noDir}, "greater than", "-c 1,10"},
		{[]string{"-c", "10,1", noDir}, "greater than", "-c 10,1"},
		// -e (xid epoch)
		{[]string{"-e", "foo", noDir}, "invalid argument for option -e", "-e foo"},
		{[]string{"-e", "-1", noDir}, "must not be -1", "-e -1"},
		// -l (next WAL file name)
		{[]string{"-l", "foo", noDir}, "invalid argument for option -l", "-l foo"},
		// -m (multixact ids: "new,old")
		{[]string{"-m", "foo", noDir}, "invalid argument for option -m", "-m foo"},
		{[]string{"-m", "10,bar", noDir}, "invalid argument for option -m", "-m 10,bar"},
		{[]string{"-m", "10,0", noDir}, "must not be 0", "-m 10,0"},
		// -o (next oid)
		{[]string{"-o", "foo", noDir}, "invalid argument for option -o", "-o foo"},
		{[]string{"-o", "0", noDir}, "must not be 0", "-o 0"},
		// -O (multixact offset)
		{[]string{"-O", "foo", noDir}, "invalid argument for option -O", "-O foo"},
		{[]string{"-O", "-1", noDir}, "must be between 0 and 4294967295", "-O -1"},
		// --wal-segsize
		{[]string{"--wal-segsize", "foo", noDir}, "invalid value", "--wal-segsize foo"},
		{[]string{"--wal-segsize", "13", noDir}, "must be a power", "--wal-segsize 13"},
		// -u (oldest xid)
		{[]string{"-u", "foo", noDir}, "invalid argument for option -u", "-u foo"},
		{[]string{"-u", "1", noDir}, "must be greater than", "-u 1"},
		// -x (next xid)
		{[]string{"-x", "foo", noDir}, "invalid argument for option -x", "-x foo"},
		{[]string{"-x", "1", noDir}, "must be greater than", "-x 1"},
		// --char-signedness
		{[]string{"--char-signedness", "foo", noDir}, "invalid argument for option --char-signedness", "--char-signedness foo"},
	}
	for _, tc := range cases {
		commandFailsContaining(t, bin, tc.args, tc.want, "pg_resetwal: "+tc.desc)
	}
}

// TestPort_PgResetwal001BasicServer ports the pg_control read/write
// round-trip half of the server tier of
// postgres/src/bin/pg_resetwal/t/001_basic.pl (M0110-0004 / RW-002).
//
// It drives the upstream pg_resetwal binary against a real goopg data
// directory and asserts the upstream-equivalent observable results:
//
//   - PGDATA permissions are 0700 (dirs) / 0600 (files)            [upstream l.29]
//   - `pg_resetwal -n` reads pg_control and prints "checkpoint"     [upstream l.19]
//   - `pg_resetwal <dir>` refuses while the server runs
//     ("lock file \"postmaster.pid\" exists")                       [upstream l.39]
//   - `pg_resetwal --pgdata <dir>` succeeds after a CLEAN shutdown,
//     WITHOUT --force — exercising goopg's new DB_SHUTDOWNED state   [upstream l.33]
//   - the server starts and `SELECT 1` works after the reset        [upstream l.35-37]
//   - control-override options apply and the change is visible:
//     `--next-oid 100000` then `--dry-run` shows NextOID = 100000    [upstream l.190-242]
//   - the server starts and works after the override reset          [upstream l.244-245]
//
// pg_resetwal does not link libpq and takes a data directory rather than a
// connection, so it is invoked directly via clientToolBin (not RunClientTool,
// which would inject -h/-p/-U). See the file header for what stays deferred.
func TestPort_PgResetwal001BasicServer(t *testing.T) {
	// upstream: postgres/src/bin/pg_resetwal/t/001_basic.pl (server tier)
	bin := clientToolBin(t, "pg_resetwal")
	if bin == "" {
		t.Skip("pg_resetwal not in PATH or postgres/local_install/bin")
	}

	c := newCluster(t, "pgresetwal001_server")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	dir := c.DataDir()

	// --- PGDATA permissions: 0700 dirs / 0600 files (upstream check_mode_recursive).
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		mode := info.Mode().Perm()
		if d.IsDir() {
			if mode != 0o700 {
				t.Errorf("dir %s has mode %#o, want 0700", p, mode)
			}
		} else if mode != 0o600 {
			t.Errorf("file %s has mode %#o, want 0600", p, mode)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk PGDATA: %v", err)
	}

	// --- `pg_resetwal -n` reads pg_control and prints a checkpoint dump
	// (server not yet started; matches upstream ordering).
	if res := runTool(t, bin, "-n", dir); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal -n exit=%d stderr=%q", res.ExitCode, res.Stderr)
	} else if !strings.Contains(res.Stdout, "checkpoint") {
		t.Errorf("pg_resetwal -n output missing %q:\n%s", "checkpoint", res.Stdout)
	}

	// --- server up: SELECT 1 works, and pg_resetwal refuses while running.
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	started := true
	defer func() {
		if started {
			_ = c.Stop(cluster.ShutdownFast)
		}
	}()
	assertSelect1(t, c)

	if res := runTool(t, bin, dir); res.ExitCode == 0 {
		t.Errorf("pg_resetwal succeeded while server running; want failure")
	} else if !strings.Contains(res.Stderr, "lock file") ||
		!strings.Contains(res.Stderr, "postmaster.pid") {
		t.Errorf("pg_resetwal running-server error missing lock-file text:\n%s", res.Stderr)
	}

	// --- clean shutdown leaves DB_SHUTDOWNED, so `pg_resetwal --pgdata`
	// succeeds WITHOUT --force (the M0110-0004 fix; before it the cluster
	// looked unclean and this required -f).
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	started = false
	if res := runTool(t, bin, "--pgdata", dir); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal --pgdata after clean shutdown exit=%d stderr=%q",
			res.ExitCode, res.Stderr)
	}

	// --- server works after the plain reset.
	if err := c.Start(); err != nil {
		t.Fatalf("start after reset: %v", err)
	}
	started = true
	assertSelect1(t, c)
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop before override: %v", err)
	}
	started = false

	// --- control-override options apply and are observable. We use the
	// non-SLRU subset that goopg round-trips cleanly: --epoch + --next-oid.
	// (--wal-segsize / --next-wal-file and the SLRU-derived ids stay deferred
	// under RW-002 — see the file header.)
	if res := runTool(t, bin, "--pgdata", dir,
		"--epoch", "1", "--next-oid", "100000", "--dry-run"); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal override dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if res := runTool(t, bin, "--pgdata", dir,
		"--epoch", "1", "--next-oid", "100000"); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal override apply exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	// Spot check that the control change was applied (upstream l.239-242).
	res := runTool(t, bin, "--dry-run", dir)
	if res.ExitCode != 0 {
		t.Fatalf("pg_resetwal --dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !containsNextOID(res.Stdout, "100000") {
		t.Errorf("control override not applied; NextOID line missing 100000:\n%s", res.Stdout)
	}

	// --- server starts and works after the override reset.
	if err := c.Start(); err != nil {
		t.Fatalf("start after override reset: %v", err)
	}
	started = true
	assertSelect1(t, c)
}

// assertSelect1 fails the test unless `SELECT 1` returns a single "1".
func assertSelect1(t *testing.T, c *cluster.Cluster) {
	t.Helper()
	rows, err := c.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "1" {
		t.Fatalf("SELECT 1 returned %v, want [[1]]", rows)
	}
}

// containsNextOID reports whether the pg_resetwal dump has a
// "Latest checkpoint's NextOID:" line whose value equals want.
func containsNextOID(out, want string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if fields := strings.Fields(line); strings.Contains(line, "NextOID:") &&
			len(fields) > 0 && fields[len(fields)-1] == want {
			return true
		}
	}
	return false
}
