package testport

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery043NoContrecordSwitch ports postgres/src/test/recovery/t/043_no_contrecord_switch.pl
// to the M0149 contract: the primary's last record ends exactly at a WAL page
// boundary and the next page is zeroed in the archived segment, so two
// standbys restoring from the archive stop with "invalid magic number 0000"
// at that page. One is promoted; the other, switched to stream from it, must
// follow the new timeline from that boundary — no continuation record is
// expected there — and replay what the promoted standby writes. It runs in
// three pairings: all-goopg, a PG 18.3 primary with goopg standbys, and a
// goopg primary with PG 18.3 standbys (M0149-0028).
func TestPort_Recovery043NoContrecordSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	goopgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, false)
	}
	pgFromBackup := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, false)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T, name string) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopgFromBackup},
		{"pg18-primary-goopg-standbys", pg, goopgFromBackup},
		{"goopg-primary-pg18-standbys", goopg, pgFromBackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runNoContrecordSwitch(t, tc.primary(t, "primary"), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runNoContrecordSwitch(t *testing.T, primary *tapnode.Node, fromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	getInt := func(n *tapnode.Node, sql string) int64 {
		t.Helper()
		v, err := strconv.ParseInt(n.SafePSQL("postgres", sql), 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	// emit_wal: a WAL record of arbitrary size; returns its end LSN.
	emitWAL := func(n *tapnode.Node, size int64) int64 {
		return getInt(n, fmt.Sprintf("SELECT pg_logical_emit_message(true, '', repeat('a', %d)) - '0/0'", size))
	}
	insertLSN := func(n *tapnode.Node) int64 {
		return getInt(n, "SELECT pg_current_wal_insert_lsn() - '0/0'")
	}
	// wait_for_log: poll the log from offset until pattern matches.
	waitForLog := func(n *tapnode.Node, pattern string, offset int64) {
		t.Helper()
		re := regexp.MustCompile(pattern)
		deadline := time.Now().Add(180 * time.Second)
		for time.Now().Before(deadline) {
			if re.MatchString(n.LogFrom(offset)) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in %s's log; tail:\n%s", pattern, n.Name(), fileTail(n.LogPath(), 30))
	}

	primary.EnableArchiving()
	for _, l := range []string{
		"wal_level = replica", "max_wal_senders = 10",
		"autovacuum = off", "checkpoint_timeout = '30min'", "wal_keep_size = 1GB",
	} {
		primary.AppendConf(l)
	}
	primary.Start()

	bkp := primary.Backup("backup")

	primary.SafePSQL("postgres", "CREATE TABLE t AS SELECT 0")

	walSegmentSize := getInt(primary, "SELECT setting FROM pg_settings WHERE name = 'wal_segment_size'")
	walBlockSize := getInt(primary, "SELECT setting FROM pg_settings WHERE name = 'wal_block_size'")
	tli := getInt(primary, "SELECT timeline_id FROM pg_control_checkpoint()")

	// Get close to the end of the current WAL page, enough to fit the
	// beginning of a record that spans on two pages, generating a
	// continuation record.
	emitWAL(primary, 0)

	// advance_wal_out_of_record_splitting_zone
	pageThreshold := walBlockSize / 4
	endLSN := insertLSN(primary)
	for endLSN%walBlockSize >= walBlockSize-pageThreshold {
		emitWAL(primary, pageThreshold)
		endLSN = insertLSN(primary)
	}

	// Do some math to find the record size that will overflow the page, and
	// write it.
	overflowSize := walBlockSize - (endLSN % walBlockSize)
	endLSN = emitWAL(primary, overflowSize)
	primary.Stop(tapnode.StopImmediate)

	// Find the beginning of the page with the continuation record and fill
	// the entire page with zero bytes to simulate broken replication.
	startPage := endLSN &^ (walBlockSize - 1)
	segment, offset := startPage/walSegmentSize, startPage%walSegmentSize
	segmentName := fmt.Sprintf("%08X%08X%08X", tli, 0, segment)
	walFile := filepath.Join(primary.DataDir(), "pg_wal", segmentName)
	f, err := os.OpenFile(walFile, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("could not open WAL segment %s: %v", walFile, err)
	}
	if _, err := f.WriteAt(make([]byte, walBlockSize), offset); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Copy the file we just "hacked" to the archives.
	data, err := os.ReadFile(walFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary.ArchiveDir(), segmentName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Start two standbys nodes from the backup and set up streaming between
	// them.
	standby1 := fromBackup("standby1", bkp, primary)
	standby1.EnableRestoring(primary, true)
	standby2 := fromBackup("standby2", bkp, primary)
	standby2.EnableRestoring(primary, true)

	logSize1 := standby1.LogSize()
	logSize2 := standby2.LogSize()

	standby1.Start()
	standby2.Start()

	pattern := fmt.Sprintf(`invalid magic number 0000 .* segment %s.* offset %d`, segmentName, offset)
	waitForLog(standby1, pattern, logSize1)
	waitForLog(standby2, pattern, logSize2)

	// Now promote and checkpoint standby1
	standby1.Promote()
	standby1.SafePSQL("postgres", "SELECT pg_switch_wal()")
	standby1.SafePSQL("postgres", "INSERT INTO t SELECT * FROM generate_series(1, 1000)")

	// Now enable streaming between the two standbys.
	standby2.EnableStreaming(standby1)
	standby2.Reload()

	waitForReplayOf(t, standby1, standby2)

	result := standby2.SafePSQL("postgres", "SELECT count(*) FROM t")
	if strings.TrimSpace(result) != "1001" {
		t.Errorf("not ok: check streamed content on standby2: got %q, want %q", result, "1001")
	}
}
