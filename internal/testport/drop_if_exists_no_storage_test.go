package testport

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// M-NIGHTLY 0-byte relfilenode-file class pin (filed 2026-10-31 from
// AI-20261031-002109-005): pgbench -i opens with
//
//	drop table if exists pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers cascade
//
// on a database where those tables do not exist. The diagnosis recorded
// that statement executing through the TRUNCATE executor and leaving four
// 0-byte files whose relfilenodes the later CREATEs then reused. At HEAD
// the DROP arm is clean (NOTICE + skip, no OID consumed); the 0-byte files
// came from the smgr truncate path opening with O_CREATE and from
// parent-level storage work in the truncate executor. This test pins the
// server-level contract on a fresh cluster:
//
//   - the pgbench-style DROP IF EXISTS creates NO relation file (no
//     user-range 0-byte stubs),
//   - it consumes no OIDs: the first CREATE afterwards lands on the
//     next-in-sequence relfilenode (max pg_class oid + 1).
//
// NOTICE wording ("table ... does not exist, skipping") is asserted at the
// executor level (internal/executor/drop_truncate_nostorage_test.go); the
// Go pg driver used here does not surface NOTICE frames.
func TestDropTableIfExistsMissingNoStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("drop-if-exists-no-storage", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dir,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	baseDir := filepath.Join(dir, "base", "1")
	fileSet := func() map[string]int64 {
		out := map[string]int64{}
		ents, err := os.ReadDir(baseDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", baseDir, err)
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			st, err := e.Info()
			if err != nil {
				t.Fatalf("stat %s: %v", e.Name(), err)
			}
			out[e.Name()] = st.Size()
		}
		return out
	}
	// Baseline: a simple CREATE TABLE takes the next free OID, so a second
	// simple CREATE right after it gets +1. If the DROP burned oids, the
	// post-drop CREATE would land further along.
	if err := runSQLSimple(t, c, "CREATE TABLE ref_before(b int)"); err != nil {
		t.Fatalf("CREATE ref_before: %v", err)
	}
	refRFN := queryScalar(t, c, "SELECT relfilenode::text FROM pg_class WHERE relname='ref_before'")
	maxOID := queryScalar(t, c, "SELECT coalesce(max(oid),0)::text FROM pg_class WHERE oid >= 16384")
	before := fileSet()

	if err := runSQLSimple(t, c,
		"drop table if exists pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers cascade"); err != nil {
		t.Fatalf("pgbench-style DROP IF EXISTS: %v", err)
	}

	// No new files: the drop must not materialise 0-byte relfilenode stubs.
	after := fileSet()
	if len(after) != len(before) {
		t.Fatalf("file inventory changed after DROP: before=%d after=%d (before=%v after=%v)", len(before), len(after), before, after)
	}
	for name, size := range after {
		if before[name] != size {
			t.Fatalf("file %s changed size %d -> %d during DROP of missing relations", name, before[name], size)
		}
		if oid, err := strconv.Atoi(name); err == nil && oid >= 16384 {
			t.Fatalf("DROP of missing relations created user-range file %s (size %d)", name, size)
		}
	}

	// No OID consumption: the next CREATE gets the untouched next oid.
	if got := queryScalar(t, c, "SELECT coalesce(max(oid),0)::text FROM pg_class WHERE oid >= 16384"); got != maxOID {
		t.Fatalf("max pg_class oid moved %s -> %s; DROP IF EXISTS of missing relations must not consume OIDs", maxOID, got)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE probe_after(b int)"); err != nil {
		t.Fatalf("CREATE probe_after: %v", err)
	}
	wantNext := func() string {
		n, err := strconv.Atoi(refRFN)
		if err != nil {
			t.Fatalf("parse ref_before relfilenode %q: %v", refRFN, err)
		}
		return strconv.Itoa(n + 1)
	}
	gotRFN := queryScalar(t, c, "SELECT relfilenode::text FROM pg_class WHERE relname='probe_after'")
	if gotRFN != wantNext() {
		t.Fatalf("probe_after relfilenode = %s, want %s (ref_before %s + 1; the drop burned no oids)", gotRFN, wantNext(), refRFN)
	}
}
