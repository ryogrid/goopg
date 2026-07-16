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
// CREATE SCHEMA writes a real pg_namespace heap row (B1.1; XLOG_HEAP_INSERT
// + btree index entries on the wire), and the startup heap reload re-registers
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
	// the pg_namespace heap reload on Open; nothing else carries it across restart.
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

// TestPort_AlterSchemaSurvivesRestart pins B1.1's heap-UPDATE journaling:
// ALTER SCHEMA RENAME (non-HOT pg_namespace update — nspname is indexed)
// and ALTER SCHEMA OWNER both survive a restart via the heap reload.
func TestPort_AlterSchemaSurvivesRestart(t *testing.T) {
	c, err := cluster.New("alter-schema-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE SCHEMA renme"); err != nil {
		t.Fatalf("CREATE SCHEMA renme: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER SCHEMA renme RENAME TO renamed"); err != nil {
		t.Fatalf("ALTER SCHEMA RENAME: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE schema_owner_b1"); err != nil {
		t.Fatalf("CREATE ROLE: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER SCHEMA renamed OWNER TO schema_owner_b1"); err != nil {
		t.Fatalf("ALTER SCHEMA OWNER: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 'renamed'"); got != "1" {
		t.Fatalf("post-restart renamed schema count = %q, want 1 (rename not durable)", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_namespace WHERE nspname = 'renme'"); got != "0" {
		t.Fatalf("post-restart old-name count = %q, want 0 (old version resurrected)", got)
	}
}

// TestPort_FunctionSurvivesRestart pins B1.2's pg_proc heap journaling:
// CREATE [OR REPLACE] FUNCTION, ALTER FUNCTION (rename/volatility), and
// DROP FUNCTION all survive a restart via the pg_proc heap reload —
// replacing the retired initdb function_ddl_recovery scanner tests.
func TestPort_FunctionSurvivesRestart(t *testing.T) {
	c, err := cluster.New("function-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE FUNCTION b12_add(a int, b int) RETURNS int LANGUAGE sql AS 'SELECT a + b'"); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE OR REPLACE FUNCTION b12_add(a int, b int) RETURNS int LANGUAGE sql IMMUTABLE AS 'SELECT a + b + 0'"); err != nil {
		t.Fatalf("CREATE OR REPLACE: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE FUNCTION b12_gone() RETURNS int LANGUAGE sql AS 'SELECT 1'"); err != nil {
		t.Fatalf("CREATE FUNCTION b12_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP FUNCTION b12_gone()"); err != nil {
		t.Fatalf("DROP FUNCTION: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	if got := queryScalar(t, c, "SELECT b12_add(20, 22)"); got != "42" {
		t.Fatalf("post-restart b12_add(20,22) = %q, want 42 (function or its REPLACE body not durable)", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_proc WHERE proname = 'b12_gone'"); got != "0" {
		t.Fatalf("post-restart dropped function count = %q, want 0", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_proc WHERE proname = 'b12_add'"); got != "1" {
		t.Fatalf("post-restart b12_add pg_proc count = %q, want 1 (OR REPLACE must not duplicate)", got)
	}
}

// TestPort_SequenceCatalogRowSurvivesRestart pins B1.3: CREATE/ALTER
// SEQUENCE journal real pg_sequence heap rows (definition), and the row
// updates in place after a restart (TID reseed) instead of duplicating.
func TestPort_SequenceCatalogRowSurvivesRestart(t *testing.T) {
	c, err := cluster.New("sequence-catalog-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE SEQUENCE b13_seq INCREMENT 2 MAXVALUE 1000"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if got := queryScalar(t, c, "SELECT seqincrement FROM pg_sequence WHERE seqrelid = 'b13_seq'::regclass"); got != "2" {
		t.Fatalf("pre-restart seqincrement = %q, want 2", got)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Post-restart ALTER must UPDATE the reseeded row in place, not insert
	// a duplicate.
	if err := runSQLSimple(t, c, "ALTER SEQUENCE b13_seq INCREMENT 5"); err != nil {
		t.Fatalf("ALTER SEQUENCE: %v", err)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_sequence WHERE seqrelid = 'b13_seq'::regclass"); got != "1" {
		t.Fatalf("post-alter pg_sequence row count = %q, want 1 (duplicate row = TID reseed broken)", got)
	}
	if got := queryScalar(t, c, "SELECT seqincrement FROM pg_sequence WHERE seqrelid = 'b13_seq'::regclass"); got != "5" {
		t.Fatalf("post-alter seqincrement = %q, want 5", got)
	}
}

// TestPort_DomainSurvivesRestart pins B2.1b's domain heap journaling:
// CREATE DOMAIN (with CHECK + DEFAULT + NOT NULL) reloads from its pg_type +
// pg_constraint heap rows — replacing the retired kind-119/120 WAL scanner —
// and ALTER DOMAIN mutations (previously NOT durable at all) survive via
// non-HOT pg_type heap updates.
func TestPort_DomainSurvivesRestart(t *testing.T) {
	c, err := cluster.New("domain-durability", cluster.Options{
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
		"CREATE DOMAIN b21_color AS text CHECK (VALUE IN ('red', 'green', 'blue'))"); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN b21_old AS int"); err != nil {
		t.Fatalf("CREATE DOMAIN b21_old: %v", err)
	}
	// ALTER durability (all-new in B2.1b — these previously vanished on restart).
	if err := runSQLSimple(t, c, "ALTER DOMAIN b21_old RENAME TO b21_renamed"); err != nil {
		t.Fatalf("ALTER DOMAIN RENAME: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER DOMAIN b21_renamed SET DEFAULT 7"); err != nil {
		t.Fatalf("ALTER DOMAIN SET DEFAULT: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN b21_gone AS int"); err != nil {
		t.Fatalf("CREATE DOMAIN b21_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP DOMAIN b21_gone"); err != nil {
		t.Fatalf("DROP DOMAIN: %v", err)
	}
	// Sanity: the CHECK must enforce BEFORE the restart, or the post-restart
	// assertion below tests nothing.
	if err := runSQLSimple(t, c, "SELECT 'mauve'::b21_color"); err == nil {
		t.Fatal("pre-restart invalid domain cast succeeded — CHECK not enforced at all")
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	// CHECK (VALUE IN ...) must still ENFORCE post-restart (InValues are
	// re-derived from the pg_constraint row's conbin text).
	if got := queryScalar(t, c, "SELECT 'red'::b21_color"); got != "red" {
		t.Fatalf("post-restart valid domain cast = %q, want red", got)
	}
	if err := runSQLSimple(t, c, "SELECT 'mauve'::b21_color"); err == nil {
		t.Fatal("post-restart invalid domain cast succeeded — CHECK constraint not reloaded")
	}
	// The rename + SET DEFAULT survive: the renamed domain resolves, and the
	// heap-backed pg_type row carries the default. (INSERT-time application
	// of domain defaults is a separate, pre-existing goopg gap — verified
	// absent even without a restart — so the durability probe reads the
	// catalog row, not an inserted value.)
	if got := queryScalar(t, c, "SELECT 5::b21_renamed"); got != "5" {
		t.Fatalf("post-restart renamed domain cast = %q, want 5 (RENAME not durable)", got)
	}
	if got := queryScalar(t, c,
		"SELECT typdefaultbin FROM pg_type WHERE typname = 'b21_renamed'"); got != "7" {
		t.Fatalf("post-restart typdefaultbin = %q, want 7 (SET DEFAULT not durable)", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_type WHERE typname = 'b21_gone'"); got != "0" {
		t.Fatalf("post-restart dropped domain pg_type count = %q, want 0", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_type WHERE typname = 'b21_old'"); got != "0" {
		t.Fatalf("post-restart old-name pg_type count = %q, want 0 (rename left stale row)", got)
	}
}

// TestPort_RangeTypeSurvivesRestart pins B2.1c's range-type heap journaling:
// CREATE TYPE AS RANGE reloads from its pg_range + pg_type heap rows —
// replacing the retired kind-81/82/117/118 WAL scanner — and ALTER TYPE
// RENAME/OWNER survive via non-HOT pg_type heap updates.
func TestPort_RangeTypeSurvivesRestart(t *testing.T) {
	c, err := cluster.New("range-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE TYPE b21c_r AS RANGE (subtype = int4)"); err != nil {
		t.Fatalf("CREATE TYPE AS RANGE: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TYPE b21c_old AS RANGE (subtype = int8)"); err != nil {
		t.Fatalf("CREATE TYPE AS RANGE b21c_old: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE b21c_old RENAME TO b21c_renamed"); err != nil {
		t.Fatalf("ALTER TYPE RENAME: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TYPE b21c_gone AS RANGE (subtype = date)"); err != nil {
		t.Fatalf("CREATE TYPE AS RANGE b21c_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TYPE b21c_gone"); err != nil {
		t.Fatalf("DROP TYPE: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	// The range type + its multirange linkage must be usable post-restart.
	if got := queryScalar(t, c, "SELECT '[1,5)'::b21c_r::text"); got != "[1,5)" {
		t.Fatalf("post-restart range cast = %q, want [1,5)", got)
	}
	if got := queryScalar(t, c, "SELECT '[10,20)'::b21c_renamed::text"); got != "[10,20)" {
		t.Fatalf("post-restart renamed range cast = %q, want [10,20) (RENAME not durable)", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_type WHERE typname = 'b21c_gone'"); got != "0" {
		t.Fatalf("post-restart dropped range pg_type count = %q, want 0", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_range r JOIN pg_type t ON t.oid = r.rngtypid WHERE t.typname = 'b21c_r'"); got != "1" {
		t.Fatalf("post-restart pg_range row count = %q, want 1", got)
	}
}
