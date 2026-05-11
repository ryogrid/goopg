package testport

// Ports of postgres/src/bin/pg_basebackup/t/*.pl tests into Go.
//
// Upstream suites: BB-010, BB-011, BB-020, BB-030, BB-040 in
//   docs/test-port/postgres-oracle-port-status.csv.
// Milestone doc: docs/milestones/0095-client-tools-tap-test-porting.md
//
// Each test covers:
//  1. Binary existence check (t.Skip if absent).
//  2. CLI option-validation sub-cases: --help, --version, unknown flag,
//     and mandatory-argument / option-conflict checks that fail before
//     any server connection is attempted.
//  3. t.Skip for the WAL-streaming / physical-replication / logical-replication
//     sub-cases that require pg_basebackup-compatible streaming protocol or a
//     primary + standby cluster — not yet supported in goopg v0.
//
// Binary discovery: PATH first, then postgres/local_install/bin fallback
// (via clientToolBin in client_tools_port_test.go).

import (
	"strings"
	"testing"
)

// TestPort_PgBasebackup010 ports postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - must specify output directory
//   - --compress none:1 fails with "does not accept a compression level"
//   - --compress none+ fails with "unrecognized compression algorithm"
//   - actual backup, WAL-fetching/streaming, incremental, compression, format tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: backup execution sub-cases require pg_basebackup-compatible physical
// streaming protocol which goopg v0 does not expose.
func TestPort_PgBasebackup010(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_basebackup") && !strings.Contains(res.Stderr, "pg_basebackup") {
		t.Fatalf("--help output does not mention pg_basebackup; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// must specify output directory or backup target
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --pgdata should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, "output directory") && !strings.Contains(combined, "backup target") {
		t.Fatalf("expected 'output directory' or 'backup target' in error; got %q", combined)
	}

	// --compress none:1 fails: "none does not accept a compression level"
	res = runTool(t, bin, "--pgdata="+t.TempDir(), "--compress=none:1")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none:1 should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none:1 error; got %q", res.Stdout+res.Stderr)
	}

	// --compress none+ fails: "unrecognized compression algorithm"
	res = runTool(t, bin, "--pgdata="+t.TempDir(), "--compress=none+")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none+ should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none+ error; got %q", res.Stdout+res.Stderr)
	}

	// Backup execution sub-cases deferred: goopg v0 does not implement the
	// pg_basebackup physical streaming protocol (BASE_BACKUP replication command).
	// Remove this Skip when goopg supports BASE_BACKUP streaming.
	t.Skip("pg_basebackup backup execution requires physical streaming protocol " +
		"not yet implemented in goopg v0")
}

// TestPort_PgBasebackup011InPlaceTablespace ports
// postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl.
//
// Upstream tests: backup of a cluster containing an in-place tablespace.
// All sub-cases require a running primary with pg_basebackup physical streaming.
//
// Deferred entirely: in-place tablespace backup requires physical streaming
// replication (--wal-method none still needs BASE_BACKUP protocol) which is
// not yet implemented in goopg v0.
func TestPort_PgBasebackup011InPlaceTablespace(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}

	// All sub-cases require BASE_BACKUP physical streaming.
	// Deferred until goopg implements pg_basebackup-compatible replication protocol.
	t.Skip("in-place tablespace backup requires physical streaming replication " +
		"(BASE_BACKUP protocol) not yet implemented in goopg v0")
}

// TestPort_PgReceivewal020 ports postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - needs target directory
//   - --create-slot + --drop-slot conflict
//   - --create-slot without --slot name
//   - --synchronous + --no-sync conflict
//   - --compress none:1 fails with "does not accept a compression level"
//   - slot creation, WAL streaming, compression, partial-segment, synchronous tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: WAL streaming and slot management require replication protocol.
func TestPort_PgReceivewal020(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl
	bin := clientToolBin(t, "pg_receivewal")
	if bin == "" {
		t.Skip("pg_receivewal not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_receivewal") && !strings.Contains(res.Stderr, "pg_receivewal") {
		t.Fatalf("--help output does not mention pg_receivewal; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	streamDir := t.TempDir()

	// needs target directory
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --directory should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "target directory") && !strings.Contains(res.Stdout+res.Stderr, "directory") {
		t.Fatalf("expected 'target directory' in error; got %q", res.Stdout+res.Stderr)
	}

	// --create-slot and --drop-slot are mutually exclusive
	res = runTool(t, bin, "--directory="+streamDir, "--create-slot", "--drop-slot")
	if res.ExitCode == 0 {
		t.Fatalf("--create-slot + --drop-slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// --create-slot requires --slot
	res = runTool(t, bin, "--directory="+streamDir, "--create-slot")
	if res.ExitCode == 0 {
		t.Fatalf("--create-slot without --slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "slot") {
		t.Fatalf("expected 'slot' in --create-slot error; got %q", res.Stdout+res.Stderr)
	}

	// --synchronous and --no-sync are mutually exclusive
	res = runTool(t, bin, "--directory="+streamDir, "--synchronous", "--no-sync")
	if res.ExitCode == 0 {
		t.Fatalf("--synchronous + --no-sync should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// --compress none:1 fails
	res = runTool(t, bin, "--directory="+streamDir, "--compress=none:1")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none:1 should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none:1 error; got %q", res.Stdout+res.Stderr)
	}

	// WAL streaming, slot creation/drop, and compression sub-cases deferred:
	// goopg v0 does not implement the pg_receivewal streaming replication protocol.
	// Remove this Skip when goopg supports START_REPLICATION / WAL receiver protocol.
	t.Skip("pg_receivewal streaming and slot management require replication protocol " +
		"not yet implemented in goopg v0")
}

// TestPort_PgRecvlogical030 ports postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - needs a slot name
//   - needs a database
//   - needs an action
//   - no destination file specified (when --start given)
//   - logical slot creation, logical decoding, streaming, plugin tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: logical replication streaming and slot management require replication protocol.
func TestPort_PgRecvlogical030(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl
	bin := clientToolBin(t, "pg_recvlogical")
	if bin == "" {
		t.Skip("pg_recvlogical not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_recvlogical") && !strings.Contains(res.Stderr, "pg_recvlogical") {
		t.Fatalf("--help output does not mention pg_recvlogical; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// no slot specified
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "slot") {
		t.Fatalf("expected 'slot' in no-slot error; got %q", res.Stdout+res.Stderr)
	}

	// no database specified
	res = runTool(t, bin, "--slot=test")
	if res.ExitCode == 0 {
		t.Fatalf("no --dbname should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "database") {
		t.Fatalf("expected 'database' in no-dbname error; got %q", res.Stdout+res.Stderr)
	}

	// no action specified
	res = runTool(t, bin, "--slot=test", "--dbname=postgres")
	if res.ExitCode == 0 {
		t.Fatalf("no action should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "action") {
		t.Fatalf("expected 'action' in no-action error; got %q", res.Stdout+res.Stderr)
	}

	// no destination file (--start without --file/-f)
	res = runTool(t, bin, "--slot=test", "--dbname=postgres", "--start")
	if res.ExitCode == 0 {
		t.Fatalf("--start without file should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, "file") && !strings.Contains(combined, "target") {
		t.Fatalf("expected 'file' or 'target' in no-file error; got %q", combined)
	}

	// Logical decoding, slot creation/drop, and streaming sub-cases deferred:
	// goopg v0 does not implement pg_recvlogical logical replication streaming.
	// Remove this Skip when goopg supports CREATE_REPLICATION_SLOT + logical decoding.
	t.Skip("pg_recvlogical logical streaming and slot management require logical " +
		"replication protocol not yet fully supported for pg_recvlogical in goopg v0")
}

// TestPort_PgCreatesubscriber040 ports
// postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - no subscriber data directory specified
//   - no publisher connection string specified
//   - no database name specified
//   - actual subscriber setup (requires running primary + standby cluster)
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: subscriber setup requires physical streaming + logical replication.
func TestPort_PgCreatesubscriber040(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl
	bin := clientToolBin(t, "pg_createsubscriber")
	if bin == "" {
		t.Skip("pg_createsubscriber not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_createsubscriber") && !strings.Contains(res.Stderr, "pg_createsubscriber") {
		t.Fatalf("--help output does not mention pg_createsubscriber; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	tmpDir := t.TempDir()

	// no subscriber data directory specified
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --pgdata should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "data directory") && !strings.Contains(res.Stdout+res.Stderr, "subscriber") {
		t.Fatalf("expected 'data directory' or 'subscriber' in no-pgdata error; got %q",
			res.Stdout+res.Stderr)
	}

	// no publisher connection string specified
	res = runTool(t, bin, "--pgdata="+tmpDir)
	if res.ExitCode == 0 {
		t.Fatalf("no --publisher-server should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "publisher") {
		t.Fatalf("expected 'publisher' in no-publisher-server error; got %q",
			res.Stdout+res.Stderr)
	}

	// no database name specified
	res = runTool(t, bin, "--verbose", "--pgdata="+tmpDir, "--publisher-server=port=5432")
	if res.ExitCode == 0 {
		t.Fatalf("no --database should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "database") {
		t.Fatalf("expected 'database' in no-database error; got %q",
			res.Stdout+res.Stderr)
	}

	// Subscriber setup sub-cases deferred: requires a running primary + standby
	// with physical streaming + logical replication, not yet supported in goopg v0.
	// Remove this Skip when goopg supports pg_createsubscriber-compatible protocol.
	t.Skip("pg_createsubscriber subscriber setup requires physical streaming + " +
		"logical replication not yet supported in goopg v0")
}
