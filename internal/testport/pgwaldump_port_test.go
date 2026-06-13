package testport

// Port of the CLI-only tier of postgres/src/bin/pg_waldump/t/001_basic.pl into
// a Go test (M0110-0002).
//
// 001_basic.pl has two tiers:
//
//  1. A pure command-line-handling tier (upstream lines 10-77): --help /
//     --version / options handling, "no arguments" / "too many arguments",
//     the invalid-argument cases for --block/--fork/--limit/--relation/--rmgr/
//     --start/--end, and the `--rmgr=list` resource-manager listing. Every
//     assertion here is satisfied by the pg_waldump binary's argument parser
//     and built-in rmgr table before any WAL file is opened, so it needs no
//     goopg server. This is the tier ported here.
//
//  2. A server-dependent tier (upstream lines 80-323): it spins up a cluster,
//     runs DDL exercising heap/btree/hash/gin/gist/spgist/brin indexes,
//     tablespaces, logical messages and relmap changes, then runs pg_waldump
//     over the produced segments asserting per-rmgr / per-relation / per-block
//     filtering. That tier is deferred: goopg does not implement the hash, gin,
//     gist, spgist and brin access methods the workload requires, and the
//     WAL-format readability it would prove is already covered for goopg's
//     supported record types by TestPort_WALPgWaldumpCompat (row W-001).
//
// The port drives the upstream pg_waldump shipped in
// postgres/local_install/bin (which goopg reuses unchanged) and validates that
// its CLI surface matches the expectations the rest of the suite depends on.
//
// CSV row: WD-001 (the CLI tier of 001_basic.pl). The server tier and
// 002_save_fullpage.pl remain deferred under row WD-002.

import (
	"strings"
	"testing"
)

// commandLikeMatching mirrors PostgreSQL::Test::Utils::command_like: the binary
// must exit 0 and its stdout must satisfy the caller's predicate.
func commandLikeMatching(t *testing.T, bin string, args []string, want func(string) bool, desc string) {
	t.Helper()
	res := runTool(t, bin, args...)
	if res.ExitCode != 0 {
		t.Fatalf("%s: expected zero exit; exit=%d stderr=%q", desc, res.ExitCode, res.Stderr)
	}
	if !want(res.Stdout) {
		t.Fatalf("%s: stdout did not match expectation; stdout=%q", desc, res.Stdout)
	}
}

// TestPort_PgWaldump001Basic ports the CLI-only tier of
// postgres/src/bin/pg_waldump/t/001_basic.pl.
func TestPort_PgWaldump001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_waldump/t/001_basic.pl
	bin := clientToolBin(t, "pg_waldump")
	if bin == "" {
		t.Skip("pg_waldump not in PATH or postgres/local_install/bin")
	}

	// --- Basic checks: help / version / options handling -----------------
	programHelpOk(t, bin, "pg_waldump")
	programVersionOk(t, bin, "pg_waldump")
	programOptionsHandlingOk(t, bin, "pg_waldump")

	// --- wrong number of arguments ---------------------------------------
	commandFailsContaining(t, bin, []string{},
		"error: no arguments",
		"pg_waldump: no arguments")
	commandFailsContaining(t, bin, []string{"foo", "bar", "baz"},
		"error: too many command-line arguments",
		"pg_waldump: too many arguments")

	// --- invalid option arguments ----------------------------------------
	// These are all rejected by getopt/parse before any WAL file is touched.
	commandFailsContaining(t, bin, []string{"--block", "bad"},
		"error: invalid block number",
		"pg_waldump: invalid block number")
	commandFailsContaining(t, bin, []string{"--fork", "bad"},
		"error: invalid fork name",
		"pg_waldump: invalid fork name")
	commandFailsContaining(t, bin, []string{"--limit", "bad"},
		"error: invalid value",
		"pg_waldump: invalid limit")
	commandFailsContaining(t, bin, []string{"--relation", "bad"},
		"error: invalid relation",
		"pg_waldump: invalid relation specification")
	// Upstream regex is qr/error: resource manager .* does not exist/; with the
	// fixed "bad" argument the message is deterministic.
	commandFailsContaining(t, bin, []string{"--rmgr", "bad"},
		`error: resource manager "bad" does not exist`,
		"pg_waldump: invalid rmgr name")
	commandFailsContaining(t, bin, []string{"--start", "bad"},
		"error: invalid WAL location",
		"pg_waldump: invalid start LSN")
	commandFailsContaining(t, bin, []string{"--end", "bad"},
		"error: invalid WAL location",
		"pg_waldump: invalid end LSN")

	// --- rmgr list -------------------------------------------------------
	// Upstream asserts the exact resource-manager list. The list is built into
	// the binary's rmgr table; if a new rmgr is added upstream this must change
	// in lockstep (the upstream comment makes the same point).
	wantRmgrs := []string{
		"XLOG", "Transaction", "Storage", "CLOG", "Database", "Tablespace",
		"MultiXact", "RelMap", "Standby", "Heap2", "Heap", "Btree", "Hash",
		"Gin", "Gist", "Sequence", "SPGist", "BRIN", "CommitTs",
		"ReplicationOrigin", "Generic", "LogicalMessage",
	}
	expectedList := strings.Join(wantRmgrs, "\n")
	commandLikeMatching(t, bin, []string{"--rmgr=list"},
		func(out string) bool { return strings.TrimRight(out, "\n") == expectedList },
		"pg_waldump: rmgr list")
}
