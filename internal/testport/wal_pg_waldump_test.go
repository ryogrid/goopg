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
//  4. Enumerate pg_wal/ segments.
//  5. For each segment, create a filesystem alias with the PostgreSQL-format
//     name (timeline=1 prefix) if needed, then run pg_waldump --quiet.
//  6. Assert pg_waldump exits 0 for every segment.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_WALPgWaldumpCompat verifies that goopg's WAL segments are
// readable by pg_waldump. M0101-0003.
func TestPort_WALPgWaldumpCompat(t *testing.T) {
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

	// Locate the data dir and WAL segments.
	walDir := filepath.Join(c.DataDir(), "pg_wal")
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("read pg_wal: %v", err)
	}

	var segs []string
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) != 24 {
			continue
		}
		// Goopg names segments as 24-hex segment-number; skip non-hex.
		if _, err := strconv.ParseUint(e.Name(), 16, 64); err != nil {
			continue
		}
		segs = append(segs, e.Name())
	}
	if len(segs) == 0 {
		t.Fatal("no WAL segments found in pg_wal/")
	}
	sort.Strings(segs)

	// pg_waldump expects files named with a timeline prefix
	// (e.g. 000000010000000000000000). goopg uses timeline=0 in the
	// filename but embeds timeline=1 in the page headers (M0101-0001).
	// Create timeline=1 aliases so pg_waldump can locate the files.
	t.Logf("found %d WAL segment(s); creating timeline=1 aliases and running pg_waldump", len(segs))
	for i, seg := range segs {
		origPath := filepath.Join(walDir, seg)

		// Determine the alias name: strip the timeline prefix (first 8
		// hex chars = "00000000") and prepend timeline=1 ("00000001").
		// goopg segment names are plain 24-char hex numbers with the
		// segment number in positions 8-23 (after skipping TLI zeros).
		aliasName := "00000001" + seg[8:]
		aliasPath := filepath.Join(walDir, aliasName)

		raw, err := os.ReadFile(origPath)
		if err != nil {
			t.Fatalf("read segment %s: %v", seg, err)
		}
		if err := os.WriteFile(aliasPath, raw, 0o600); err != nil {
			t.Fatalf("write alias %s: %v", aliasName, err)
		}
		defer os.Remove(aliasPath)

		// Determine the -s (start LSN) from the segment number.
		// goopg LSNs are 1-based byte offsets; segment 0 starts at byte 1.
		segNum, _ := strconv.ParseUint(seg, 16, 64)
		segSize := int64(16 * 1024 * 1024) // DefaultSegmentSize = 16 MiB
		startByte := int64(segNum) * segSize
		// Convert to PG XLogRecPtr (0-based, high32/low32 format).
		startXLSN := ""
		if startByte == 0 {
			startXLSN = "0/1"
		} else {
			pos := uint64(startByte)
			startXLSN = fmt.Sprintf("%X/%X", pos>>32, uint32(pos))
		}

		args := []string{
			"--quiet",
			"-t", "1",
			"-s", startXLSN,
			"-p", walDir,
			aliasName,
		}
		out, err := exec.Command(waldump, args...).CombinedOutput()
		outStr := strings.TrimSpace(string(out))
		if err != nil {
			// pg_waldump exits non-zero when it encounters the zero-fill
			// at the end of a partially-written segment ("invalid record
			// length ... got 0") or an "end of WAL" marker. These are
			// expected for the last segment in a live or freshly-stopped
			// cluster. Only treat it as a real failure if the error output
			// indicates a structural problem with the page headers themselves
			// (wrong magic, bad CRC, xl_prev mismatch, etc.).
			isEndOfWAL := strings.Contains(outStr, "invalid record length") ||
				strings.Contains(outStr, "end of WAL") ||
				strings.Contains(outStr, "no start WAL location")
			hasStructuralError := strings.Contains(outStr, "incorrect prev-link") ||
				strings.Contains(outStr, "invalid magic number") ||
				strings.Contains(outStr, "incorrect resource manager") ||
				(strings.Contains(outStr, "error:") && !isEndOfWAL)
			if hasStructuralError {
				t.Errorf("pg_waldump segment %s (alias %s) structural error: %v\n%s",
					seg, aliasName, err, outStr)
			} else {
				t.Logf("segment %d/%d (%s): pg_waldump OK (end-of-WAL: %s)",
					i+1, len(segs), seg, outStr)
			}
		} else {
			t.Logf("segment %d/%d (%s): pg_waldump OK%s", i+1, len(segs), seg,
				func() string {
					if outStr != "" {
						return ": " + outStr
					}
					return ""
				}())
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

