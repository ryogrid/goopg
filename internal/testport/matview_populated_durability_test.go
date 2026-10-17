package testport

// 02e item A: a materialized view's populated state (pg_class.relispopulated)
// now survives a restart. B5 Slice C dropped the goopg-private record that
// carried it, so before this fix a WITH NO DATA matview reloaded as populated.
// The state is now persisted in pg_class.relispopulated (offset 129) and read
// back by loadUserTablesFromHeap; loadViewsFromHeap no longer clobbers it.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_MatviewPopulatedSurvivesRestart creates a populated matview and an
// unpopulated (WITH NO DATA) matview, restarts, and asserts relispopulated is
// preserved for both.
func TestPort_MatviewPopulatedSurvivesRestart(t *testing.T) {
	c, err := cluster.New("matview-populated-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE mvbase (id int, val int)"); err != nil {
		t.Fatalf("CREATE TABLE mvbase: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO mvbase VALUES (1,10),(2,20)"); err != nil {
		t.Fatalf("INSERT mvbase: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE MATERIALIZED VIEW mv_pop AS SELECT id, val FROM mvbase"); err != nil {
		t.Fatalf("CREATE MATERIALIZED VIEW mv_pop: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE MATERIALIZED VIEW mv_nodata AS SELECT id, val FROM mvbase WITH NO DATA"); err != nil {
		t.Fatalf("CREATE MATERIALIZED VIEW mv_nodata WITH NO DATA: %v", err)
	}

	// Pre-restart sanity: the in-memory projection already distinguishes them.
	if got := queryScalar(t, c,
		"SELECT relispopulated FROM pg_class WHERE relname = 'mv_pop'"); got != "true" {
		t.Fatalf("pre-restart mv_pop relispopulated = %q, want true", got)
	}
	if got := queryScalar(t, c,
		"SELECT relispopulated FROM pg_class WHERE relname = 'mv_nodata'"); got != "false" {
		t.Fatalf("pre-restart mv_nodata relispopulated = %q, want false", got)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// The populated flag survives the restart for both matviews.
	if got := queryScalar(t, c,
		"SELECT relispopulated FROM pg_class WHERE relname = 'mv_pop'"); got != "true" {
		t.Fatalf("post-restart mv_pop relispopulated = %q, want true", got)
	}
	if got := queryScalar(t, c,
		"SELECT relispopulated FROM pg_class WHERE relname = 'mv_nodata'"); got != "false" {
		t.Fatalf("post-restart mv_nodata relispopulated = %q, want false (WITH NO DATA state lost across restart)", got)
	}
	// Both are still matviews (relkind='m'), not demoted to plain relations.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_class WHERE relname IN ('mv_pop','mv_nodata') AND relkind = 'm'"); got != "2" {
		t.Fatalf("post-restart matview relkind count = %q, want 2", got)
	}
}

// TestPort_MatviewSurvivesRefreshAndTwoRestarts pins M0146-0089: a matview
// that was REFRESHed came back from its SECOND restart as a plain view
// (relkind 'v'), reading the base table live, and REFRESH then failed with
// `"mv" is not a materialized view`. The second start registered user tables
// from the M0114 JSON catalog cache, which did not carry the matview flag;
// 17e9db192 retired the cache, so every start rebuilds from the catalog heap.
// PostgreSQL keeps relkind 'm' and the rows stored at the last REFRESH.
//
// The same sequence then showed the pre-REFRESH rows coming back: REFRESH
// stamped their xmax without dirtying the page or writing WAL, so pages the
// re-population did not touch reverted on restart (and on crash recovery).
// Nothing may scan mv before the restarts — a scan dirtied the pages through
// hint bits and hid the defect.
func TestPort_MatviewSurvivesRefreshAndTwoRestarts(t *testing.T) {
	c, err := cluster.New("matview-refresh-two-restarts", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, sql := range []string{
		"CREATE TABLE s (a int)",
		"INSERT INTO s SELECT generate_series(1, 3000)",
		"CREATE MATERIALIZED VIEW mv AS SELECT a FROM s",
		"CHECKPOINT",
		"DELETE FROM s WHERE a > 1000",
		"REFRESH MATERIALIZED VIEW mv",
		"CHECKPOINT",
	} {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for restart := 1; restart <= 2; restart++ {
		if err := c.Stop(cluster.ShutdownFast); err != nil {
			t.Fatalf("stop %d: %v", restart, err)
		}
		if err := c.Start(); err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		if got := queryScalar(t, c, "SELECT relkind FROM pg_class WHERE relname = 'mv'"); got != "m" {
			t.Fatalf("after restart %d: mv relkind = %q, want m", restart, got)
		}
	}
	// The stored rows, not the base table: a further delete must not show.
	if err := runSQLSimple(t, c, "DELETE FROM s WHERE a > 500"); err != nil {
		t.Fatal(err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM mv"); got != "1000" {
		t.Fatalf("count(*) FROM mv = %q, want 1000 (the rows stored by the last REFRESH)", got)
	}
	if err := runSQLSimple(t, c, "REFRESH MATERIALIZED VIEW mv"); err != nil {
		t.Fatalf("REFRESH after two restarts: %v", err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM mv"); got != "500" {
		t.Fatalf("count(*) FROM mv after REFRESH = %q, want 500", got)
	}
	// Crash right after a REFRESH: redo must replay the old rows' deletes.
	if err := runSQLSimple(t, c, "DELETE FROM s WHERE a > 100"); err != nil {
		t.Fatal(err)
	}
	if err := runSQLSimple(t, c, "REFRESH MATERIALIZED VIEW mv"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(cluster.ShutdownImmediate); err != nil {
		t.Fatalf("immediate stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart after crash: %v", err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM mv"); got != "100" {
		t.Fatalf("count(*) FROM mv after crash recovery = %q, want 100", got)
	}
}
