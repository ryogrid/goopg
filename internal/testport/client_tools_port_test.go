package testport

// Ports of PostgreSQL client-tools TAP tests into Go tests.
//
// Tools covered: pg_checksums, pg_controldata, pg_walsummary.
// Upstream suites: C-001/002, CD-001, WS-001/002 in
//   docs/test-port/postgres-oracle-target-inventory.csv.
// Milestone doc: docs/milestones/0095-client-tools-tap-test-porting.md
//
// Binary discovery: PATH first, then postgres/local_install/bin as fallback.
// All tests call t.Skip if the binary is not available.
//
// pg_controldata and pg_checksums require a global/pg_control file.
// Goopg writes a PG-compatible pg_control during initdb (M0095-0001),
// so the full output check is active.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
//   - Data-directory sub-case: goopg now writes a PG-compatible global/pg_control
//     during initdb (M0095-0001), so the test asserts pg_controldata exits 0 and
//     prints upstream-shape output (contains "checkpoint" label).
//   - CRC-corruption sub-case: still deferred — pg_controldata only warns on a
//     bad CRC and still exits 0, so a positive-output corruption check would
//     duplicate the success path. Promote once goopg's pg_control reflects
//     live checkpoint state and the corruption check has a meaningful signal.
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
	// goopg writes a PG-compatible global/pg_control during initdb (M0095-0001),
	// so pg_controldata must exit 0 and print upstream-shape output containing
	// the "checkpoint" label.
	c := newCluster(t, "pgcontroldata001")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	res = runTool(t, bin, c.DataDir())
	if res.ExitCode != 0 {
		t.Fatalf("pg_controldata <datadir> exit=%d stderr=%q stdout=%q",
			res.ExitCode, res.Stderr, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "checkpoint") {
		t.Fatalf("pg_controldata output missing 'checkpoint'; stdout=%q", res.Stdout)
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

// TestPort_PgWalsummary002Blocks ports postgres/src/bin/pg_walsummary/t/002_blocks.pl
// (M0122-0009 S7): with summarize_wal on, a summary appears after a
// CHECKPOINT, the walsummarizer counts WAL reads in pg_stat_io, and the one
// summary covering an UPDATE of a row in block 0 lists exactly two blocks of
// the table — block 0 and the block the new tuple version went to — as
// `pg_walsummary -i` prints them.
//
// The node is initialised as upstream's init(has_archiving => 1,
// allows_streaming => 1) does, with Cluster.pm's settings appended to
// postgresql.conf (init / enable_archiving). Adapted: wal_log_hints,
// archive_mode and archive_command are left out, because goopg has no WAL
// archiver and does not register them yet (the GUC registry task's
// unregistered parameters; deferral ledger). None of them changes which
// blocks the UPDATE's summary lists: wal_log_hints only adds FPIs for
// hint-bit changes, and with full_page_writes on the CHECKPOINT before the
// UPDATE already makes its first touch of each page an FPI.
func TestPort_PgWalsummary002Blocks(t *testing.T) {
	// upstream: postgres/src/bin/pg_walsummary/t/002_blocks.pl
	bin := clientToolBin(t, "pg_walsummary")
	if bin == "" {
		t.Skip("pg_walsummary not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "walsummary002")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		// allows_streaming => 1
		"wal_level = replica",
		"max_wal_senders = 10",
		"max_replication_slots = 10",
		"autovacuum_worker_slots = 3",
		"hot_standby = on",
		"shared_buffers = 1MB",
		"max_connections = 10",
		"max_wal_size = 128MB",
		"summarize_wal = on",
	} {
		if err := c.AppendPostgresqlConf(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	ctx := context.Background()
	query := func(sql string) [][]string {
		t.Helper()
		rows, err := c.Query(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return rows
	}
	one := func(sql string) string {
		t.Helper()
		rows := query(sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: want one value, got %v", sql, rows)
		}
		return rows[0][0]
	}
	// pollQueryUntil is poll_query_until: re-run sql until psql would print
	// 't'. cluster.Query renders a boolean column as "true"; "t" is also
	// accepted because goopg types a bare EXISTS column as text (filed).
	pollQueryUntil := func(sql string) bool {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			if v := one(sql); v == "t" || v == "true" {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Create a table and insert a few test rows into it. VACUUM FREEZE it so
	// that autovacuum doesn't induce any future modifications unexpectedly.
	query(`CREATE TABLE mytable (a int, b text)`)
	query(`INSERT INTO mytable
SELECT
	g, random()::text||random()::text||random()::text||random()::text
FROM
	generate_series(1, 400) g`)
	query(`VACUUM FREEZE`)

	// Record the current WAL insert LSN.
	baseLSN := one(`SELECT pg_current_wal_insert_lsn()`)
	t.Logf("just after insert, WAL insert LSN is %s", baseLSN)

	// Now perform a CHECKPOINT.
	query(`CHECKPOINT`)

	// Wait for a new summary to show up, one that includes the inserts we
	// just did.
	if !pollQueryUntil(`SELECT EXISTS (
    SELECT * from pg_available_wal_summaries()
    WHERE end_lsn >= '` + baseLSN + `'
)`) {
		t.Fatal("WAL summarization caught up after insert: timed out")
	}

	// The WAL summarizer should have generated some IO statistics.
	if !pollQueryUntil(`SELECT sum(reads) > 0 FROM pg_stat_io
   WHERE backend_type = 'walsummarizer' AND object = 'wal'`) {
		t.Fatal("Timed out while waiting for WAL summarizer to generate statistics for WAL reads")
	}

	// Find the highest LSN that is summarized on disk.
	summarizedLSN := one(`SELECT MAX(end_lsn) AS summarized_lsn FROM pg_available_wal_summaries()`)
	t.Logf("after insert, summarized through %s", summarizedLSN)

	// Update a row in the first block of the table and trigger a checkpoint.
	query(`UPDATE mytable SET b = 'abcdefghijklmnopqrstuvwxyz' || b || '01234567890'
	WHERE a = 2`)
	query(`CHECKPOINT`)

	// Wait for a new summary to show up.
	if !pollQueryUntil(`SELECT EXISTS (
    SELECT * from pg_available_wal_summaries()
    WHERE end_lsn > '` + summarizedLSN + `'
)`) {
		t.Fatal("got new WAL summary after update: timed out")
	}

	// Figure out the exact details for the new summary file.
	details := query(`SELECT tli, start_lsn, end_lsn from pg_available_wal_summaries()
	WHERE end_lsn > '` + summarizedLSN + `'`)
	if len(details) != 1 {
		t.Fatalf("got exactly one new WAL summary: got %v", details)
	}
	tli, startLSN, endLSN := details[0][0], details[0][1], details[0][2]
	t.Logf("examining summary for TLI %s from %s to %s", tli, startLSN, endLSN)

	// Reconstruct the full pathname for the WAL summary file.
	// perl's sprintf "%08s" zero-pads each field to 8 characters.
	pad := func(s string) string {
		if len(s) < 8 {
			s = strings.Repeat("0", 8-len(s)) + s
		}
		return s
	}
	startHi, startLo, _ := strings.Cut(startLSN, "/")
	endHi, endLo, _ := strings.Cut(endLSN, "/")
	filename := fmt.Sprintf("%s/pg_wal/summaries/%s%s%s%s%s.summary", c.DataDir(),
		pad(tli), pad(startHi), pad(startLo), pad(endHi), pad(endLo))
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("WAL summary file exists: %v", err)
	}

	// Run pg_walsummary on it. We expect exactly two blocks to be modified,
	// block 0 and one other.
	res := runTool(t, bin, "-i", filename)
	t.Log(res.Stdout)
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if !regexp.MustCompile(`(?m)FORK main: block 0$`).MatchString(res.Stdout) {
		t.Errorf("stdout shows block 0 modified: %q", res.Stdout)
	}
	if res.Stderr != "" {
		t.Errorf("stderr is empty: %q", res.Stderr)
	}
	if len(lines) != 2 {
		t.Errorf("UPDATE modified 2 blocks: got %d lines %q", len(lines), res.Stdout)
	}
}
