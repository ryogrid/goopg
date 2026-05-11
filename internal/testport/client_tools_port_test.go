package testport

// Ports of PostgreSQL client-tools TAP tests into Go tests.
//
// Tools covered: pg_checksums, pg_controldata, pg_walsummary.
// Upstream suites: C-001/002, CD-001, WS-001/002 in
//   docs/test-port/postgres-oracle-port-status.csv.
// Milestone doc: docs/milestones/0095-client-tools-tap-test-porting.md
//
// Binary discovery: PATH first, then postgres/local_install/bin as fallback.
// All tests call t.Skip if the binary is not available.
//
// pg_controldata and pg_checksums require a global/pg_control file which
// goopg v0 does not write during init. Affected sub-cases note this with
// a comment and test the correct error behaviour instead.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// clientToolBin returns the absolute path to a PostgreSQL client tool.
// It checks PATH first, then falls back to postgres/local_install/bin.
// Returns "" if not found in either location.
func clientToolBin(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	root := repoRoot(t)
	p := filepath.Join(root, "postgres", "local_install", "bin", name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// runTool executes a client tool binary and returns its result.
func runTool(t *testing.T, bin string, args ...string) util.CommandResult {
	t.Helper()
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    args,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return res
}

// TestPort_PgChecksums001Basic ports postgres/src/bin/pg_checksums/t/001_basic.pl.
//
// Upstream tests:
//   - program_help_ok:    pg_checksums --help exits 0
//   - program_version_ok: pg_checksums --version exits 0
//   - program_options_handling_ok: unknown option exits non-0
func TestPort_PgChecksums001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_checksums/t/001_basic.pl
	bin := clientToolBin(t, "pg_checksums")
	if bin == "" {
		t.Skip("pg_checksums not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_checksums") && !strings.Contains(res.Stderr, "pg_checksums") {
		t.Fatalf("--help output does not mention pg_checksums; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok: unrecognised option exits non-0
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
}

// TestPort_PgChecksums002Actions ports postgres/src/bin/pg_checksums/t/002_actions.pl.
//
// Upstream tests: cluster init, checksum enable/disable/check, corruption cases.
//
// Adapted sub-cases (pass in v0):
//   - --filenode cannot be combined with --disable: option conflict detected before data-dir access.
//   - --filenode cannot be combined with --enable:  same.
//
// Deferred (requires global/pg_control which goopg v0 does not write):
//   - checksum enable / disable / check on a data directory.
//   - corruption detection on live page files.
// Remove the deferred comment once goopg writes global/pg_control during init.
func TestPort_PgChecksums002Actions(t *testing.T) {
	// upstream: postgres/src/bin/pg_checksums/t/002_actions.pl
	bin := clientToolBin(t, "pg_checksums")
	if bin == "" {
		t.Skip("pg_checksums not in PATH or postgres/local_install/bin")
	}

	// pg_checksums rejects --filenode when action is --disable (option parsed before
	// the data directory is opened, so an empty tmpDir is sufficient).
	tmpDir := t.TempDir()
	res := runTool(t, bin, "--disable", "--filenode", "1234", "--pgdata", tmpDir)
	if res.ExitCode == 0 {
		t.Fatalf("--filenode with --disable should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// pg_checksums rejects --filenode when action is --enable.
	res = runTool(t, bin, "--enable", "--filenode", "1234", "--pgdata", tmpDir)
	if res.ExitCode == 0 {
		t.Fatalf("--filenode with --enable should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
}

// TestPort_PgControldata001 ports postgres/src/bin/pg_controldata/t/001_pg_controldata.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - pg_controldata without arguments fails
//   - pg_controldata with nonexistent directory fails
//   - pg_controldata <datadir> produces output containing "checkpoint"
//   - CRC-corruption sub-case (writes zeroed pg_control, checks warnings)
//
// Adapted:
//   - CLI validation sub-cases pass unchanged.
//   - Data-directory sub-case: goopg v0 does not write global/pg_control during
//     init, so pg_controldata exits non-zero with a "could not open" error.
//     The test asserts that the failure is a pg_control-access error (not a crash),
//     which is the expected v0 behaviour.  Remove the conditional and assert
//     qr/checkpoint/ output once goopg writes global/pg_control.
//   - CRC-corruption sub-case: deferred (requires a valid pg_control to corrupt).
func TestPort_PgControldata001(t *testing.T) {
	// upstream: postgres/src/bin/pg_controldata/t/001_pg_controldata.pl
	bin := clientToolBin(t, "pg_controldata")
	if bin == "" {
		t.Skip("pg_controldata not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_controldata") && !strings.Contains(res.Stderr, "pg_controldata") {
		t.Fatalf("--help output does not mention pg_controldata; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// command_fails: no argument
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no argument should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// command_fails: nonexistent directory
	res = runTool(t, bin, "/nonexistent-goopg-data-dir-xyz-404")
	if res.ExitCode == 0 {
		t.Fatalf("nonexistent directory should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// cluster init + pg_controldata <datadir>.
	// Adapted from upstream output-check and CRC-corruption sub-case.
	//
	// goopg v0 does not write global/pg_control, so pg_controldata exits non-zero.
	// We verify the error is a pg_control-related file-access error (not a crash
	// or an unrelated error), confirming correct tool behaviour against a v0 data dir.
	// When goopg writes global/pg_control, replace the else-branch with:
	//   if !strings.Contains(res.Stdout, "checkpoint") { t.Fatalf(...) }
	c := newCluster(t, "pgcontroldata001")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	res = runTool(t, bin, c.DataDir())
	if res.ExitCode == 0 {
		// pg_control present (future-proof path): check basic output.
		if !strings.Contains(res.Stdout, "checkpoint") {
			t.Fatalf("pg_controldata output missing 'checkpoint'; stdout=%q", res.Stdout)
		}
	} else {
		// goopg v0: no pg_control; expect a file-access error mentioning pg_control.
		combined := res.Stdout + res.Stderr
		if !strings.Contains(combined, "pg_control") &&
			!strings.Contains(combined, "open") &&
			!strings.Contains(combined, "No such file") {
			t.Fatalf("expected pg_control file-access error; stderr=%q stdout=%q",
				res.Stderr, res.Stdout)
		}
	}
}

// TestPort_PgWalsummary001Basic ports postgres/src/bin/pg_walsummary/t/001_basic.pl.
//
// Upstream tests:
//   - program_help_ok:    pg_walsummary --help exits 0
//   - program_version_ok: pg_walsummary --version exits 0
//   - program_options_handling_ok: unknown option exits non-0
//   - command_fails_like: no input files → "no input files specified"
func TestPort_PgWalsummary001Basic(t *testing.T) {
	// upstream: postgres/src/bin/pg_walsummary/t/001_basic.pl
	bin := clientToolBin(t, "pg_walsummary")
	if bin == "" {
		t.Skip("pg_walsummary not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_walsummary") && !strings.Contains(res.Stderr, "pg_walsummary") {
		t.Fatalf("--help output does not mention pg_walsummary; stdout=%q", res.Stdout)
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

	// command_fails_like: no input files → "no input files specified"
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no input files should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, "no input files") && !strings.Contains(combined, "input files") {
		t.Fatalf("expected 'no input files' in output; got stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
}

// TestPort_PgWalsummary002Blocks ports postgres/src/bin/pg_walsummary/t/002_blocks.pl.
//
// Upstream tests:
//   - cluster init with summarize_wal = on
//   - CREATE TABLE, INSERT, VACUUM FREEZE, CHECKPOINT
//   - pg_available_wal_summaries() returns summaries after checkpoint
//   - pg_stat_io shows walsummarizer reads
//   - pg_walsummary -i <file> reports modified blocks
//
// Adapted sub-cases (pass in v0):
//   - cluster init (without summarize_wal = on — goopg rejects unknown GUCs at startup)
//   - CREATE TABLE, INSERT rows, VACUUM, CHECKPOINT
//
// Deferred (requires WAL summarization not yet implemented in goopg v0):
//   - summarize_wal GUC
//   - pg_available_wal_summaries() built-in function
//   - pg_stat_io walsummarizer backend_type rows
//   - pg_walsummary -i <summary-file> block-level output
//
// Remove the t.Skip call and implement the full test once goopg supports
// WAL summarization (summarize_wal GUC + pg_available_wal_summaries()).
func TestPort_PgWalsummary002Blocks(t *testing.T) {
	// upstream: postgres/src/bin/pg_walsummary/t/002_blocks.pl
	bin := clientToolBin(t, "pg_walsummary")
	if bin == "" {
		t.Skip("pg_walsummary not in PATH or postgres/local_install/bin")
	}

	// Basic cluster-init + SQL portion.
	// goopg v0 does not support the summarize_wal GUC so we omit it;
	// upstream's has_archiving / allows_streaming are also omitted.
	c := newCluster(t, "walsummary002")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// CREATE TABLE and INSERT — adapted: use VALUES instead of generate_series()
	// (pg_current_wal_insert_lsn and generate_series not implemented in goopg v0).
	if _, err := c.Query(context.Background(),
		`CREATE TABLE walsummary_t (a int, b text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO walsummary_t VALUES (1, 'alpha'), (2, 'beta'), (3, 'gamma')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// VACUUM (FREEZE keyword omitted — not supported in goopg v0 parser).
	if _, err := c.Query(context.Background(), `VACUUM walsummary_t`); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}

	// CHECKPOINT via the goopg control plane.
	if err := c.Checkpoint(); err != nil {
		t.Fatalf("CHECKPOINT: %v", err)
	}

	// WAL summarization sub-cases are deferred: goopg v0 does not implement
	// summarize_wal, pg_available_wal_summaries(), or walsummarizer process.
	// The pg_walsummary -i <file> block-output test is therefore also deferred.
	// Unblock by implementing the WAL summarizer and its pg_available_wal_summaries()
	// catalog function, then remove this t.Skip.
	t.Skip("WAL summarization not implemented in goopg v0: " +
		"summarize_wal GUC, pg_available_wal_summaries(), and pg_stat_io " +
		"walsummarizer rows are absent; pg_walsummary -i block test deferred")
}
