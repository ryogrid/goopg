package testport

// pgwaldump_vacuum_prune_test.go — TestPort_PgWaldumpVacuumPruneRoundtrip
//
// Closes the "still open" item left by the M0119-0005 prune/VACUUM
// canonical-WAL fix (docs/design/0110-0002-pg-waldump-tap-port.md, "2026-07-03:
// prune/VACUUM canonical-WAL fix (LANDED)"): that loop verified the new
// XLOG_HEAP2_PRUNE_VACUUM_SCAN record's byte layout and emission via unit
// tests only (internal/catalog, internal/vacuum, internal/executor), never a
// live `pg_waldump --rmgr=Heap2` round-trip against a real VACUUM-pruned
// page. This test drives that end-to-end: DELETE rows, run VACUUM, stop
// the cluster, and assert the upstream pg_waldump binary can walk the
// resulting WAL and decode the record — no structural error, and the
// `PRUNE_VACUUM_SCAN` identifier / this table's relation locator both appear
// in its output.
//
// Unconditionally skipped since 2026-07-15 (doc 04 §5.1-5.3 canonical-family
// removal) — see the t.Skip below and .ralph/deferral_ledger.md.
//
// This does NOT attempt a standby replay (that would need a second goopg
// instance consuming WAL as a physical standby, out of scope here); it
// confirms only that a vanilla PG18 tool reads the bytes goopg wrote,
// mirroring the same-scope claim TestPort_WALPgWaldumpCompat (W-001) already
// makes for ordinary insert/update WAL.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// segmentIsAllZero reports whether the WAL segment file at path contains
// only zero bytes — the signature of a preallocated, never-written segment
// (see the skip rationale in the round-trip loop). Read errors are treated
// as "not all-zero" so the caller still feeds the segment to pg_waldump and
// surfaces any real problem.
func segmentIsAllZero(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<16)
	for {
		n, rerr := f.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return false
			}
		}
		if errors.Is(rerr, io.EOF) {
			return true // scanned the whole file, every byte was zero
		}
		if rerr != nil {
			return false // unexpected read error: let pg_waldump surface it
		}
	}
}

func TestPort_PgWaldumpVacuumPruneRoundtrip(t *testing.T) {
	t.Skip("PG-tool WAL compat removed 2026-07-15 (canonical/knob/skip-tag removed; native->PG (rmid,info) dispatch); intentional, not a regression - resumes after native->PG content rewrite. See docs/design/wal-native-pg-format/04 + .ralph/deferral_ledger.md")
	waldump := findPGWaldumpBin(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}

	c := newCluster(t, "wal_vacuum_prune")
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

	// Half the rows die and commit before VACUUM scans the table, so
	// VacuumWithOptions' `reclaimed > 0` arm (internal/vacuum/vacuum.go) fires
	// and emits the canonical PRUNE_VACUUM_SCAN record.
	runSQL("CREATE TABLE vacuum_prune_t (id serial primary key, val text)")
	runSQL("INSERT INTO vacuum_prune_t (val) SELECT 'x' || g FROM generate_series(1,200) g")
	runSQL("DELETE FROM vacuum_prune_t WHERE id <= 100")
	runSQL("VACUUM vacuum_prune_t")

	// Resolve the relation locator TBLSPC/DB/RELNODE exactly as
	// TestPort_PgWaldump002SaveFullpage does (pgwaldump_savefullpage_test.go):
	// goopg has no pg_database.dattablespace column but always writes
	// pg_default (1663), and the DB component is only observable on disk
	// (catalog.go's pgDatabase.VirtualRows carries a legacy display OID).
	const pgDefaultTablespace = 1663
	relNode := runSQL("SELECT pg_relation_filenode('vacuum_prune_t'::regclass)")
	if relNode == "" {
		t.Fatalf("could not resolve relnode for vacuum_prune_t")
	}
	dbDirMatches, globErr := filepath.Glob(filepath.Join(c.DataDir(), "base", "*", relNode))
	if globErr != nil || len(dbDirMatches) != 1 {
		t.Fatalf("could not resolve on-disk database dir for relnode %s: matches=%v err=%v",
			relNode, dbDirMatches, globErr)
	}
	dbOID := filepath.Base(filepath.Dir(dbDirMatches[0]))
	blockRef := fmt.Sprintf("rel %d/%s/%s", pgDefaultTablespace, dbOID, relNode)
	t.Logf("vacuum_prune_t relation locator = %d/%s/%s", pgDefaultTablespace, dbOID, relNode)

	// Stop cleanly so all WAL (including the canonical prune record) is
	// flushed to disk.
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	walDir := filepath.Join(c.DataDir(), "pg_wal")
	segs := listWALSegments(t, walDir)
	if len(segs) == 0 {
		t.Fatal("no WAL segments found in pg_wal/")
	}

	sawPruneRecordForRelation := false
	for i, seg := range segs {
		// Skip an all-zero, never-written segment. With wal_init_zero=on
		// (goopg's default, mirroring upstream) the writer eagerly
		// preallocates the NEXT segment as a full-size zero-filled file
		// the moment the current one opens — so after a tiny workload the
		// trailing 000000010000000000000002 is a legitimate all-zero
		// phantom that persists across a clean shutdown, exactly as it
		// does in real PostgreSQL (XLogFileInit zero-fills; the long page
		// header is only written when the segment first becomes the insert
		// target). pg_waldump reads the segment size from that long page
		// header's xlp_seg_size, so pointed at an all-zero segment it
		// fatally reports `invalid WAL segment size ... (0 bytes)`. That
		// is expected for a preallocated tail (real pg_waldump errors the
		// same way) and carries no records, so it is not a structural
		// decode problem and must not fail the round-trip.
		if segmentIsAllZero(t, filepath.Join(walDir, seg)) {
			t.Logf("segment %d/%d (%s): all-zero preallocated tail, skipping", i+1, len(segs), seg)
			continue
		}
		res, _ := util.RunCommand(util.CommandSpec{
			Name:    waldump,
			Args:    []string{"--rmgr=Heap2", "-p", walDir, seg},
			Env:     []string{"LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		outStr := res.Stdout
		errStr := strings.TrimSpace(res.Stderr)
		combined := outStr + "\n" + errStr
		if res.ExitCode != 0 {
			// Mirrors TestPort_WALPgWaldumpCompat: end-of-WAL on the trailing
			// segment is expected; only a structural decode problem is fatal.
			isEndOfWAL := strings.Contains(combined, "invalid record length") ||
				strings.Contains(combined, "end of WAL") ||
				strings.Contains(combined, "no start WAL location")
			hasStructuralError := strings.Contains(combined, "incorrect prev-link") ||
				strings.Contains(combined, "invalid magic number") ||
				strings.Contains(combined, "incorrect resource manager") ||
				(strings.Contains(combined, "error:") && !isEndOfWAL)
			if hasStructuralError {
				t.Errorf("pg_waldump --rmgr=Heap2 segment %s structural error:\n%s", seg, combined)
			} else {
				t.Logf("segment %d/%d (%s): pg_waldump OK (end-of-WAL: %s)", i+1, len(segs), seg, errStr)
			}
		}
		if strings.Contains(outStr, "PRUNE_VACUUM_SCAN") && strings.Contains(outStr, blockRef) {
			sawPruneRecordForRelation = true
		}
		t.Logf("segment %d/%d (%s):\n%s", i+1, len(segs), seg, outStr)
	}

	if !sawPruneRecordForRelation {
		t.Errorf("pg_waldump --rmgr=Heap2 found no PRUNE_VACUUM_SCAN record for %s", blockRef)
	}
}
