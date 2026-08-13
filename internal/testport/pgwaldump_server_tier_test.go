package testport

// Port of the server-dependent tier of postgres/src/bin/pg_waldump/t/001_basic.pl
// (upstream lines 80-323) into a Go test (M0119-0005).
//
// The upstream workload spins up a cluster and runs DDL exercising
// heap/btree/hash/gin/gist/spgist/brin indexes, tablespaces, logical messages
// and relmap changes, then runs the upstream pg_waldump binary over the
// produced segments asserting per-rmgr / per-relation / per-block / --limit /
// --fullpage / --stats filtering plus its WAL-range argument handling.
//
// goopg implements none of the hash/gin/gist/spgist/brin access methods (nor
// the jsonb/point types they need), so this port uses a REDUCED heap/btree-only
// workload. None of the filtering assertions target the omitted rmgrs — the
// only rmgr-specific assertion is `--rmgr Btree`, which goopg satisfies via
// EncodeBtreeInsertPG/EncodeBtreeSplitPG — so the filtering coverage is
// complete for every record type goopg actually emits. See
// docs/design/0119-0005-pg-waldump-server-tier-reduced-workload.md.
//
// Two upstream assertions are dropped and recorded in the deferral ledger:
//   - the full workload's hash/gin/gist/spgist/brin index rmgrs (goopg lacks
//     those AMs; nothing in the assertion set targets them);
//   - `--fork init` (goopg emits no init-fork block-reference WAL — no
//     INIT_FORKNUM emit exists, and unlogged mutations skip WAL).
//
// Deviations from the upstream .pl, all forced by goopg gaps and documented:
//   - LSN values are derived from segment filenames rather than
//     pg_walfile_name()/pg_current_wal_insert_lsn() (seeded but not executable
//     in goopg). A segment filename ttttttttxxxxxxxxyyyyyyyy encodes its start
//     LSN as (logid<<32)|(seg<<24) — see segmentStartLSNUint.
//   - the relation-locator DB component is globbed via base/*/<relnode> because
//     pg_database.oid is a legacy display placeholder (same workaround as
//     TestPort_PgWaldump002SaveFullpage).
//   - relnode comes from pg_relation_filenode() (relation OID == filenode).
//
// CSV row: WD-002 (promoted from defer → port).

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgWaldump001BasicServerTier ports the server-dependent tier of
// postgres/src/bin/pg_waldump/t/001_basic.pl against a reduced heap/btree
// workload.
func TestPort_PgWaldump001BasicServerTier(t *testing.T) {
	waldump := clientToolBin(t, "pg_waldump")
	if waldump == "" {
		t.Skip("pg_waldump not in PATH or postgres/local_install/bin")
	}
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}

	c := newCluster(t, "wal_001basic_server")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	addr := c.ListenAddr()
	host, port, _ := splitHostPort(addr)
	root := repoRoot(t)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	psqlEnv := []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir}

	runSQL := func(sql string) string {
		t.Helper()
		res, err := util.RunCommand(util.CommandSpec{
			Name:    psqlBin,
			Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-tA", "-c", sql},
			Env:     psqlEnv,
			Timeout: 30e9,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("psql failed (exit=%d): %s\n%s\nSQL: %s", res.ExitCode, res.Stderr, res.Stdout, sql)
		}
		return strings.TrimSpace(res.Stdout)
	}

	// Reduced heap/btree workload (see the design doc). The upstream .pl sends
	// the whole workload as ONE psql -c batch, with the abort block in the
	// middle. goopg's simple-query dispatch uses one transaction per Query
	// message (not per statement), so a single `…; START TRANSACTION; INSERT;
	// ROLLBACK; …` message would roll the pre-BEGIN statements back with the
	// block — deviating from PG's per-statement autocommit. We split into three
	// messages (pre / abort-block / post) to preserve PG's intended semantics;
	// the message-level single-transaction deviation is recorded in the
	// deferral ledger. Each message's WAL is identical to what the single
	// upstream batch would produce statement-for-statement.
	const workloadPre = `
CREATE TABLE t1 (a int, b text);
CREATE INDEX i1a ON t1 USING btree (a);
INSERT INTO t1 VALUES (1, 'one'), (2, 'two');
DELETE FROM t1 WHERE b = 'one';
`
	const workloadAbort = `
START TRANSACTION;
INSERT INTO t1 VALUES (3, 'three');
ROLLBACK;
`
	const workloadPost = `
CREATE UNLOGGED TABLE t2 (x int);
CREATE INDEX i2 ON t2 USING btree (x);
INSERT INTO t2 SELECT generate_series(1, 10);
VACUUM;
CHECKPOINT;
UPDATE t1 SET b = 'updated' WHERE a = 2;
`
	for _, batch := range []string{workloadPre, workloadAbort, workloadPost} {
		res, err := util.RunCommand(util.CommandSpec{
			Name:    psqlBin,
			Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-c", batch},
			Env:     psqlEnv,
			Timeout: 30e9,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("workload failed (exit=%d): %s\n%s\nSQL:\n%s", res.ExitCode, res.Stderr, res.Stdout, batch)
		}
	}

	// Resolve the relation locators TBLSPC/DB/RELNODE for t1 and i1a. The
	// tablespace component is fixed at 1663 (pg_default, the only value goopg's
	// WAL decoder accepts); the relnode is the relation OID via
	// pg_relation_filenode; the DB component is globbed (see the file comment).
	const pgDefaultTablespace = 1663
	// Relnodes come from pg_class.oid (== the filenode, since goopg uses the
	// relation OID as its filenode). pg_relation_filenode() only resolves
	// tables (its LookupTableByOID arm), not indexes, so the index i1a is
	// resolved through pg_class.oid exactly as the upstream .pl does.
	relT1 := runSQL("SELECT oid FROM pg_class WHERE relname = 't1'")
	relI1a := runSQL("SELECT oid FROM pg_class WHERE relname = 'i1a'")
	if relT1 == "" || relI1a == "" {
		t.Fatalf("could not resolve relnode (t1=%q i1a=%q)", relT1, relI1a)
	}
	dbDirMatches, globErr := filepath.Glob(filepath.Join(c.DataDir(), "base", "*", relT1))
	if globErr != nil || len(dbDirMatches) != 1 {
		t.Fatalf("could not resolve on-disk database dir for t1 relnode %s: matches=%v err=%v",
			relT1, dbDirMatches, globErr)
	}
	dbOID := filepath.Base(filepath.Dir(dbDirMatches[0]))
	relationT1 := fmt.Sprintf("%d/%s/%s", pgDefaultTablespace, dbOID, relT1)
	relationI1a := fmt.Sprintf("%d/%s/%s", pgDefaultTablespace, dbOID, relI1a)
	t.Logf("t1 locator=%s i1a locator=%s", relationT1, relationI1a)

	// Stop cleanly so all WAL is flushed to disk.
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	// Enumerate the non-all-zero WAL segments (the eagerly-preallocated all-zero
	// tail carries no records and would fatal pg_waldump — skip it).
	walDir := filepath.Join(c.DataDir(), "pg_wal")
	segs := listWALSegments(t, walDir)
	var nonZero []string
	for _, s := range segs {
		if segmentIsAllZero(t, filepath.Join(walDir, s)) {
			continue
		}
		nonZero = append(nonZero, s)
	}
	if len(nonZero) == 0 {
		t.Fatal("no non-zero WAL segments found in pg_wal/")
	}
	startSeg, lastSeg := nonZero[0], nonZero[len(nonZero)-1]
	startSegPath := filepath.Join(walDir, startSeg)
	lastSegPath := filepath.Join(walDir, lastSeg)

	// --start is the first segment's exact start LSN (offset 0, which suppresses
	// pg_waldump's "first record is after" info). --end is the end of the last
	// written record — the first zero-fill byte — which goopg does not expose
	// (pg_current_wal_insert_lsn is seeded-but-inexecutable), so we derive it
	// from pg_waldump's own "invalid record length at X/X" report. Using
	// startLSN(last)+16 MiB instead would overshoot into the zero-fill and make
	// pg_waldump abort rather than stop cleanly at --end.
	startLSN := formatLSN(segmentStartLSNUint(startSeg))
	endLSN := endOfWALLSN(t, waldump, libDir, c.DataDir(), startLSN)
	t.Logf("startLSN=%s endLSN=%s (segments %d..%s)", startLSN, endLSN, len(nonZero), lastSeg)

	// --- A. filtering options (mirror upstream test_pg_waldump) -------------
	rmgrLineRE := regexp.MustCompile(`^rmgr: \w`)
	fpwRE := regexp.MustCompile(`\bFPW\b`)
	blk1RE := regexp.MustCompile(`\bblk 1\b`)

	// no options → every line is an rmgr line.
	lines := waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN)
	for _, ln := range lines {
		if !rmgrLineRE.MatchString(ln) {
			t.Errorf("no-options line %q does not match ^rmgr: \\w", ln)
		}
	}

	// --limit 6 → exactly 6 lines.
	lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN, "--limit", "6")
	if len(lines) != 6 {
		t.Errorf("--limit 6: got %d lines, want 6", len(lines))
	}

	// --fullpage → every line carries an FPW image (and at least one does).
	lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN, "--fullpage")
	for _, ln := range lines {
		if !fpwRE.MatchString(ln) {
			t.Errorf("--fullpage line %q lacks FPW", ln)
		}
	}

	// --stats → "WAL statistics" header, no rmgr lines.
	for _, opt := range []string{"--stats", "--stats=record"} {
		lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN, opt)
		if !strings.Contains(lines[0], "WAL statistics") {
			t.Errorf("%s: first line %q does not contain 'WAL statistics'", opt, lines[0])
		}
		for _, ln := range lines {
			if strings.HasPrefix(ln, "rmgr:") {
				t.Errorf("%s: rmgr line %q present in statistics output", opt, ln)
			}
		}
	}

	// --rmgr Btree → only Btree lines (non-vacuous via the INSERTs' first dirty
	// leaf pages).
	lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN, "--rmgr", "Btree")
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "rmgr: Btree") {
			t.Errorf("--rmgr Btree line %q is not a Btree record", ln)
		}
	}

	// --relation <t1> → only records referencing t1's locator (non-vacuous: t1
	// has heap INSERT/DELETE/UPDATE WAL).
	wantT1 := "rel " + relationT1
	lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN, "--relation", relationT1)
	for _, ln := range lines {
		if !strings.Contains(ln, wantT1) {
			t.Errorf("--relation %s line %q does not reference the t1 locator", relationT1, ln)
		}
	}

	// --relation <i1a> --block 1 → only block-1 records for the index (the btree
	// root leaf is block 1, metapage is block 0).
	lines = waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN,
		"--relation", relationI1a, "--block", "1")
	for _, ln := range lines {
		if !blk1RE.MatchString(ln) {
			t.Errorf("--relation i1a --block 1 line %q lacks blk 1", ln)
		}
	}

	// --- B. WAL-range argument handling -------------------------------------
	// The positional-segment "runs" assertions (upstream `command_like … qr/./`)
	// assert exit 0 because the upstream workload fills whole 16 MiB segments.
	// goopg's reduced workload leaves the final segment partial, so pg_waldump
	// prints every record then aborts on the zero-filled tail ("invalid record
	// length … got 0") with a non-zero exit. We assert the positive half — the
	// records were read / --quiet suppressed output / the info message fired —
	// and tolerate that end-of-WAL exit; W-001 already asserts the structural
	// decodability of goopg's segments.
	waldumpFailsContaining(t, waldump, libDir, []string{"foo", "bar"},
		`could not locate WAL file "foo"`, "start file not found")

	res := runToolWithLib(t, waldump, libDir, startSegPath)
	if !strings.Contains(res.Stdout, "rmgr:") {
		t.Errorf("pg_waldump <start_seg>: no rmgr records in stdout %q", res.Stdout)
	}

	waldumpFailsContaining(t, waldump, libDir, []string{startSegPath, "bar"},
		`could not open file "bar"`, "end file not found")

	res = runToolWithLib(t, waldump, libDir, startSegPath, lastSegPath)
	if !strings.Contains(res.Stdout, "rmgr:") {
		t.Errorf("pg_waldump <start_seg> <end_seg>: no rmgr records in stdout %q", res.Stdout)
	}

	waldumpFailsContaining(t, waldump, libDir, []string{"--path", c.DataDir()},
		"no start WAL location given", "path option requires start location")

	// --path --start --end with an exact end position stops cleanly (exit 0).
	if res := runToolWithLib(t, waldump, libDir,
		"--path", c.DataDir(), "--start", startLSN, "--end", endLSN); res.ExitCode != 0 {
		t.Errorf("pg_waldump --path --start --end: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// --path + --start with no --end falls off the end of the WAL.
	waldumpFailsContaining(t, waldump, libDir,
		[]string{"--path", c.DataDir(), "--start", startLSN},
		"error in WAL record at", "falling off the end of the WAL")

	// --quiet <start_seg> → no per-record output (the trailing end-of-WAL error
	// goes to stderr, not stdout).
	res = runToolWithLib(t, waldump, libDir, "--quiet", startSegPath)
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("pg_waldump --quiet <start_seg>: expected empty stdout, got %q", res.Stdout)
	}

	// errors are still shown with --quiet.
	waldumpFailsContaining(t, waldump, libDir,
		[]string{"--quiet", "--path", c.DataDir(), "--start", startLSN},
		"error in WAL record at", "errors shown with --quiet")

	// --start one byte past a segment boundary prints the "first record is
	// after" info message to stderr (before the trailing end-of-WAL error).
	plusOne := formatLSN(segmentStartLSNUint(startSeg) + 1)
	res = runToolWithLib(t, waldump, libDir, "--start", plusOne, startSegPath)
	if !strings.Contains(res.Stderr, "first record is after") {
		t.Errorf("pg_waldump --start <lsn+1>: stderr %q lacks 'first record is after'", res.Stderr)
	}
}

// waldumpLines runs `pg_waldump --path <dataDir> --start <start> --end <end>
// <opts...>` and returns the stdout lines, mirroring upstream test_pg_waldump's
// preconditions: exit 0, empty stderr, and at least one output line. The last
// is also the non-vacuity guard: an empty result set is treated as a failure
// rather than a vacuous green.
func waldumpLines(t *testing.T, waldump, libDir, dataDir, start, end string, opts ...string) []string {
	t.Helper()
	args := append([]string{"--path", dataDir, "--start", start, "--end", end}, opts...)
	res := runToolWithLib(t, waldump, libDir, args...)
	if res.ExitCode != 0 {
		t.Fatalf("pg_waldump %v: exit=%d stderr=%q", opts, res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stderr) != "" {
		t.Fatalf("pg_waldump %v: unexpected stderr %q", opts, res.Stderr)
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		t.Fatalf("pg_waldump %v: no output (non-vacuity guard)", opts)
	}
	return lines
}

// waldumpFailsContaining mirrors command_fails_like for pg_waldump: the binary
// must exit non-zero and its combined stdout+stderr must contain the expected
// literal text.
func waldumpFailsContaining(t *testing.T, bin, libDir string, args []string, want, desc string) {
	t.Helper()
	res := runToolWithLib(t, bin, libDir, args...)
	if res.ExitCode == 0 {
		t.Fatalf("%s: expected non-zero exit; stdout=%q stderr=%q", desc, res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, want) {
		t.Fatalf("%s: output %q does not contain %q", desc, combined, want)
	}
}

// endOfWALLSN derives the end-of-WAL position — the byte offset where the
// eagerly-zero-filled tail of the final segment begins, i.e. one past the last
// written record — by running pg_waldump without --end and parsing its
// "invalid record length at X/X" report. goopg does not execute
// pg_current_wal_insert_lsn/pg_walfile_name, so this is the only way to obtain
// the exact --end position that makes pg_waldump stop cleanly (exit 0) instead
// of aborting on the zero-fill.
func endOfWALLSN(t *testing.T, waldump, libDir, dataDir, startLSN string) string {
	t.Helper()
	res := runToolWithLib(t, waldump, libDir, "--path", dataDir, "--start", startLSN)
	re := regexp.MustCompile(`invalid record length at ([0-9A-F]+/[0-9A-F]+)`)
	if m := re.FindStringSubmatch(res.Stderr); m != nil {
		return m[1]
	}
	t.Fatalf("could not derive end-of-WAL from pg_waldump stderr: %q", res.Stderr)
	return ""
}

// segmentStartLSNUint returns the absolute start LSN (byte offset) of the WAL
// segment named by the 24-hex-char filename ttttttttxxxxxxxxyyyyyyyy, mirroring
// PostgreSQL's XLogFromFileName: startLSN = (logid<<32) | (seg<<24), where
// logid = hex(bytes 8..16) and seg = hex(bytes 16..24) and a segment is 16 MiB.
func segmentStartLSNUint(segName string) uint64 {
	logid, _ := strconv.ParseUint(segName[8:16], 16, 32)
	seg, _ := strconv.ParseUint(segName[16:24], 16, 32)
	return (logid << 32) | (seg << 24)
}

// formatLSN renders a 64-bit LSN in PostgreSQL's "high/low" uppercase-hex form
// (e.g. 0x01000000 → "0/1000000"), matching pg_current_wal_insert_lsn()'s %X/%X
// text format that pg_waldump's --start/--end accept.
func formatLSN(lsn uint64) string {
	return fmt.Sprintf("%X/%X", uint32(lsn>>32), uint32(lsn))
}
