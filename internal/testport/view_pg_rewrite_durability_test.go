package testport

// End-to-end coverage of view / materialized-view restart persistence after
// B5 Slice C: a view's defining SELECT now journals as a real pg_rewrite
// _RETURN rule heap row (base/<dbOid>/2618, ev_action = the query as SQL text)
// instead of the retired RecordKindCreateView(103)/RecordKindCreateMatView(102)
// records. The reload (loadViewsFromHeap) re-parses ev_action to rebuild the
// view AST. This test exercises that full write->restart->reload round-trip for
// a plain view, a matview, and a dropped view.
//
// NOTE: ALTER VIEW/TABLE RENAME across a restart is covered by
// TestPort_RenameSurvivesRestart (02e item B fixed the pg_class relname
// re-sync). This test focuses on the pg_rewrite view-query reload.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_ViewsSurviveRestart creates views/matviews, restarts, and asserts
// each survives (or stays gone) with its definition queryable.
func TestPort_ViewsSurviveRestart(t *testing.T) {
	c, err := cluster.New("view-pg-rewrite-durability", cluster.Options{
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

	if err := runSQLSimple(t, c,
		"CREATE TABLE vbase (id int, val int)"); err != nil {
		t.Fatalf("CREATE TABLE vbase: %v", err)
	}
	if err := runSQLSimple(t, c,
		"INSERT INTO vbase VALUES (1,10),(2,20),(3,30)"); err != nil {
		t.Fatalf("INSERT vbase: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE VIEW v_plain AS SELECT id, val FROM vbase WHERE val >= 20"); err != nil {
		t.Fatalf("CREATE VIEW v_plain: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE MATERIALIZED VIEW v_mat AS SELECT id, val FROM vbase WHERE id = 1"); err != nil {
		t.Fatalf("CREATE MATERIALIZED VIEW v_mat: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE VIEW v_gone AS SELECT val FROM vbase"); err != nil {
		t.Fatalf("CREATE VIEW v_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP VIEW v_gone"); err != nil {
		t.Fatalf("DROP VIEW v_gone: %v", err)
	}

	// Pre-restart: the plain view returns 2 rows (val 20, 30).
	if got := queryScalar(t, c, "SELECT count(*) FROM v_plain"); got != "2" {
		t.Fatalf("pre-restart v_plain count = %q, want 2", got)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Plain view is still queryable and returns the same result — proves the
	// pg_rewrite ev_action re-parsed into the correct SELECT (WHERE predicate
	// intact).
	if got := queryScalar(t, c, "SELECT count(*) FROM v_plain"); got != "2" {
		t.Fatalf("post-restart v_plain count = %q, want 2 (view def not reloaded)", got)
	}
	if got := queryScalar(t, c, "SELECT sum(val) FROM v_plain"); got != "50" {
		t.Fatalf("post-restart v_plain sum(val) = %q, want 50 (WHERE predicate lost)", got)
	}
	// Matview survives as a matview (relkind='m') and returns its populated row.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_class WHERE relname = 'v_mat' AND relkind = 'm'"); got != "1" {
		t.Fatalf("post-restart v_mat relkind = %q, want a matview (reloaded as plain table)", got)
	}
	if got := queryScalar(t, c, "SELECT val FROM v_mat"); got != "10" {
		t.Fatalf("post-restart v_mat val = %q, want 10", got)
	}
	// Dropped view stays gone.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_class WHERE relname = 'v_gone'"); got != "0" {
		t.Fatalf("post-restart v_gone count = %q, want 0 (drop did not survive)", got)
	}
}
