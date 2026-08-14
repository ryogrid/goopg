package testport

// Port of postgres/src/bin/pg_amcheck/t/001_basic.pl into a Go test (M0110-0003).
//
// 001_basic.pl is a pure command-line-handling test:
//
//	program_help_ok('pg_amcheck');
//	program_version_ok('pg_amcheck');
//	program_options_handling_ok('pg_amcheck');
//	done_testing();
//
// Every assertion is satisfied by the binary's argument parser before any
// server connection is attempted, so the port drives the upstream pg_amcheck
// shipped in postgres/local_install/bin (which goopg reuses unchanged) and
// validates only its CLI surface.
//
// Unlike pg_dump/pg_waldump, the pg_amcheck binary links a newer libpq symbol
// (PQcancelBlocking, PG 17+) that the host's system libpq.so.5 may not export.
// Loaded against the system library it dies at startup with
// "undefined symbol: PQcancelBlocking". We therefore point LD_LIBRARY_PATH at
// the bundled postgres/local_install/lib so the in-tree libpq is resolved
// (mirrors the env handling other testport binaries already use).
//
// The remaining pg_amcheck suite (002_nonesuch, 003_check, 004_verify_heapam,
// 005_opclass_damage) needs the verify_heapam() SRF and operator-class catalog
// parity against a live goopg server; those stay deferred (rows AC-002..AC-005
// in docs/test-port/postgres-oracle-target-inventory.csv).
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md (M0110-0003).
// CSV row: AC-001.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/util"
)

// runToolWithLib runs a client tool with LD_LIBRARY_PATH pointed at the bundled
// postgres/local_install/lib, so binaries linking in-tree-only libpq symbols
// resolve against the shipped library rather than the host's system libpq.
func runToolWithLib(t *testing.T, bin, libDir string, args ...string) util.CommandResult {
	t.Helper()
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    args,
		Env:     []string{"LD_LIBRARY_PATH=" + libDir},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return res
}

// TestPort_PgAmcheck001Basic ports postgres/src/bin/pg_amcheck/t/001_basic.pl.
func TestPort_PgAmcheck001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_amcheck/t/001_basic.pl
	bin := clientToolBin(t, "pg_amcheck")
	if bin == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")

	// program_help_ok: --help exits 0 and mentions the program name.
	res := runToolWithLib(t, bin, libDir, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("pg_amcheck --help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_amcheck") && !strings.Contains(res.Stderr, "pg_amcheck") {
		t.Fatalf("pg_amcheck --help output does not mention pg_amcheck; stdout=%q", res.Stdout)
	}

	// program_version_ok: --version exits 0.
	res = runToolWithLib(t, bin, libDir, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("pg_amcheck --version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok: an unrecognized option exits non-zero.
	res = runToolWithLib(t, bin, libDir, "--option-does-not-exist")
	if res.ExitCode == 0 {
		t.Fatalf("pg_amcheck --option-does-not-exist should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
}
