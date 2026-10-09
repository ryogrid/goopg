package testport

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// TestE2E_PGReadsThreeLevelCatalogBtree (M-NIGHTLY, AI-20260928-004845-004
// follow-up): once pg_class grows past what one internal root of
// pg_class_relname_nsp_index (2663) can point at (~97 leaves), goopg's catalog
// btree rebuild adds another internal level (sys_catalog_btree_levels.go).
// Before, every CREATE failed with "internal-root overflow inserting downlink
// 97". The index is read by real PostgreSQL too, so this test hands the
// directory to PG 18.3 and has it resolve relations by name through that
// index (RELNAMENSP syscache and an index scan on pg_class), which fails on a
// malformed internal level (high keys, minus-infinity downlinks, levels).
func TestE2E_PGReadsThreeLevelCatalogBtree(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_M0131_E2E") != "" {
		t.Skip("skipping catalog-btree e2e (short mode or GOOPG_SKIP_M0131_E2E set)")
	}
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)
	dir := filepath.Join(t.TempDir(), "goopgdata")

	g, err := cluster.New("btree-levels-goopg", cluster.Options{
		RepoRoot: repo, DataDir: dir,
		StartupWait: 60 * time.Second, ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	if err := g.Init(); err != nil {
		t.Fatalf("goopg init: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = g.Stop(cluster.ShutdownImmediate)
		}
	}()
	if err := g.Start(); err != nil {
		t.Fatalf("goopg start: %v", err)
	}

	// ~97 leaves x ~96 entries of 80 bytes fill one internal root; 9600
	// relations push the index past it.
	const n = 9600
	const batch = 100
	for lo := 0; lo < n; lo += batch {
		var b strings.Builder
		for i := lo; i < lo+batch && i < n; i++ {
			fmt.Fprintf(&b, "CREATE TABLE lvl_%05d (a int);", i)
		}
		if err := runSQLSimple(t, g, b.String()); err != nil {
			logTail, _ := os.ReadFile(g.LogPath())
			t.Fatalf("create tables %d..: %v\n--- goopg log ---\n%s", lo, err, tailLines(string(logTail), 30))
		}
	}
	if err := runSQLSimple(t, g, "INSERT INTO lvl_04321 VALUES (7)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := g.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("goopg stop: %v", err)
	}
	stopped = true

	// The metapage of 2663 must now report a tree above level 1.
	level, err := sysBtreeMetaLevel(filepath.Join(dir, "base"), "2663")
	if err != nil {
		t.Fatalf("read 2663 metapage: %v", err)
	}
	if level < 2 {
		t.Fatalf("pg_class_relname_nsp_index level = %d after %d CREATEs; the test did not reach a third level", level, n)
	}

	pg, err := pgcluster.OpenExisting("btree-levels-pg", pgcluster.Options{
		RepoRoot: repo, DataDir: dir, User: "postgres", StartupWait: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("pgcluster.OpenExisting: %v", err)
	}
	defer func() { _ = pg.Stop() }()
	if err := pg.Start(); err != nil {
		t.Fatalf("postgres -D <goopg dir>: %v", err)
	}
	readyCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := pg.WaitReady(readyCtx, 60*time.Second); err != nil {
		logTail, _ := os.ReadFile(pgLogPathFor(dir))
		t.Fatalf("pg.WaitReady: %v\n--- PG log ---\n%s", err, tailLines(string(logTail), 40))
	}
	// Relation lookup by name goes through RELNAMENSP (index 2663).
	for _, name := range []string{"lvl_00000", "lvl_04321", "lvl_09599"} {
		got := pgQueryColumn(t, pg, "SELECT count(*) FROM "+name)
		want := "0"
		if name == "lvl_04321" {
			want = "1"
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("hosted PG count(*) FROM %s = %v, want %s", name, got, want)
		}
	}
	// And an explicit index scan on pg_class by name.
	got := pgQueryColumn(t, pg, `SET enable_seqscan = off; SET enable_bitmapscan = off;
		SELECT count(*) FROM pg_class WHERE relname IN ('lvl_00001','lvl_05000','lvl_09598') AND relnamespace = 'public'::regnamespace`)
	if len(got) == 0 || got[len(got)-1] != "3" {
		t.Fatalf("hosted PG index lookup on pg_class_relname_nsp_index = %v, want 3", got)
	}
}

// sysBtreeMetaLevel reads btm_level from block 0 of the first base/<db>/<file>
// relation file (the metapage: page header 24 bytes, then btm_magic,
// btm_version, btm_root, btm_level).
func sysBtreeMetaLevel(baseDir, file string) (uint32, error) {
	matches, err := filepath.Glob(filepath.Join(baseDir, "*", file))
	if err != nil {
		return 0, err
	}
	var best uint32
	found := false
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil || len(b) < 24+16 {
			continue
		}
		lvl := binary.LittleEndian.Uint32(b[24+12 : 24+16])
		if !found || lvl > best {
			best, found = lvl, true
		}
	}
	if !found {
		return 0, fmt.Errorf("no %s file under %s", file, baseDir)
	}
	return best, nil
}
