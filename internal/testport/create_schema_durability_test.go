package testport

// End-to-end coverage of CREATE SCHEMA durability across a server restart
// (M0110-0003 enabler).
//
// goopg's CREATE SCHEMA is a catalog-only side effect: the schema name is
// recorded in the in-memory catalog registry that backs pg_namespace and
// schema-qualified relation resolution, but (unlike pg_class for CREATE TABLE)
// it has no per-schema on-disk file namespace. Before this change the registry
// entry was lost on restart, so a `--schema s1` run that was clean before a
// restart reported "no relations to check in schemas matching s1" afterwards
// (surfaced repeatedly while porting pg_amcheck/t/003_check.pl).
//
// The fix mirrors the CREATE/DROP DATABASE WAL-record mechanism (M0054-0001):
// CREATE SCHEMA appends a wal.RecordKindCreateSchema record (carrying the
// assigned OID), and the recovery driver replaySchemaDDLRecords re-registers
// each schema after physical replay on the next Open.
//
// This e2e proves the full path through the real executor emit site
// (execCompatNoop case "schema") and the restart/replay, which the
// wal/initdb unit tests cannot: that a `CREATE SCHEMA` issued over the wire
// survives a clean stop -> restart and remains visible in pg_namespace, and
// that a subsequent DROP SCHEMA is likewise durable.
//
// Design doc: docs/design/0110-0012-create-schema-wal-durability.md.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_CreateSchemaSurvivesRestart creates a user schema, restarts the
// cluster, and asserts the schema is still registered (visible in
// pg_namespace) — then drops it and asserts the drop is also durable.
func TestPort_CreateSchemaSurvivesRestart(t *testing.T) {
	c, err := cluster.New("create-schema-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE SCHEMA s1"); err != nil {
		t.Fatalf("CREATE SCHEMA s1: %v", err)
	}

	// Pre-restart sanity: the schema is registered and visible in pg_namespace.
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 's1'"); got != "1" {
		t.Fatalf("pre-restart pg_namespace count for s1 = %q, want 1 "+
			"(CREATE SCHEMA did not register the schema)", got)
	}

	// Clean stop -> restart. The schema registry is rebuilt from the WAL by
	// replaySchemaDDLRecords on Open; nothing else carries it across restart.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 's1'"); got != "1" {
		t.Fatalf("post-restart pg_namespace count for s1 = %q, want 1 "+
			"(schema did not survive the restart — WAL replay missing or broken)", got)
	}

	// DROP SCHEMA must be durable too: drop, restart, confirm it stays gone.
	if err := runSQLSimple(t, c, "DROP SCHEMA s1"); err != nil {
		t.Fatalf("DROP SCHEMA s1: %v", err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 's1'"); got != "0" {
		t.Fatalf("post-drop pg_namespace count for s1 = %q, want 0", got)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster after drop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after drop: %v", err)
	}

	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 's1'"); got != "0" {
		t.Fatalf("post-restart-after-drop pg_namespace count for s1 = %q, want 0 "+
			"(DROP SCHEMA was not durable — a stale CREATE record was replayed)", got)
	}
}
