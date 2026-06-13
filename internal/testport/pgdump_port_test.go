package testport

// Port of postgres/src/bin/pg_dump/t/001_basic.pl into a Go test (M0110-0001).
//
// 001_basic.pl is a pure command-line-handling test: it exercises
// --help/--version/options handling and a long list of invalid-option and
// disallowed-combination cases for the pg_dump, pg_restore and pg_dumpall
// client binaries. The upstream comment notes these checks "Doesn't require a
// PG instance to be set up" — every assertion is satisfied by the binary's
// argument parser before any server connection is attempted. The port
// therefore drives the upstream binaries shipped in postgres/local_install/bin
// (which goopg reuses unchanged) and validates that their CLI surface matches
// the expectations the rest of the suite depends on.
//
// The remaining pg_dump suite (002–010) requires broad catalog-view parity and
// a dump/restore round-trip against a live goopg server; those stay deferred
// (rows DU-002..DU-010 in docs/test-port/postgres-oracle-port-status.csv).
//
// Design doc: docs/design/0110-0001-pg-dump-tap-port.md (M0110-0001).
// CSV row: DU-001.

import (
	"strings"
	"testing"
)

// programHelpOk mirrors PostgreSQL::Test::Utils::program_help_ok: the binary
// must exit 0 on --help and mention its own name in the help output.
func programHelpOk(t *testing.T, bin, name string) {
	t.Helper()
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("%s --help exit=%d stderr=%q", name, res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, name) && !strings.Contains(res.Stderr, name) {
		t.Fatalf("%s --help output does not mention %q; stdout=%q", name, name, res.Stdout)
	}
}

// programVersionOk mirrors program_version_ok: --version exits 0.
func programVersionOk(t *testing.T, bin, name string) {
	t.Helper()
	res := runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("%s --version exit=%d stderr=%q", name, res.ExitCode, res.Stderr)
	}
}

// programOptionsHandlingOk mirrors program_options_handling_ok: an unrecognized
// option must produce a non-zero exit.
func programOptionsHandlingOk(t *testing.T, bin, name string) {
	t.Helper()
	res := runTool(t, bin, "--option-does-not-exist")
	if res.ExitCode == 0 {
		t.Fatalf("%s --option-does-not-exist should exit non-0; stdout=%q stderr=%q",
			name, res.Stdout, res.Stderr)
	}
}

// commandFailsContaining mirrors PostgreSQL::Test::Utils::command_fails_like:
// the binary must exit non-zero and its combined stdout+stderr must contain
// the expected literal error text. Upstream uses qr/\Q...\E/ literal-quoted
// regexes whose meaningful payload is a fixed substring, so a Contains check is
// faithful and avoids regex-escaping drift.
func commandFailsContaining(t *testing.T, bin string, args []string, want, desc string) {
	t.Helper()
	res := runTool(t, bin, args...)
	if res.ExitCode == 0 {
		t.Fatalf("%s: expected non-zero exit; stdout=%q stderr=%q", desc, res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, want) {
		t.Fatalf("%s: output %q does not contain %q", desc, combined, want)
	}
}

// TestPort_PgDump001Basic ports postgres/src/bin/pg_dump/t/001_basic.pl.
func TestPort_PgDump001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_dump/t/001_basic.pl
	pgDump := clientToolBin(t, "pg_dump")
	pgRestore := clientToolBin(t, "pg_restore")
	pgDumpall := clientToolBin(t, "pg_dumpall")
	if pgDump == "" || pgRestore == "" || pgDumpall == "" {
		t.Skip("pg_dump/pg_restore/pg_dumpall not in PATH or postgres/local_install/bin")
	}

	// --- Basic checks: help / version / options handling -----------------
	for _, tc := range []struct{ bin, name string }{
		{pgDump, "pg_dump"},
		{pgRestore, "pg_restore"},
		{pgDumpall, "pg_dumpall"},
	} {
		programHelpOk(t, tc.bin, tc.name)
		programVersionOk(t, tc.bin, tc.name)
		programOptionsHandlingOk(t, tc.bin, tc.name)
	}

	// --- pg_dump: invalid options and disallowed combinations ------------
	commandFailsContaining(t, pgDump, []string{"qqq", "abc"},
		`pg_dump: error: too many command-line arguments (first is "abc")`,
		"pg_dump: too many command-line arguments")
	commandFailsContaining(t, pgDump, []string{"-s", "-a"},
		"options -s/--schema-only and -a/--data-only cannot be used together",
		"pg_dump: -s and -a cannot be used together")
	commandFailsContaining(t, pgDump, []string{"-s", "--statistics-only"},
		"options -s/--schema-only and --statistics-only cannot be used together",
		"pg_dump: -s and --statistics-only cannot be used together")
	commandFailsContaining(t, pgDump, []string{"-a", "--statistics-only"},
		"options -a/--data-only and --statistics-only cannot be used together",
		"pg_dump: -a and --statistics-only cannot be used together")
	commandFailsContaining(t, pgDump, []string{"-s", "--include-foreign-data=xxx"},
		"options -s/--schema-only and --include-foreign-data cannot be used together",
		"pg_dump: -s and --include-foreign-data cannot be used together")
	commandFailsContaining(t, pgDump, []string{"--statistics-only", "--no-statistics"},
		"options --statistics-only and --no-statistics cannot be used together",
		"pg_dump: --statistics-only and --no-statistics cannot be used together")
	commandFailsContaining(t, pgDump, []string{"-j2", "--include-foreign-data=xxx"},
		"option --include-foreign-data is not supported with parallel backup",
		"pg_dump: --include-foreign-data not supported with parallel backup")
	commandFailsContaining(t, pgDump, []string{"-c", "-a"},
		"options -c/--clean and -a/--data-only cannot be used together",
		"pg_dump: -c and -a cannot be used together")
	commandFailsContaining(t, pgDump, []string{"--if-exists"},
		"option --if-exists requires option -c/--clean",
		"pg_dump: --if-exists requires -c/--clean")
	commandFailsContaining(t, pgDump, []string{"-j3"},
		"parallel backup only supported by the directory format",
		"pg_dump: parallel backup only supported by directory format")
	// Note the trailing whitespace in the value of --jobs, that is valid.
	commandFailsContaining(t, pgDump, []string{"-j", "-1 "},
		"-j/--jobs must be in range",
		"pg_dump: -j/--jobs must be in range")
	commandFailsContaining(t, pgDump, []string{"-F", "garbage"},
		"invalid output format",
		"pg_dump: invalid output format")
	commandFailsContaining(t, pgDump, []string{"--compress", "garbage"},
		"unrecognized compression algorithm",
		"pg_dump: invalid --compress")
	commandFailsContaining(t, pgDump, []string{"--compress", "none:1"},
		`invalid compression specification: compression algorithm "none" does not accept a compression level`,
		"pg_dump: none does not accept a compression level")
	commandFailsContaining(t, pgDump, []string{"--extra-float-digits", "-16"},
		"--extra-float-digits must be in range",
		"pg_dump: --extra-float-digits must be in range")
	commandFailsContaining(t, pgDump, []string{"--rows-per-insert", "0"},
		"--rows-per-insert must be in range",
		"pg_dump: --rows-per-insert must be in range")
	commandFailsContaining(t, pgDump, []string{"--on-conflict-do-nothing"},
		"option --on-conflict-do-nothing requires option --inserts, --rows-per-insert, or --column-inserts",
		"pg_dump: --on-conflict-do-nothing requires --inserts/...")

	// Compression cases are gated on HAVE_LIBZ upstream. Probe the binary's
	// behaviour rather than its compile config: with zlib, -Z 15 reports a
	// gzip level-range error; without it, gzip is unavailable and the parallel
	// /tar paths are exercised instead.
	probe := runTool(t, pgDump, "-Z", "15")
	haveLibz := strings.Contains(probe.Stdout+probe.Stderr, "between 1 and 9")
	if haveLibz {
		commandFailsContaining(t, pgDump, []string{"-Z", "15"},
			`compression algorithm "gzip" expects a compression level between 1 and 9`,
			"pg_dump: gzip level must be in range")
		commandFailsContaining(t, pgDump, []string{"--compress", "1", "--format", "tar"},
			"compression is not supported by tar archive format",
			"pg_dump: compression not supported by tar format")
		commandFailsContaining(t, pgDump, []string{"-Z", "gzip:nonInt"},
			`invalid compression specification: unrecognized compression option: "nonInt"`,
			"pg_dump: gzip level must be an integer")
	} else {
		commandFailsContaining(t, pgDump, []string{"--format", "tar", "-j3"},
			"parallel backup only supported by the directory format",
			"pg_dump: parallel backup not supported by tar format")
		commandFailsContaining(t, pgDump, []string{"-Z", "gzip:nonInt", "--format", "tar", "-j2"},
			"invalid compression specification: unrecognized compression option",
			"pg_dump: gzip level must be an integer")
	}

	// --- pg_restore: invalid options and disallowed combinations ---------
	commandFailsContaining(t, pgRestore, []string{"qqq", "abc"},
		`pg_restore: error: too many command-line arguments (first is "abc")`,
		"pg_restore: too many command-line arguments")
	commandFailsContaining(t, pgRestore, []string{},
		"one of -d/--dbname and -f/--file must be specified",
		"pg_restore: one of -d/--dbname and -f/--file must be specified")
	commandFailsContaining(t, pgRestore, []string{"-s", "-a", "-f -"},
		"options -s/--schema-only and -a/--data-only cannot be used together",
		"pg_restore: -s and -a cannot be used together")
	commandFailsContaining(t, pgRestore, []string{"-d", "xxx", "-f", "xxx"},
		"options -d/--dbname and -f/--file cannot be used together",
		"pg_restore: -d and -f cannot be used together")
	commandFailsContaining(t, pgRestore, []string{"-c", "-a", "-f -"},
		"options -c/--clean and -a/--data-only cannot be used together",
		"pg_restore: -c and -a cannot be used together")
	commandFailsContaining(t, pgRestore, []string{"-j", "-1", "-f -"},
		"-j/--jobs must be in range",
		"pg_restore: -j/--jobs must be in range")
	commandFailsContaining(t, pgRestore, []string{"--single-transaction", "-j3", "-f -"},
		"cannot specify both --single-transaction and multiple jobs",
		"pg_restore: cannot specify both --single-transaction and multiple jobs")
	commandFailsContaining(t, pgRestore, []string{"--if-exists", "-f -"},
		"option --if-exists requires option -c/--clean",
		"pg_restore: --if-exists requires -c/--clean")
	commandFailsContaining(t, pgRestore, []string{"-f -", "-F", "garbage"},
		`unrecognized archive format "garbage";`,
		"pg_restore: unrecognized archive format")
	commandFailsContaining(t, pgRestore, []string{"-C", "-1", "-f -"},
		"options -C/--create and -1/--single-transaction cannot be used together",
		"pg_restore: -C and -1 cannot be used together")

	// --- pg_dumpall: invalid options and disallowed combinations ---------
	commandFailsContaining(t, pgDumpall, []string{"qqq", "abc"},
		`pg_dumpall: error: too many command-line arguments (first is "qqq")`,
		"pg_dumpall: too many command-line arguments")
	commandFailsContaining(t, pgDumpall, []string{"-g", "-r"},
		"options -g/--globals-only and -r/--roles-only cannot be used together",
		"pg_dumpall: -g and -r cannot be used together")
	commandFailsContaining(t, pgDumpall, []string{"-g", "-t"},
		"options -g/--globals-only and -t/--tablespaces-only cannot be used together",
		"pg_dumpall: -g and -t cannot be used together")
	commandFailsContaining(t, pgDumpall, []string{"-r", "-t"},
		"options -r/--roles-only and -t/--tablespaces-only cannot be used together",
		"pg_dumpall: -r and -t cannot be used together")
	commandFailsContaining(t, pgDumpall, []string{"--if-exists"},
		"option --if-exists requires option -c/--clean",
		"pg_dumpall: --if-exists requires -c/--clean")
	commandFailsContaining(t, pgDumpall, []string{"--exclude-database=foo", "--globals-only"},
		"option --exclude-database cannot be used together with -g/--globals-only",
		"pg_dumpall: --exclude-database cannot be used with -g/--globals-only")
}
