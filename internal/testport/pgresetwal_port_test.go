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

import (
	"path/filepath"
	"testing"
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
