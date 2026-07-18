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

// TestPort_EnumSurvivesRestart pins B2.1d's pg_enum heap journaling: enums
// previously had NO restart durability at all (labels lived only in the
// in-memory registry). CREATE TYPE AS ENUM / ADD VALUE / RENAME VALUE all
// reload from the pg_type + pg_enum heaps.
func TestPort_EnumSurvivesRestart(t *testing.T) {
	c, err := cluster.New("enum-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE TYPE b21d_mood AS ENUM ('sad', 'ok', 'happy')"); err != nil {
		t.Fatalf("CREATE TYPE AS ENUM: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE b21d_mood ADD VALUE 'ecstatic' AFTER 'happy'"); err != nil {
		t.Fatalf("ADD VALUE: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE b21d_mood RENAME VALUE 'ok' TO 'fine'"); err != nil {
		t.Fatalf("RENAME VALUE: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TYPE b21d_gone AS ENUM ('x')"); err != nil {
		t.Fatalf("CREATE b21d_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP TYPE b21d_gone"); err != nil {
		t.Fatalf("DROP TYPE: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	if got := queryScalar(t, c, "SELECT 'happy'::b21d_mood"); got != "happy" {
		t.Fatalf("post-restart enum cast = %q, want happy (enum not reloaded)", got)
	}
	if got := queryScalar(t, c, "SELECT 'ecstatic'::b21d_mood"); got != "ecstatic" {
		t.Fatalf("post-restart added value = %q, want ecstatic (ADD VALUE not durable)", got)
	}
	if got := queryScalar(t, c, "SELECT 'fine'::b21d_mood"); got != "fine" {
		t.Fatalf("post-restart renamed value = %q, want fine (RENAME VALUE not durable)", got)
	}
	if err := runSQLSimple(t, c, "SELECT 'ok'::b21d_mood"); err == nil {
		t.Fatal("post-restart old label 'ok' still accepted (rename left stale row)")
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid WHERE t.typname = 'b21d_mood'"); got != "4" {
		t.Fatalf("post-restart pg_enum row count = %q, want 4", got)
	}
	if got := queryScalar(t, c, "SELECT count(*) FROM pg_type WHERE typname = 'b21d_gone'"); got != "0" {
		t.Fatalf("post-restart dropped enum pg_type count = %q, want 0", got)
	}
}

// TestPort_CastSurvivesRestart pins B2.2a's pg_cast heap journaling:
// CREATE CAST reloads from its pg_cast heap row (kinds 38/39 retired) and
// DROP CAST is durable.
func TestPort_CastSurvivesRestart(t *testing.T) {
	c, err := cluster.New("cast-durability", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE FUNCTION b22_i2t(int) RETURNS text LANGUAGE sql AS 'SELECT $1::text'"); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE CAST (int AS text) WITH FUNCTION b22_i2t(int) AS IMPLICIT"); err != nil {
		t.Fatalf("CREATE CAST: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE CAST (int AS bool) WITH INOUT"); err != nil {
		t.Fatalf("CREATE CAST 2: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP CAST (int AS bool)"); err != nil {
		t.Fatalf("DROP CAST: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Numeric type OIDs (int4=23, text=25, bool=16), not 'int'::regtype:
	// goopg's regtype input leaves builtin type NAMES as strings (only OID
	// digits and user-type names resolve), so the oid-column comparison
	// silently matches nothing (ledger: regtype-builtin-name-input gap).
	// oid >= 16384 scopes to user casts — int4→bool has a BUILTIN pg_cast
	// row (10034) that survives the user cast's drop.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_cast WHERE castsource = 23 AND casttarget = 25 AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart pg_cast count = %q, want 1 (cast not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_cast WHERE castsource = 23 AND casttarget = 16 AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart dropped cast count = %q, want 0", got)
	}
}

// TestPort_AggregateSurvivesRestart pins B2.2 slice 2's pg_aggregate/pg_proc
// heap journaling: CREATE AGGREGATE reloads from its prokind='a' pg_proc row
// (kinds 46-49 retired), an ALTER ... RENAME survives as a pg_proc heap
// UPDATE, a dropped aggregate stays dropped, and the reloaded aggregate is
// EXECUTABLE (transfn name fidelity through the JSON meta).
func TestPort_AggregateSurvivesRestart(t *testing.T) {
	c, err := cluster.New("aggregate-durability", cluster.Options{
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

	stmts := []string{
		"CREATE FUNCTION b22b_acc(int, int) RETURNS int LANGUAGE sql AS 'SELECT $1 + $2'",
		"CREATE FUNCTION b22b_fin(int) RETURNS text LANGUAGE sql AS 'SELECT $1::text'",
		"CREATE AGGREGATE b22b_sum(int) (SFUNC = b22b_acc, STYPE = int, INITCOND = '0', FINALFUNC = b22b_fin)",
		"CREATE AGGREGATE b22b_gone(int) (SFUNC = b22b_acc, STYPE = int)",
		"DROP AGGREGATE b22b_gone(int)",
		"ALTER AGGREGATE b22b_sum(int) RENAME TO b22b_total",
		"CREATE TABLE b22b_t (v int)",
		"INSERT INTO b22b_t VALUES (1), (2), (3)",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_proc WHERE proname = 'b22b_total' AND prokind = 'a'"); got != "1" {
		t.Fatalf("post-restart renamed aggregate pg_proc count = %q, want 1", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_proc WHERE proname IN ('b22b_sum', 'b22b_gone') AND prokind = 'a'"); got != "0" {
		t.Fatalf("post-restart stale aggregate names count = %q, want 0", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_aggregate a JOIN pg_proc p ON p.oid = a.aggfnoid WHERE p.proname = 'b22b_total'"); got != "1" {
		t.Fatalf("post-restart pg_aggregate join count = %q, want 1", got)
	}
	if got := queryScalar(t, c, "SELECT b22b_total(v) FROM b22b_t"); got != "6" {
		t.Fatalf("post-restart aggregate execution = %q, want 6 (transfn/initcond/finalfunc lost?)", got)
	}
}

// TestPort_OperatorSurvivesRestart pins B2.2 slice 3's pg_operator heap
// journaling: CREATE OPERATOR (with a COMMUTATOR pair — the two-pass
// shell/back-patch scheme produces heap UPDATEs) reloads from its
// pg_operator row (kinds 83/84 retired), a dropped operator stays dropped,
// and the reloaded operator is EXECUTABLE post-restart.
func TestPort_OperatorSurvivesRestart(t *testing.T) {
	c, err := cluster.New("operator-durability", cluster.Options{
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

	stmts := []string{
		"CREATE FUNCTION b22c_addmod(int, int) RETURNS int LANGUAGE sql AS 'SELECT ($1 + $2) % 7'",
		// Forward COMMUTATOR reference: mints a shell for >+< first, then
		// the second CREATE fills it in and back-patches (OperatorUpd).
		"CREATE OPERATOR <+> (LEFTARG = int, RIGHTARG = int, FUNCTION = b22c_addmod, COMMUTATOR = OPERATOR(>+<))",
		"CREATE OPERATOR >+< (LEFTARG = int, RIGHTARG = int, FUNCTION = b22c_addmod, COMMUTATOR = OPERATOR(<+>))",
		"CREATE OPERATOR <+< (LEFTARG = int, RIGHTARG = int, FUNCTION = b22c_addmod)",
		"DROP OPERATOR <+< (int, int)",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_operator WHERE oprname = '<+>' AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart pg_operator count for <+> = %q, want 1 (operator not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_operator a JOIN pg_operator b ON a.oprcom = b.oid WHERE a.oprname = '<+>' AND b.oprname = '>+<'"); got != "1" {
		t.Fatalf("post-restart commutator link count = %q, want 1 (back-patch lost)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_operator WHERE oprname = '<+<' AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart dropped operator count = %q, want 0", got)
	}
	// Executing a user-defined operator is out of goopg's scope (the
	// create_operator regress test is excluded "out of scope for v0"), so
	// the function link pins via the oprcode → pg_proc join instead.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_operator o JOIN pg_proc p ON p.oid = o.oprcode::oid WHERE o.oprname = '<+>' AND p.proname = 'b22c_addmod'"); got != "1" {
		t.Fatalf("post-restart oprcode join count = %q, want 1 (function link lost)", got)
	}
}

// TestPort_CollationSurvivesRestart pins B2.2 slice 4's pg_collation heap
// journaling: CREATE COLLATION reloads from its pg_collation row (kinds
// 42-45/93 retired), ALTER ... RENAME/OWNER survive as heap UPDATEs, and a
// dropped collation stays dropped.
func TestPort_CollationSurvivesRestart(t *testing.T) {
	c, err := cluster.New("collation-durability", cluster.Options{
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

	stmts := []string{
		"CREATE COLLATION b22d_coll (provider = icu, locale = 'de-u-co-phonebk', deterministic = false)",
		"ALTER COLLATION b22d_coll RENAME TO b22d_coll2",
		"CREATE ROLE b22d_owner",
		"ALTER COLLATION b22d_coll2 OWNER TO b22d_owner",
		"CREATE COLLATION b22d_gone (locale = 'C')",
		"DROP COLLATION b22d_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_collation WHERE collname = 'b22d_coll2' AND collprovider = 'i' AND NOT collisdeterministic AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart renamed collation count = %q, want 1 (collation not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_collation WHERE collname IN ('b22d_coll', 'b22d_gone') AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart stale collation names count = %q, want 0", got)
	}
	if got := queryScalar(t, c,
		"SELECT colllocale FROM pg_collation WHERE collname = 'b22d_coll2'"); got != "de-u-co-phonebk" {
		t.Fatalf("post-restart colllocale = %q, want de-u-co-phonebk", got)
	}
}

// TestPort_ConversionSurvivesRestart pins B2.2 slice 4's pg_conversion heap
// journaling (kinds 40/41/130-132 retired): CREATE [DEFAULT] CONVERSION
// reloads from its pg_conversion row with the conproc link intact, ALTER
// RENAME survives as a heap UPDATE, and a dropped conversion stays dropped.
func TestPort_ConversionSurvivesRestart(t *testing.T) {
	c, err := cluster.New("conversion-durability", cluster.Options{
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

	stmts := []string{
		"CREATE CONVERSION b22d_conv FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8",
		"ALTER CONVERSION b22d_conv RENAME TO b22d_conv2",
		"CREATE CONVERSION b22d_gone FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8",
		"DROP CONVERSION b22d_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_conversion WHERE conname = 'b22d_conv2' AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart renamed conversion count = %q, want 1 (conversion not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_conversion WHERE conname IN ('b22d_conv', 'b22d_gone') AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart stale conversion names count = %q, want 0", got)
	}
}

// TestPort_OpClassFamilySurvivesRestart pins B2.2 slice 5's pg_opfamily /
// pg_opclass / pg_amop / pg_amproc heap journaling (kinds 85-92 retired):
// CREATE OPERATOR FAMILY / CLASS (with AS-list members), ALTER OPERATOR
// FAMILY ADD (a family-attributed "loose" member), and the drops all
// survive a restart — including each member's CLASS attribution, which
// rides an INTERNAL pg_depend row because pg_amop/pg_amproc have no column
// for it (PG's own channel: opclasscmds.c storeOperators).
func TestPort_OpClassFamilySurvivesRestart(t *testing.T) {
	c, err := cluster.New("opclass-durability", cluster.Options{
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

	stmts := []string{
		"CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)",
		"CREATE OPERATOR FAMILY public.b22e_fam USING btree",
		`CREATE OPERATOR CLASS public.b22e_class FOR TYPE int4 USING btree FAMILY public.b22e_fam AS
			OPERATOR 1 ~=~ (int4, int4),
			FUNCTION 1 int4eq(int4, int4)`,
		// A loose (family-attributed) member: no class attribution, so no
		// INTERNAL pg_depend row — its zero ClassOID must survive too.
		"ALTER OPERATOR FAMILY public.b22e_fam USING btree ADD OPERATOR 3 ~=~ (int4, int4)",
		"CREATE OPERATOR FAMILY public.b22e_gone USING btree",
		"DROP OPERATOR FAMILY public.b22e_gone USING btree",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_opfamily WHERE opfname = 'b22e_fam' AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart pg_opfamily count = %q, want 1 (family not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_opfamily WHERE opfname = 'b22e_gone' AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart dropped family count = %q, want 0", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_opclass c JOIN pg_opfamily f ON f.oid = c.opcfamily WHERE c.opcname = 'b22e_class' AND f.opfname = 'b22e_fam' AND c.opcintype = 23"); got != "1" {
		t.Fatalf("post-restart pg_opclass join count = %q, want 1 (class or its family link lost)", got)
	}
	// Both AS-list members plus the ALTER-ADD'd loose operator.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_amop a JOIN pg_opfamily f ON f.oid = a.amopfamily WHERE f.opfname = 'b22e_fam' AND a.oid >= 16384"); got != "2" {
		t.Fatalf("post-restart pg_amop count = %q, want 2 (AS-list + ALTER-ADD member)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_amproc p JOIN pg_opfamily f ON f.oid = p.amprocfamily WHERE f.opfname = 'b22e_fam' AND p.oid >= 16384"); got != "1" {
		t.Fatalf("post-restart pg_amproc count = %q, want 1", got)
	}
	// Class attribution: the AS-list OPERATOR/FUNCTION members keep their
	// INTERNAL ('i') dependency on the class, while the ALTER-ADD'd member
	// stays AUTO ('a') on the family (PG's AlterOpFamilyAdd semantics).
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_depend d JOIN pg_opclass c ON c.oid = d.refobjid WHERE d.refclassid = 2616 AND d.deptype = 'i' AND c.opcname = 'b22e_class'"); got != "2" {
		t.Fatalf("post-restart INTERNAL class-attribution deps = %q, want 2 (member ClassOID lost)", got)
	}
}

// TestPort_TransformSurvivesRestart pins B3.1's pg_transform heap
// journaling: CREATE TRANSFORM reloads from its pg_transform row (kinds
// 36/37 retired) and DROP TRANSFORM is durable.
func TestPort_TransformSurvivesRestart(t *testing.T) {
	c, err := cluster.New("transform-durability", cluster.Options{
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

	stmts := []string{
		"CREATE TRANSFORM FOR int LANGUAGE sql (FROM SQL WITH FUNCTION prsd_lextype(internal), TO SQL WITH FUNCTION int4recv(internal))",
		"CREATE TRANSFORM FOR float8 LANGUAGE sql (FROM SQL WITH FUNCTION prsd_lextype(internal))",
		"DROP TRANSFORM FOR float8 LANGUAGE sql",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_transform WHERE trftype = 23 AND oid >= 16384"); got != "1" {
		t.Fatalf("post-restart pg_transform count for int = %q, want 1 (transform not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_transform WHERE trftype = 701 AND oid >= 16384"); got != "0" {
		t.Fatalf("post-restart dropped transform count = %q, want 0", got)
	}
}

// TestPort_EventTriggerSurvivesRestart pins B3.2's pg_event_trigger heap
// journaling: CREATE EVENT TRIGGER (with a WHEN TAG filter → evttags text[]
// array), ALTER ENABLE/DISABLE (evtenabled UPDATE), ALTER RENAME, and DROP
// all survive a restart via the pg_event_trigger heap reload (kinds 56-60
// retired).
func TestPort_EventTriggerSurvivesRestart(t *testing.T) {
	c, err := cluster.New("event-trigger-durability", cluster.Options{
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

	stmts := []string{
		"CREATE FUNCTION b32_et_func() RETURNS event_trigger LANGUAGE plpgsql AS 'BEGIN END'",
		"CREATE EVENT TRIGGER b32_et ON ddl_command_start WHEN TAG IN ('CREATE TABLE', 'ALTER TABLE') EXECUTE FUNCTION b32_et_func()",
		"ALTER EVENT TRIGGER b32_et DISABLE",
		"ALTER EVENT TRIGGER b32_et RENAME TO b32_et2",
		"CREATE EVENT TRIGGER b32_gone ON sql_drop EXECUTE FUNCTION b32_et_func()",
		"DROP EVENT TRIGGER b32_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_event_trigger WHERE evtname = 'b32_et2' AND evtevent = 'ddl_command_start' AND evtenabled = 'D'"); got != "1" {
		t.Fatalf("post-restart renamed+disabled event trigger count = %q, want 1 (not reloaded / ALTER lost)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_event_trigger WHERE evtname IN ('b32_et', 'b32_gone')"); got != "0" {
		t.Fatalf("post-restart stale event-trigger names count = %q, want 0", got)
	}
	// The WHEN TAG filter (evttags text[]) round-trips.
	if got := queryScalar(t, c,
		"SELECT array_length(evttags, 1) FROM pg_event_trigger WHERE evtname = 'b32_et2'"); got != "2" {
		t.Fatalf("post-restart evttags length = %q, want 2 (WHEN TAG array lost)", got)
	}
}

// TestPort_PublicationSurvivesRestart pins B3.3's pg_publication +
// pg_publication_rel heap journaling: CREATE PUBLICATION (both FOR ALL
// TABLES and FOR TABLE with members), ALTER OWNER, and DROP all survive a
// restart via the heap reload (kinds 50-52 retired; subscription 53-55
// stays bespoke for B4).
func TestPort_PublicationSurvivesRestart(t *testing.T) {
	c, err := cluster.New("publication-durability", cluster.Options{
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

	stmts := []string{
		"CREATE TABLE b33_t1 (a int)",
		"CREATE TABLE b33_t2 (a int)",
		"CREATE ROLE b33_owner",
		"CREATE PUBLICATION b33_all FOR ALL TABLES",
		"CREATE PUBLICATION b33_some FOR TABLE b33_t1, b33_t2 WITH (publish = 'insert, update')",
		"ALTER PUBLICATION b33_some OWNER TO b33_owner",
		"CREATE PUBLICATION b33_gone FOR ALL TABLES",
		"DROP PUBLICATION b33_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_publication WHERE pubname = 'b33_all' AND puballtables"); got != "1" {
		t.Fatalf("post-restart FOR ALL TABLES publication count = %q, want 1 (not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_publication WHERE pubname = 'b33_some' AND NOT puballtables AND pubinsert AND pubupdate AND NOT pubdelete"); got != "1" {
		t.Fatalf("post-restart FOR TABLE publication (publish flags) count = %q, want 1", got)
	}
	// The two member relations round-trip through the PubSub registry, which
	// goopg exposes as pg_publication_tables and which the reload repopulates
	// from the pg_publication_rel heap.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_publication_tables WHERE pubname = 'b33_some'"); got != "2" {
		t.Fatalf("post-restart publication member count = %q, want 2 (pub.Tables not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_publication WHERE pubname = 'b33_gone'"); got != "0" {
		t.Fatalf("post-restart dropped publication count = %q, want 0", got)
	}
}

// TestPort_ForeignDataSurvivesRestart pins B3.4's pg_foreign_data_wrapper +
// pg_foreign_server + pg_user_mapping heap journaling: CREATE FOREIGN DATA
// WRAPPER (which gained restart durability in this slice), CREATE SERVER
// (srvfdw → FDW OID), CREATE USER MAPPING (umserver → server OID, umuser →
// role OID), and the drops all survive a restart (kinds 126-129 retired).
func TestPort_ForeignDataSurvivesRestart(t *testing.T) {
	c, err := cluster.New("foreign-data-durability", cluster.Options{
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

	stmts := []string{
		"CREATE ROLE b34_role",
		"CREATE FOREIGN DATA WRAPPER b34_fdw",
		"CREATE SERVER b34_srv FOREIGN DATA WRAPPER b34_fdw",
		"CREATE USER MAPPING FOR b34_role SERVER b34_srv",
		"CREATE SERVER b34_gone FOREIGN DATA WRAPPER b34_fdw",
		"DROP SERVER b34_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_foreign_data_wrapper WHERE fdwname = 'b34_fdw'"); got != "1" {
		t.Fatalf("post-restart FDW count = %q, want 1 (FDW durability not added / reloaded)", got)
	}
	// The server's srvfdw resolves back to the FDW by OID after reload.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_foreign_server s JOIN pg_foreign_data_wrapper f ON f.oid = s.srvfdw WHERE s.srvname = 'b34_srv' AND f.fdwname = 'b34_fdw'"); got != "1" {
		t.Fatalf("post-restart server→FDW join count = %q, want 1 (srvfdw lost)", got)
	}
	// The user mapping's umserver + umuser resolve back by OID.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_user_mappings WHERE srvname = 'b34_srv' AND usename = 'b34_role'"); got != "1" {
		t.Fatalf("post-restart user-mapping count = %q, want 1 (umserver/umuser lost)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_foreign_server WHERE srvname = 'b34_gone'"); got != "0" {
		t.Fatalf("post-restart dropped server count = %q, want 0", got)
	}
}

// TestPort_TSDictSurvivesRestart pins B3.5's pg_ts_dict heap journaling:
// CREATE TEXT SEARCH DICTIONARY, ALTER (RENAME / SET SCHEMA / options), and
// DROP all survive a restart via the pg_ts_dict heap reload (kinds
// 104/105/114/115/116 retired).
func TestPort_TSDictSurvivesRestart(t *testing.T) {
	c, err := cluster.New("tsdict-durability", cluster.Options{
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

	stmts := []string{
		"CREATE SCHEMA b35_s",
		"CREATE TEXT SEARCH DICTIONARY b35_dict (TEMPLATE = pg_catalog.simple, STOPWORDS = english)",
		"ALTER TEXT SEARCH DICTIONARY b35_dict RENAME TO b35_dict2",
		"ALTER TEXT SEARCH DICTIONARY b35_dict2 SET SCHEMA b35_s",
		"CREATE TEXT SEARCH DICTIONARY b35_gone (TEMPLATE = pg_catalog.simple)",
		"DROP TEXT SEARCH DICTIONARY b35_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Renamed + moved to schema b35_s, with its init options intact.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_ts_dict d JOIN pg_namespace n ON n.oid = d.dictnamespace WHERE d.dictname = 'b35_dict2' AND n.nspname = 'b35_s' AND d.dictinitoption IS NOT NULL"); got != "1" {
		t.Fatalf("post-restart renamed+moved dict count = %q, want 1 (not reloaded / ALTER lost)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_ts_dict WHERE dictname IN ('b35_dict', 'b35_gone')"); got != "0" {
		t.Fatalf("post-restart stale dict names count = %q, want 0", got)
	}
}

// TestPort_TSConfigSurvivesRestart pins B3.6's pg_ts_config +
// pg_ts_config_map heap journaling: CREATE TEXT SEARCH CONFIGURATION, ADD
// MAPPING (config_map rows), ALTER (RENAME / SET SCHEMA), and DROP all
// survive a restart (kinds 106-113 retired).
func TestPort_TSConfigSurvivesRestart(t *testing.T) {
	c, err := cluster.New("tsconfig-durability", cluster.Options{
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

	stmts := []string{
		"CREATE SCHEMA b36_s",
		"CREATE TEXT SEARCH CONFIGURATION b36_cfg (PARSER = pg_catalog.default)",
		"ALTER TEXT SEARCH CONFIGURATION b36_cfg ADD MAPPING FOR asciiword, word WITH simple",
		"ALTER TEXT SEARCH CONFIGURATION b36_cfg RENAME TO b36_cfg2",
		"ALTER TEXT SEARCH CONFIGURATION b36_cfg2 SET SCHEMA b36_s",
		"CREATE TEXT SEARCH CONFIGURATION b36_gone (PARSER = pg_catalog.default)",
		"DROP TEXT SEARCH CONFIGURATION b36_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Renamed + moved to b36_s.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_ts_config cfg JOIN pg_namespace n ON n.oid = cfg.cfgnamespace WHERE cfg.cfgname = 'b36_cfg2' AND n.nspname = 'b36_s'"); got != "1" {
		t.Fatalf("post-restart renamed+moved config count = %q, want 1 (not reloaded / ALTER lost)", got)
	}
	// The two ADD MAPPING entries (asciiword, word → simple) round-tripped
	// through pg_ts_config_map.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_ts_config_map m JOIN pg_ts_config cfg ON cfg.oid = m.mapcfg WHERE cfg.cfgname = 'b36_cfg2'"); got != "2" {
		t.Fatalf("post-restart pg_ts_config_map count = %q, want 2 (mappings lost)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_ts_config WHERE cfgname IN ('b36_cfg', 'b36_gone')"); got != "0" {
		t.Fatalf("post-restart stale config names count = %q, want 0", got)
	}
}

// TestPort_AccessMethodSurvivesRestart pins B3.7's pg_am heap journaling:
// CREATE ACCESS METHOD and DROP both survive a restart via the pg_am heap
// seq-scan reload (kinds 70/71 retired).
func TestPort_AccessMethodSurvivesRestart(t *testing.T) {
	c, err := cluster.New("access-method-durability", cluster.Options{
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

	stmts := []string{
		"CREATE FUNCTION public.b37_am_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'b37_am_handler'",
		"CREATE ACCESS METHOD b37_am TYPE INDEX HANDLER b37_am_handler",
		"CREATE ACCESS METHOD b37_gone TYPE INDEX HANDLER b37_am_handler",
		"DROP ACCESS METHOD b37_gone",
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_am WHERE amname = 'b37_am' AND amtype = 'i'"); got != "1" {
		t.Fatalf("post-restart access method count = %q, want 1 (not reloaded)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_am WHERE amname = 'b37_gone'"); got != "0" {
		t.Fatalf("post-restart dropped access method count = %q, want 0", got)
	}
}

// TestPort_TablespaceSurvivesRestart validates B4.1: CREATE/DROP TABLESPACE
// journals a real pg_tablespace SHARED heap row (global/1213) + pg_shdepend
// owner dep + RM_TBLSPC record, and a restart reloads the registry from the
// heap (reloadUserTablespacesFromHeap) — no bespoke kind 124/125 record.
func TestPort_TablespaceSurvivesRestart(t *testing.T) {
	c, err := cluster.New("tablespace-durability", cluster.Options{
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

	// allow_in_place_tablespaces is a per-session GUC (BootVal off); keep the
	// SET in the same simple-query batch as the CREATEs so it applies.
	if err := runSQLSimple(t, c,
		"SET allow_in_place_tablespaces = on;"+
			"CREATE TABLESPACE b41_ts LOCATION '';"+
			"CREATE TABLESPACE b41_gone LOCATION '';"+
			"DROP TABLESPACE b41_gone"); err != nil {
		t.Fatalf("create/drop tablespace: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Surviving tablespace present, with the resolved owner OID (superuser 10),
	// after reload from the pg_tablespace heap.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_tablespace WHERE spcname = 'b41_ts' AND spcowner = 10"); got != "1" {
		t.Fatalf("post-restart tablespace count = %q, want 1 (reloaded from heap)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_tablespace WHERE spcname = 'b41_gone'"); got != "0" {
		t.Fatalf("post-restart dropped tablespace count = %q, want 0", got)
	}
}

// TestPort_DbRoleSettingSurvivesRestart validates B4.2: ALTER DATABASE/ROLE
// SET/RESET journals a real pg_db_role_setting SHARED heap row (global/2964),
// and a restart reloads the overrides from it (reloadDbRoleSettingsFromHeap) —
// no bespoke kind 73-78 record.
func TestPort_DbRoleSettingSurvivesRestart(t *testing.T) {
	c, err := cluster.New("dbrolesetting-durability", cluster.Options{
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

	stmts := []string{
		"ALTER DATABASE postgres SET search_path TO b42_dbval",
		"ALTER ROLE postgres SET statement_timeout TO 12345",
		"ALTER ROLE postgres SET lock_timeout TO 6789",
		"ALTER ROLE postgres RESET lock_timeout", // exercise the delete-entry path
	}
	for _, s := range stmts {
		if err := runSQLSimple(t, c, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Surviving overrides present after reload from the heap.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_db_role_setting WHERE setconfig::text LIKE '%search_path=b42_dbval%'"); got != "1" {
		t.Fatalf("post-restart database config count = %q, want 1 (reloaded from heap)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_db_role_setting WHERE setconfig::text LIKE '%statement_timeout=12345%'"); got != "1" {
		t.Fatalf("post-restart role config count = %q, want 1 (reloaded from heap)", got)
	}
	// The RESET lock_timeout entry must be gone.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_db_role_setting WHERE setconfig::text LIKE '%lock_timeout%'"); got != "0" {
		t.Fatalf("post-restart reset config count = %q, want 0 (entry removed)", got)
	}
}
