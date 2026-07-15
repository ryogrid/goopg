package testport

// wal_pg_waldump_test.go — TestPort_WALPgWaldumpCompat
//
// Verifies that goopg's PG-compatible WAL format (PageHeaders=true,
// enabled by M0101-0001) can be parsed by the upstream pg_waldump binary.
// This is the oracle-test gate for M0101-0003.
//
// Test flow:
//  1. Start a goopg cluster (which now writes PG-compatible WAL).
//  2. Run a small workload: CREATE TABLE + INSERT 100 rows + CHECKPOINT.
//  3. Stop the cluster cleanly.
//  4. Enumerate pg_wal/ segments (native PG-format names, TLI=1 prefix).
//  5. Run pg_waldump --quiet directly on each segment (pg_waldump infers the
//     timeline and start LSN from the filename — no alias rewriting needed).
//  6. Assert no structural error (notably "incorrect prev-link") for any
//     segment — this is the regression guard for the xl_prev seeding fix in
//     internal/wal/writer.go (detectWritePos). End-of-WAL on the trailing
//     segment is expected and ignored.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_WALPgWaldumpCompat verifies that goopg's WAL segments are
// readable by pg_waldump. M0101-0003.
func TestPort_WALPgWaldumpCompat(t *testing.T) {
	t.Skip("PG-tool WAL compat intentionally removed 2026-07-15 — not a regression. " +
		"goopg now emits real PG (xl_rmid,xl_info) headers over still-native record " +
		"bodies (docs/design/wal-native-pg-format/04), so pg_waldump can no longer " +
		"structurally parse goopg WAL. Re-enable after the native->PG content rewrite " +
		"(docs 01/03). See .ralph/deferral_ledger.md.")
	waldump := findPGWaldumpBin(t)

	c := newCluster(t, "wal_pg_waldump")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Run a workload that generates WAL.
	addr := c.ListenAddr()
	host, port, _ := splitHostPort(addr)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}

	root := repoRoot(t)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	psqlEnv := []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir}

	workload := `
CREATE TABLE wal_test (id serial primary key, val text);
` + buildInsertSQL(100) + `
CHECKPOINT;
`
	res, err := util.RunCommand(util.CommandSpec{
		Name:    psqlBin,
		Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-c", workload},
		Env:     psqlEnv,
		Timeout: 30e9, // 30s
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("workload failed (exit=%d): %s\n%s", res.ExitCode, res.Stderr, res.Stdout)
	}

	// Stop cleanly so WAL is flushed.
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	// Locate the data dir and WAL segments. goopg now names segments in
	// native PostgreSQL format (TLI=1 prefix, e.g. 000000010000000000000001 —
	// M0101-0001), so pg_waldump locates each one directly with
	// -p <walDir> <segment-name> and infers the timeline and start LSN from
	// the filename. No alias rewriting or manual -s is needed (the previous
	// version parsed the full 24-hex name with ParseUint, which overflows
	// uint64 and silently skipped every segment → "no WAL segments found").
	walDir := filepath.Join(c.DataDir(), "pg_wal")
	segs := listWALSegments(t, walDir)
	if len(segs) == 0 {
		t.Fatal("no WAL segments found in pg_wal/")
	}
	t.Logf("found %d WAL segment(s); running pg_waldump", len(segs))

	for i, seg := range segs {
		// Skip an all-zero, never-written segment. With wal_init_zero=on
		// (goopg's default) the writer eagerly preallocates the NEXT
		// segment as a full-size zero-filled file when the current one
		// opens, so a trailing all-zero phantom persists across a clean
		// shutdown, exactly as in real PostgreSQL. pg_waldump reads the
		// segment size from the long page header's xlp_seg_size; on an
		// all-zero segment that field is 0 and it fatally reports
		// `invalid WAL segment size ... (0 bytes)`. That is expected for a
		// preallocated tail (real pg_waldump errors identically) and
		// carries no records, so it must not fail the round-trip.
		if segmentIsAllZero(t, filepath.Join(walDir, seg)) {
			t.Logf("segment %d/%d (%s): all-zero preallocated tail, skipping", i+1, len(segs), seg)
			continue
		}
		res, _ := util.RunCommand(util.CommandSpec{
			Name:    waldump,
			Args:    []string{"--quiet", "-p", walDir, seg},
			Env:     []string{"LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		outStr := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
		if res.ExitCode != 0 {
			// pg_waldump exits non-zero when it encounters the zero-fill
			// at the end of a partially-written segment ("invalid record
			// length ... got 0") or an "end of WAL" marker. These are
			// expected for the last segment in a live or freshly-stopped
			// cluster. Only treat it as a real failure if the error output
			// indicates a structural problem with the record chain itself
			// (wrong magic, bad CRC, xl_prev mismatch, etc.). The
			// "incorrect prev-link" check is the regression guard for the
			// xl_prev seeding fix in internal/wal/writer.go (detectWritePos).
			isEndOfWAL := strings.Contains(outStr, "invalid record length") ||
				strings.Contains(outStr, "end of WAL") ||
				strings.Contains(outStr, "no start WAL location")
			hasStructuralError := strings.Contains(outStr, "incorrect prev-link") ||
				strings.Contains(outStr, "invalid magic number") ||
				strings.Contains(outStr, "incorrect resource manager") ||
				(strings.Contains(outStr, "error:") && !isEndOfWAL)
			if hasStructuralError {
				t.Errorf("pg_waldump segment %s structural error:\n%s", seg, outStr)
			} else {
				t.Logf("segment %d/%d (%s): pg_waldump OK (end-of-WAL: %s)",
					i+1, len(segs), seg, outStr)
			}
		} else {
			suffix := ""
			if outStr != "" {
				suffix = ": " + outStr
			}
			t.Logf("segment %d/%d (%s): pg_waldump OK%s", i+1, len(segs), seg, suffix)
		}
	}
}

// findPGWaldumpBin locates the pg_waldump binary. Skips if not found.
func findPGWaldumpBin(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("pg_waldump"); err == nil {
		return p
	}
	root := repoRoot(t)
	candidate := filepath.Join(root, "postgres", "local_install", "bin", "pg_waldump")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	t.Skip("pg_waldump not found in PATH or postgres/local_install/bin")
	return ""
}

// buildInsertSQL returns a SQL string that inserts n rows into wal_test.
func buildInsertSQL(n int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO wal_test(val) VALUES ")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "('row%d')", i+1)
	}
	b.WriteByte(';')
	return b.String()
}
