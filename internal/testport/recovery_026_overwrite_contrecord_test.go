package testport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery026OverwriteContrecord ports
// postgres/src/test/recovery/t/026_overwrite_contrecord.pl (M0149-0017): a
// record that crosses into a WAL segment which is then lost. Crash recovery
// on the primary writes XLOG_OVERWRITE_CONTRECORD where the missing
// continuation began; a standby made from a cold copy taken before that
// recovery must replay past it, log "successfully skipped missing contrecord
// at", and promote. Run all-goopg and with PG 18.3 as the primary or the
// standby.
func TestPort_Recovery026OverwriteContrecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupGoopg(t, repo, name, root, bkp, true)
	}
	pgStandby := func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node {
		return tapnode.InitFromBackupPG(t, repo, name, root, bkp, true)
	}
	cases := []struct {
		name    string
		primary func(t *testing.T) *tapnode.Node
		standby func(t *testing.T, name string, root *tapnode.Node, bkp string) *tapnode.Node
	}{
		{"all-goopg", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, goopgStandby},
		{"pg18-primary-goopg-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewPG(t, repo, "primary") }, goopgStandby},
		{"goopg-primary-pg18-standby", func(t *testing.T) *tapnode.Node { return tapnode.NewGoopg(t, repo, "primary") }, pgStandby},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runOverwriteContrecord(t, tc.primary(t), func(name, bkp string, root *tapnode.Node) *tapnode.Node {
				return tc.standby(t, name, root, bkp)
			})
		})
	}
}

func runOverwriteContrecord(t *testing.T, node *tapnode.Node, standbyFromBackup func(name, bkp string, root *tapnode.Node) *tapnode.Node) {
	// We need these settings for stability of WAL behavior.
	node.AppendConf("autovacuum = off")
	node.AppendConf("wal_keep_size = 1GB")
	node.Start()

	node.SafePSQL("postgres", "create table filler (a int, b text)")

	// Now consume all remaining room in the current WAL segment, leaving
	// space enough only for the start of a largish record.
	node.SafePSQL("postgres", `
DO $$
DECLARE
    wal_segsize int := setting::int FROM pg_settings WHERE name = 'wal_segment_size';
    remain int;
    iters  int := 0;
BEGIN
    LOOP
        INSERT into filler
        select g, repeat(encode(sha256(g::text::bytea), 'hex'), (random() * 15 + 1)::int)
        from generate_series(1, 10) g;

        remain := wal_segsize - (pg_current_wal_insert_lsn() - '0/0') % wal_segsize;
        IF remain < 2 * setting::int from pg_settings where name = 'block_size' THEN
            RAISE log 'exiting after % iterations, % bytes to end of WAL segment', iters, remain;
            EXIT;
        END IF;
        iters := iters + 1;
    END LOOP;
END
$$;`)

	initfile := node.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_insert_lsn())")
	node.SafePSQL("postgres", "SELECT pg_logical_emit_message(true, 'test 026', repeat('xyzxz', 123456))")
	endfile := node.SafePSQL("postgres", "SELECT pg_walfile_name(pg_current_wal_insert_lsn())")
	if initfile == endfile {
		t.Errorf("not ok: %s differs from %s", initfile, endfile)
	}

	// Now stop abruptly, to avoid a stop checkpoint. We can remove the tail
	// file afterwards, and on startup the large message should be
	// overwritten with new contents.
	node.Stop(tapnode.StopImmediate)
	if err := os.Remove(filepath.Join(node.DataDir(), "pg_wal", endfile)); err != nil {
		t.Fatalf("could not unlink pg_wal/%s: %v", endfile, err)
	}

	// OK, create a standby at this spot.
	bkp := node.BackupFsCold("backup")
	standby := standbyFromBackup("standby", bkp, node)

	standby.Start()
	node.Start()

	node.SafePSQL("postgres", "create table foo (a text); insert into foo values ('hello')")
	node.SafePSQL("postgres", "SELECT pg_logical_emit_message(true, 'test 026', 'AABBCC')")

	untilLSN := node.SafePSQL("postgres", "SELECT pg_current_wal_lsn()")
	if !standby.PollQueryUntil("postgres", "SELECT '"+untilLSN+"'::pg_lsn <= pg_last_wal_replay_lsn()", "t") {
		t.Fatalf("Timed out while waiting for standby to catch up\n--- standby log tail ---\n%s\n--- primary log tail ---\n%s",
			fileTail(standby.LogPath(), 40), fileTail(node.LogPath(), 40))
	}

	if got, stderr, _ := standby.PSQLExit("postgres", "select * from foo"); got != "hello" {
		t.Errorf("not ok: standby replays past overwritten contrecord: got %q (%s)\n--- standby log tail ---\n%s",
			got, strings.TrimSpace(stderr), fileTail(standby.LogPath(), 30))
	}

	// Verify message appears in standby's log
	log, err := os.ReadFile(standby.LogPath())
	if err != nil {
		t.Fatalf("read standby log: %v", err)
	}
	if !strings.Contains(string(log), "successfully skipped missing contrecord at") {
		t.Errorf("not ok: found log line in standby")
	}

	// Verify promotion is successful
	standby.Promote()

	node.Stop(tapnode.StopFast)
	standby.Stop(tapnode.StopFast)
}

// fileTail is the last n lines of the file at path, for failure messages.
func fileTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
