package testport

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery016MinConsistency ports
// postgres/src/test/recovery/t/016_min_consistency.pl (M0149-0011): after a
// standby replays updates under tiny shared_buffers and is stopped cleanly,
// no page of the table on disk may carry an LSN past the standby's
// minRecoveryPoint in pg_control (UpdateMinRecoveryPoint on buffer flush
// during recovery). Run all-goopg and with PG 18.3 on either side (M0149 C3).
func TestPort_Recovery016MinConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	t.Run("all-goopg", func(t *testing.T) {
		primary := tapnode.NewGoopg(t, repo, "primary")
		runMinConsistency(t, repo, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupGoopg(t, repo, "standby", primary, bkp, true)
		})
	})
	t.Run("pg18-primary-goopg-standby", func(t *testing.T) {
		primary := tapnode.NewPG(t, repo, "primary")
		runMinConsistency(t, repo, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupGoopg(t, repo, "standby", primary, bkp, true)
		})
	})
	t.Run("goopg-primary-pg18-standby", func(t *testing.T) {
		primary := tapnode.NewGoopg(t, repo, "primary")
		runMinConsistency(t, repo, primary, func(bkp string) *tapnode.Node {
			return tapnode.InitFromBackupPG(t, repo, "standby", primary, bkp, true)
		})
	})
}

// findLargestLSN is the .pl's find_largest_lsn: the largest pd_lsn
// (xlogid, xrecoff — the first 8 bytes of each page) in a relation file.
func findLargestLSN(t *testing.T, blocksize int, filename string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("failed to open %s: %v", filename, err)
	}
	if len(data)%blocksize != 0 {
		t.Fatalf("read only %d of %d bytes from %s", len(data)%blocksize, blocksize, filename)
	}
	var maxLSN uint64
	for off := 0; off < len(data); off += blocksize {
		hi := binary.LittleEndian.Uint32(data[off:])
		lo := binary.LittleEndian.Uint32(data[off+4:])
		if v := uint64(hi)<<32 | uint64(lo); v > maxLSN {
			maxLSN = v
		}
	}
	return maxLSN
}

func parsePGLSN016(t *testing.T, s string) uint64 {
	t.Helper()
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		t.Fatalf("bad LSN %q", s)
	}
	hi, err1 := strconv.ParseUint(parts[0], 16, 32)
	lo, err2 := strconv.ParseUint(parts[1], 16, 32)
	if err1 != nil || err2 != nil {
		t.Fatalf("bad LSN %q", s)
	}
	return hi<<32 | lo
}

func runMinConsistency(t *testing.T, repo string, primary *tapnode.Node, standbyFromBackup func(bkp string) *tapnode.Node) {
	// Initialize primary node
	primary.AppendConf("shared_buffers = 128kB")
	primary.AppendConf("autovacuum = off")
	primary.Start()

	// setup/start a standby
	bkp := primary.Backup("bkp")
	standby := standbyFromBackup(bkp)
	standby.Start()

	// Create base table whose data consistency is checked.
	primary.SafePSQL("postgres", `
CREATE TABLE test1 (a int) WITH (fillfactor = 10);
INSERT INTO test1 SELECT generate_series(1, 10000);`)

	// Take a checkpoint and enforce post-checkpoint full page writes
	// which makes the next update of test1 to be a full page write.
	primary.SafePSQL("postgres", "CHECKPOINT;")

	// Update all rows of test1 to force full page writes.
	primary.SafePSQL("postgres", "UPDATE test1 SET a = a + 1;")

	// Wait for last record to have been replayed on the standby.
	waitForCatchup(t, primary, standby)

	// Fill in the standby's shared buffers with the data filled in
	// previously.
	standby.SafePSQL("postgres", "SELECT count(*) FROM test1;")

	// Update all rows of test1 again, causing evictions on the standby
	// and pages to be flushed.
	primary.SafePSQL("postgres", "UPDATE test1 SET a = a + 1;")

	// Now record the last page of the table and its relation file.
	blocksize, err := strconv.Atoi(primary.SafePSQL("postgres", "SELECT setting::int FROM pg_settings WHERE name = 'block_size';"))
	if err != nil {
		t.Fatalf("block_size: %v", err)
	}
	_ = primary.SafePSQL("postgres", fmt.Sprintf("SELECT pg_relation_size('test1')::int / %d - 1;", blocksize))
	relfilenode := primary.SafePSQL("postgres", "SELECT pg_relation_filepath('test1'::regclass);")

	// Wait for last record to have been replayed on the standby.
	waitForCatchup(t, primary, standby)

	// Issue a restart point on the standby now, which makes the checkpointer
	// update minRecoveryPoint.
	standby.SafePSQL("postgres", "CHECKPOINT;")

	// Now shut down the primary violently so as the standby does not
	// receive the shutdown checkpoint, making sure that the startup
	// process does not flush any pages on its side.  The standby is
	// cleanly stopped, which makes the checkpointer update minRecoveryPoint
	// with the restart point created at shutdown.
	primary.Stop(tapnode.StopImmediate)
	standby.Stop(tapnode.StopFast)

	// Check the data consistency of the instance while offline.  This is
	// done by directly scanning the on-disk relation blocks and what
	// pg_controldata lets know.
	offlineMaxLSN := findLargestLSN(t, blocksize, filepath.Join(standby.DataDir(), relfilenode))

	// Fetch minRecoveryPoint from the control file itself
	out, err := exec.Command(filepath.Join(tapnode.BinDir(repo), "pg_controldata"), standby.DataDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("pg_controldata: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^Minimum recovery ending location:\s*(.*)$`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("No minRecoveryPoint in control file found\n%s", out)
	}
	offlineRecoveryLSN := parsePGLSN016(t, m[1])
	t.Logf("minRecoveryPoint %s, largest page LSN %X/%X", m[1], uint32(offlineMaxLSN>>32), uint32(offlineMaxLSN))

	// minRecoveryPoint should never be older than the maximum LSN for all
	// the pages on disk. (The .pl compares the %X/%X strings with Perl's
	// `ge`; the intent is LSN order, compared numerically here.)
	if offlineRecoveryLSN < offlineMaxLSN {
		t.Errorf("not ok: Check offline that table data is consistent with minRecoveryPoint: minRecoveryPoint %s < max page LSN %X/%X",
			m[1], uint32(offlineMaxLSN>>32), uint32(offlineMaxLSN))
	}
}
