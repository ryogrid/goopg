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
