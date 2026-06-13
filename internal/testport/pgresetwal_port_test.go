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
// CSV row: RW-001 (the CLI tier of 001_basic.pl). The server tier of
// 001_basic.pl is split: its pg_control read/write round-trip half is ported
// (RW-003, TestPort_PgResetwal001BasicServer); the SLRU-derived-override
// restart remainder stays deferred under RW-002.
// 002_corrupted.pl is ported in TestPort_PgResetwal002Corrupted below (RW-004).
// Design doc: docs/design/0110-0004-pg-resetwal-tap-port.md (M0110-0004).
//
// UPDATE (M0110-0004 loop #45): the pg_control read/write round-trip half of
// the server tier is now ported in TestPort_PgResetwal001BasicServer below.
// The prerequisite — goopg's clean shutdown leaving pg_control State =
// DB_SHUTDOWNED so pg_resetwal accepts the cluster without --force — landed in
// this loop (wal.Checkpointer.CheckpointShutdown, wired into Runtime.Close).
// The maximal SLRU-derived-override RESTART (--next-transaction-id advances
// NextXID past the bootstrap pg_xact segment) now also passes: M0110-0004
// RW-002 (a) batched the startup CLOG implicit-abort sweep's SLRU mirror to one
// fsync per segment (was one per XID → ~1M fsyncs → looked hung).
// What stays deferred under RW-002:
//   - the "database server was not shut down cleanly" + --force pair: goopg's
//     stop is always a graceful checkpoint (no crash state in v0), so the
//     unclean-shutdown branch cannot be reproduced.
//
// 002_corrupted.pl is now ported in TestPort_PgResetwal002Corrupted (RW-004);
// it needs no server, only goopg's pg_control header compatibility (RW-003).

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

	// --- a control override that goopg both round-trips AND restarts from:
	// --epoch / --next-oid plus the multixact and commit-timestamp id
	// overrides. (Unlike an advanced transaction id, these do not force goopg's
	// CLOG open to walk a large xid range — see the full-override block below.)
	supported := []string{"--pgdata", dir,
		"--epoch", "1", "--next-oid", "100000",
		"--multixact-ids", "65536,1", "--multixact-offset", "52352",
		"--commit-timestamp-ids", "3,0"}
	if res := runTool(t, bin, append(append([]string(nil), supported...), "--dry-run")...); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal supported-override dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if res := runTool(t, bin, supported...); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal supported-override apply exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if out := runTool(t, bin, "--dry-run", dir).Stdout; !containsNextOID(out, "100000") {
		t.Errorf("supported override not applied; NextOID missing 100000:\n%s", out)
	}
	// server restarts and works after the supported override reset (upstream l.244-245).
	if err := c.Start(); err != nil {
		t.Fatalf("start after supported override reset: %v", err)
	}
	started = true
	assertSelect1(t, c)
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop before full override: %v", err)
	}
	started = false

	// --- full SLRU-derived override round-trip (upstream l.184-242). This
	// mirrors the upstream override block faithfully: it reads the cluster's
	// REAL SLRU segment files (pg_commit_ts / pg_multixact{offsets,members} /
	// pg_xact) and derives the --commit-timestamp-ids / --multixact-ids /
	// --multixact-offset / --oldest-transaction-id / --next-transaction-id
	// values from them with the same block-size multipliers, applied together
	// with --epoch / --next-wal-file / --next-oid / --wal-segsize. It confirms
	// pg_control round-trips EVERY override field (goopg's on-disk SLRU layout
	// parity + control read/write under upstream pg_resetwal).
	//
	// The upstream final restart (l.244-245) is exercised below. It used to be
	// deferred under RW-002 because --next-transaction-id advances NextXID far
	// past the bootstrap pg_xact segment, so initdb.Open's implicit-abort sweep
	// (CLog.MarkUnknownAsAborted) stamps ~1M XIDs; the old per-XID SLRU mirror
	// fsynced every one and startup looked hung. M0110-0004 RW-002 (a) batched
	// that mirror to one fsync per segment, so the restart completes promptly.
	blcksz := parseBlockSize(t, runTool(t, bin, "--dry-run", dir).Stdout)

	// commit-timestamp ids: "old,new"; old defaults to 3 when the first
	// segment is 0 (or absent — goopg has no pg_commit_ts segment, matching
	// hex(undef)==0 in the upstream Perl).
	ctFirst, ctLast := slruBounds(t, dir, "pg_commit_ts")
	ctOld := ctFirst
	if ctOld == 0 {
		ctOld = 3
	}
	commitTsIDs := fmt.Sprintf("%d,%d", ctOld, ctLast)

	// multixact ids: "new,old". new = (last+1)*mult; old defaults to 1.
	offFirst, offLast := slruBounds(t, dir, filepath.Join("pg_multixact", "offsets"))
	offMult := uint64(32) * uint64(blcksz) / 4
	mxOld := uint64(1)
	if offFirst != 0 {
		mxOld = offFirst * offMult
	}
	multixactIDs := fmt.Sprintf("%d,%d", (offLast+1)*offMult, mxOld)

	// multixact offset: (last+1)*mult, mult uses integer division by 20.
	_, memLast := slruBounds(t, dir, filepath.Join("pg_multixact", "members"))
	memMult := uint64(32) * uint64(blcksz/20) * 4
	multixactOffset := strconv.FormatUint((memLast+1)*memMult, 10)

	// oldest / next transaction id from pg_xact.
	xactFirst, xactLast := slruBounds(t, dir, "pg_xact")
	xactMult := uint64(32) * uint64(blcksz) * 4
	oldestXID := uint64(3)
	if xactFirst != 0 {
		oldestXID = xactFirst * xactMult
	}
	nextXID := (xactLast + 1) * xactMult

	override := []string{
		"--pgdata", dir,
		"--epoch", "1",
		"--next-wal-file", "00000001000000320000004B",
		"--next-oid", "100000",
		"--wal-segsize", "1",
		"--commit-timestamp-ids", commitTsIDs,
		"--multixact-ids", multixactIDs,
		"--multixact-offset", multixactOffset,
		"--oldest-transaction-id", strconv.FormatUint(oldestXID, 10),
		"--next-transaction-id", strconv.FormatUint(nextXID, 10),
	}
	if res := runTool(t, bin, append(append([]string(nil), override...), "--dry-run")...); res.ExitCode != 0 {
		t.Fatalf("pg_resetwal override dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if res := runTool(t, bin, override...); res.ExitCode != 0 {
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

	// Upstream final restart (001_basic.pl l.244-245): the server must come
	// back up after the maximal override. --next-transaction-id advanced
	// NextXID past the bootstrap pg_xact segment, so initdb.Open's implicit-
	// abort sweep (CLog.MarkUnknownAsAborted) stamps ~1M XIDs. Before
	// M0110-0004 RW-002 (a) that sweep fsynced once per XID and startup looked
	// hung; the batched SLRU mirror (one fsync per segment) makes this restart
	// complete promptly.
	if err := c.Start(); err != nil {
		t.Fatalf("start after maximal override reset: %v", err)
	}
	started = true
	assertSelect1(t, c)
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop after maximal override restart: %v", err)
	}
	started = false
}

// TestPort_PgResetwal002Corrupted ports
// postgres/src/bin/pg_resetwal/t/002_corrupted.pl (M0110-0004 / RW-002):
// pg_resetwal's handling of a corrupted global/pg_control.
//
// The test inits a goopg cluster (never started), corrupts pg_control two
// ways, and drives the upstream pg_resetwal binary against it:
//
//  1. ALL ZEROES — pg_resetwal cannot match PG_CONTROL_VERSION, so it warns
//     "pg_control exists but is broken or wrong version; ignoring it",
//     GuessControlValues(), and (under --dry-run) prints the guessed dump
//     (which starts with "pg_control version number"). Exit 0.
//  2. HEADER RESTORED, REST ZEROED — the first 16 bytes (system_identifier +
//     pg_control_version + catalog_version_no) make pg_control_version match
//     PG_CONTROL_VERSION, taking the "different code path" (read_controlfile
//     succeeds the version check but the CRC fails over the zeroed body), so
//     pg_resetwal warns "pg_control specifies invalid WAL segment size (0
//     bytes); proceed with caution", marks the values guessed, and again
//     prints the dump under --dry-run. Exit 0.
//  3. PLAIN RUN (no --force) — guessed values block the reset:
//     "not proceeding because control file values were guessed". Exit 1.
//  4. --force — proceeds and rewrites the control file. Exit 0.
//
// Steps 1–4 are generic pg_resetwal logic (pg_resetwal.c read_controlfile /
// main). The single goopg-specific dependency is step 2: goopg's pg_control
// header must carry a pg_control_version that upstream pg_resetwal recognizes,
// so the header-restored path is reached rather than the broken-version path.
// That byte-level pg_control compatibility was proven by RW-003
// (TestPort_PgResetwal001BasicServer); this test exercises the guessed-values
// code path on top of it. No server is started, so it is independent of the
// CLOG-startup limitation that defers the maximal-override restart.
func TestPort_PgResetwal002Corrupted(t *testing.T) {
	// upstream: postgres/src/bin/pg_resetwal/t/002_corrupted.pl
	bin := clientToolBin(t, "pg_resetwal")
	if bin == "" {
		t.Skip("pg_resetwal not in PATH or postgres/local_install/bin")
	}

	c := newCluster(t, "pgresetwal002_corrupted")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	pgControl := filepath.Join(c.DataDir(), "global", "pg_control")

	// Read out the head of the file to get PG_CONTROL_VERSION in particular
	// (upstream reads the first 16 bytes).
	orig, err := os.ReadFile(pgControl)
	if err != nil {
		t.Fatalf("read pg_control: %v", err)
	}
	size := len(orig)
	if size < 16 {
		t.Fatalf("pg_control is only %d bytes; expected >= 16", size)
	}
	header := append([]byte(nil), orig[:16]...)

	// --- (1) Fill pg_control with zeros: broken/wrong version, ignored.
	if err := os.WriteFile(pgControl, make([]byte, size), 0o600); err != nil {
		t.Fatalf("zero pg_control: %v", err)
	}
	res := runTool(t, bin, "--dry-run", c.DataDir())
	if res.ExitCode != 0 {
		t.Fatalf("all-zero --dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_control version number") {
		t.Errorf("all-zero --dry-run stdout missing %q:\n%s", "pg_control version number", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "pg_control exists but is broken or wrong version; ignoring it") {
		t.Errorf("all-zero --dry-run stderr missing broken-version warning:\n%s", res.Stderr)
	}

	// --- (2) Restore the saved 16-byte header, zero the rest: this uses a
	// different code path internally, allowing pg_resetwal to process a zero
	// WAL segment size.
	restored := make([]byte, size)
	copy(restored, header)
	if err := os.WriteFile(pgControl, restored, 0o600); err != nil {
		t.Fatalf("restore pg_control header: %v", err)
	}
	res = runTool(t, bin, "--dry-run", c.DataDir())
	if res.ExitCode != 0 {
		t.Fatalf("header-only --dry-run exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_control version number") {
		t.Errorf("header-only --dry-run stdout missing %q:\n%s", "pg_control version number", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "pg_control specifies invalid WAL segment size (0 bytes); proceed with caution") {
		t.Errorf("header-only --dry-run stderr missing zero-segsize warning:\n%s", res.Stderr)
	}

	// --- (3) A real run refuses while the values were guessed (no --force).
	res = runTool(t, bin, c.DataDir())
	if res.ExitCode == 0 {
		t.Errorf("pg_resetwal succeeded on guessed values without --force; want failure")
	}
	if !strings.Contains(res.Stderr, "not proceeding because control file values were guessed") {
		t.Errorf("guessed-values failure missing expected message:\n%s", res.Stderr)
	}

	// --- (4) --force proceeds and rewrites the control file.
	res = runTool(t, bin, "--force", c.DataDir())
	if res.ExitCode != 0 {
		t.Errorf("pg_resetwal --force on guessed values exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
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

// segNameRe matches a PostgreSQL SLRU segment file name (hex digits), mirroring
// the upstream get_slru_files grep /[0-9A-F]+/.
var segNameRe = regexp.MustCompile(`^[0-9A-F]+$`)

// parseBlockSize extracts the "Database block size:" value from a pg_resetwal
// --dry-run dump (upstream l.187).
func parseBlockSize(t *testing.T, out string) int {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Database block size:") {
			f := strings.Fields(line)
			n, err := strconv.Atoi(f[len(f)-1])
			if err != nil {
				t.Fatalf("parse block size from %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatalf("no 'Database block size:' line in pg_resetwal dump:\n%s", out)
	return 0
}

// slruBounds returns the hex values of the first and last SLRU segment file
// names (sorted) under <dir>/<sub>, mirroring the upstream get_slru_files +
// hex($files[0]) / hex($files[-1]). An empty directory yields (0, 0), matching
// the Perl hex(undef)==0 behavior.
func slruBounds(t *testing.T, dir, sub string) (first, last uint64) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, sub))
	if err != nil {
		t.Fatalf("read SLRU dir %s: %v", sub, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && segNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return 0, 0
	}
	sort.Strings(names)
	return parseHexSeg(t, names[0]), parseHexSeg(t, names[len(names)-1])
}

// parseHexSeg parses a hex SLRU segment file name into its numeric value.
func parseHexSeg(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		t.Fatalf("parse hex segment name %q: %v", s, err)
	}
	return v
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
