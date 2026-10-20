package testport

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPgWaldumpHeapRecordsCarryVMCleared reads goopg's own WAL with PG 18.3's
// pg_waldump after a change to an all-visible page. Each heap record must
// carry PG's *_ALL_VISIBLE_CLEARED flag bit (heap_insert, heap_delete and
// heap_update set it when PageIsAllVisible held), which PG's redo uses to
// clear the page's visibility-map bits. goopg logged only its own native
// rmgr-128 clear record, which a PG standby cannot replay.
func TestPgWaldumpHeapRecordsCarryVMCleared(t *testing.T) {
	waldump := clientToolBin(t, "pg_waldump")
	psqlBin := clientToolBin(t, "psql")
	if waldump == "" || psqlBin == "" {
		t.Skip("pg_waldump or psql not available")
	}
	c := newCluster(t, "wal_vm_cleared")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, _ := splitHostPort(c.ListenAddr())
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")
	runSQL := func(sql string) string {
		t.Helper()
		res, err := util.RunCommand(util.CommandSpec{
			Name:    psqlBin,
			Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-tA", "-c", sql},
			Env:     []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("psql failed (exit=%d): %s\n%s\nSQL: %s", res.ExitCode, res.Stderr, res.Stdout, sql)
		}
		return strings.TrimSpace(res.Stdout)
	}
	// One statement per message: VACUUM cannot run inside the transaction a
	// multi-statement simple query becomes. Each VACUUM leaves the single
	// page all-visible, and the change after it clears that.
	for _, sql := range []string{
		"CREATE TABLE vmc (a int)",
		"INSERT INTO vmc SELECT generate_series(1, 10)",
		"VACUUM vmc",
		"INSERT INTO vmc VALUES (99)",
		"VACUUM vmc",
		"DELETE FROM vmc WHERE a = 2",
		"VACUUM vmc",
		"UPDATE vmc SET a = 30 WHERE a = 3",
	} {
		runSQL(sql)
	}
	relnode := runSQL("SELECT pg_relation_filenode('vmc')")
	path := runSQL("SELECT pg_relation_filepath('vmc')")
	dbOID := filepath.Base(filepath.Dir(path))
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	walDir := filepath.Join(c.DataDir(), "pg_wal")
	var nonZero []string
	for _, s := range listWALSegments(t, walDir) {
		if !segmentIsAllZero(t, filepath.Join(walDir, s)) {
			nonZero = append(nonZero, s)
		}
	}
	if len(nonZero) == 0 {
		t.Fatal("no non-zero WAL segments")
	}
	startLSN := formatLSN(segmentStartLSNUint(nonZero[0]))
	endLSN := endOfWALLSN(t, waldump, libDir, c.DataDir(), startLSN)
	lines := waldumpLines(t, waldump, libDir, c.DataDir(), startLSN, endLSN,
		"--relation", fmt.Sprintf("1663/%s/%s", dbOID, relnode), "--rmgr", "Heap")

	// heapdesc.c: INSERT "off: N, flags: 0x%02X"; DELETE "..., flags: 0x%02X";
	// HOT_UPDATE / UPDATE "..., flags: 0x%02X, new_xmax: ...".
	flagRE := regexp.MustCompile(`desc: (INSERT|DELETE|HOT_UPDATE|UPDATE)\b.*?flags: 0x([0-9A-F]{2})`)
	type rec struct {
		op    string
		flags uint64
	}
	var got []rec
	for _, ln := range lines {
		if m := flagRE.FindStringSubmatch(ln); m != nil {
			var f uint64
			fmt.Sscanf(m[2], "%X", &f)
			got = append(got, rec{m[1], f})
		}
	}
	// The ten seed rows come first and clear nothing; the three changes
	// after each VACUUM each clear the page: bit 0x01 is
	// XLH_INSERT_ALL_VISIBLE_CLEARED, XLH_DELETE_ALL_VISIBLE_CLEARED and
	// XLH_UPDATE_OLD_ALL_VISIBLE_CLEARED alike.
	want := []string{"INSERT", "DELETE", "HOT_UPDATE"}
	if len(got) < len(want) {
		t.Fatalf("heap records for vmc: %v, want the last three to be %v\n%s", got, want, strings.Join(lines, "\n"))
	}
	tail := got[len(got)-len(want):]
	for i, r := range tail {
		if r.op != want[i] || r.flags&0x01 == 0 {
			t.Errorf("record %d after a VACUUM: %s flags 0x%02X, want %s with the ALL_VISIBLE_CLEARED bit\n%s",
				i, r.op, r.flags, want[i], strings.Join(lines, "\n"))
		}
	}
	for _, r := range got[:len(got)-len(want)] {
		if r.flags&0x01 != 0 {
			t.Errorf("seed %s carries ALL_VISIBLE_CLEARED; the page was not all-visible yet", r.op)
		}
	}
}
