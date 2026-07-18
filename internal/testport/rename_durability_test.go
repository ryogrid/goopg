package testport

// 02e item B: ALTER TABLE/VIEW ... RENAME TO now survives a restart. Before this
// fix, AlterTableRenameTable mutated only the in-memory catalog and never
// re-synced the pg_class heap relname, so a renamed relation reverted to its old
// name on restart (loadUserTablesFromHeap reads the stale row). The fix reuses
// the RENAME COLUMN delete-old-by-OID + write-new pattern to rewrite pg_class
// with the new name.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_RenameSurvivesRestart renames a table and a view, restarts, and
// asserts the new names persist (old names gone) and both are still usable.
func TestPort_RenameSurvivesRestart(t *testing.T) {
	c, err := cluster.New("rename-durability", cluster.Options{
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

	// rn_t0 is the table under rename test (has its own data).
	if err := runSQLSimple(t, c, "CREATE TABLE rn_t0 (id int, val int)"); err != nil {
		t.Fatalf("CREATE TABLE rn_t0: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO rn_t0 VALUES (1,10),(2,20)"); err != nil {
		t.Fatalf("INSERT rn_t0: %v", err)
	}
	// rn_v0 is the view under rename test; it reads a SEPARATE, un-renamed base
	// table so the rename we exercise is the VIEW's own relname, not a dependency
	// (goopg stores view defs as SQL text by name — renaming a referenced table
	// is a distinct pre-existing concern, resolved by 02e item C's OID-resolved
	// RTEs, out of scope here).
	if err := runSQLSimple(t, c, "CREATE TABLE rn_base (id int, val int)"); err != nil {
		t.Fatalf("CREATE TABLE rn_base: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO rn_base VALUES (7,70)"); err != nil {
		t.Fatalf("INSERT rn_base: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE VIEW rn_v0 AS SELECT id, val FROM rn_base WHERE val >= 20"); err != nil {
		t.Fatalf("CREATE VIEW rn_v0: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE rn_t0 RENAME TO rn_t1"); err != nil {
		t.Fatalf("ALTER TABLE RENAME: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER VIEW rn_v0 RENAME TO rn_v1"); err != nil {
		t.Fatalf("ALTER VIEW RENAME: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// New names present; old names gone.
	for _, name := range []string{"rn_t1", "rn_v1"} {
		if got := queryScalar(t, c,
			"SELECT count(*) FROM pg_class WHERE relname = '"+name+"'"); got != "1" {
			t.Fatalf("post-restart pg_class has %q = %q, want 1 (rename did not survive)", name, got)
		}
	}
	for _, name := range []string{"rn_t0", "rn_v0"} {
		if got := queryScalar(t, c,
			"SELECT count(*) FROM pg_class WHERE relname = '"+name+"'"); got != "0" {
			t.Fatalf("post-restart old name %q = %q, want 0 (reverted to pre-rename name)", name, got)
		}
	}
	// The renamed relations are still usable under their new names.
	if got := queryScalar(t, c, "SELECT count(*) FROM rn_t1"); got != "2" {
		t.Fatalf("post-restart SELECT count(*) FROM rn_t1 = %q, want 2", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM rn_v1"); got != "1" {
		t.Fatalf("post-restart SELECT count(*) FROM rn_v1 = %q, want 1 (view def not reloaded under new name)", got)
	}
}
