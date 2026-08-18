package testport

// TestPort_AddPrimaryKeyBare... and its siblings are the guards for
// M0134-0005o: `ALTER TABLE <t> ADD [CONSTRAINT n] PRIMARY KEY (cols)` must
// (a) reach the on-disk pg_attribute HEAP so `\d+`/pg_dump see attnotnull —
// not just the in-memory catalog — and (b) cascade the synthesized NOT NULL
// to already-existing inheritance children, mirroring PG's
// ATPrepAddPrimaryKey re-entering ATPrepCmd with recurse=true for the
// synthesized AT_AddConstraint (postgres/src/backend/commands/tablecmds.c:9499,
// same find_all_inheritors cascade as ATExecSetNotNull, :8062-8079). Before
// the fix, the in-memory flip happened but pg_attribute.attnotnull never
// reached the heap, and pre-existing children never got the constraint at
// all (M0134-0005i's cascadeNotNullToChildren was wired for SET NOT NULL /
// ADD NOT NULL only, not for ADD PRIMARY KEY).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func startAddPKNotNullCluster(t *testing.T, name string) *cluster.Cluster {
	t.Helper()
	c, err := cluster.New(name, cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	t.Cleanup(func() { _ = c.Stop(cluster.ShutdownImmediate) })
	return c
}

// attnotnullOf reads pg_attribute.attnotnull for tbl.col via the regclass
// cast, the same catalog pg_dump's getTableAttrs query reads.
func attnotnullOf(t *testing.T, c *cluster.Cluster, tbl, col string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := c.Query(ctx,
		"SELECT attnotnull FROM pg_attribute WHERE attrelid = '"+tbl+"'::regclass AND attname = '"+col+"'")
	if err != nil {
		t.Fatalf("query pg_attribute for %s.%s: %v", tbl, col, err)
	}
	if len(rows) != 1 {
		t.Fatalf("pg_attribute rows for %s.%s = %d, want 1", tbl, col, len(rows))
	}
	return rows[0][0]
}

// TestPort_AddPrimaryKeyBareSyncsAttnotnullToHeap is acceptance criterion 1:
// the bare `ADD PRIMARY KEY (a)` spelling must flip the pg_attribute HEAP
// row's attnotnull, not merely the in-memory catalog.Column. Pre-fix,
// execAlterTableAddPrimaryKey never called syncTableToCatalogHeap, so this
// query reported 'f' even though \d+ (which also reads the heap) would too.
func TestPort_AddPrimaryKeyBareSyncsAttnotnullToHeap(t *testing.T) {
	c := startAddPKNotNullCluster(t, "addpk-notnull-bare-heap")

	for _, stmt := range []string{
		"CREATE TABLE pk_bare (a int, b int)",
		"ALTER TABLE pk_bare ADD PRIMARY KEY (a)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := attnotnullOf(t, c, "pk_bare", "a"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for pk_bare.a = %q, want true (bare ADD PRIMARY KEY did not reach the heap)", got)
	}
}

// TestPort_AddConstraintPrimaryKeySyncsAttnotnullToHeap is acceptance
// criterion 2: the same check for the named spelling `ADD CONSTRAINT n
// PRIMARY KEY (a)` — both dispatch through the same parser.AlterTableAddPrimaryKey
// arm, but this test proves the named form is not accidentally routed
// elsewhere.
func TestPort_AddConstraintPrimaryKeySyncsAttnotnullToHeap(t *testing.T) {
	c := startAddPKNotNullCluster(t, "addpk-notnull-named-heap")

	for _, stmt := range []string{
		"CREATE TABLE pk_named (a int, b int)",
		"ALTER TABLE pk_named ADD CONSTRAINT pk_named_pkey PRIMARY KEY (a)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := attnotnullOf(t, c, "pk_named", "a"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for pk_named.a = %q, want true (named ADD CONSTRAINT ... PRIMARY KEY did not reach the heap)", got)
	}
}

// TestPort_AddPrimaryKeyCascadesNotNullToExistingChild is acceptance
// criterion 3: a child table created via INHERITS BEFORE the parent's ADD
// PRIMARY KEY must end up with the same not-null state on the inherited
// column as a child created after — i.e. the cascade reaches an
// already-existing child. Pre-fix, execAlterTableAddPrimaryKey never called
// cascadeNotNullToChildren, so INSERT of NULL into the child succeeded and
// the child's own pg_attribute.attnotnull stayed false.
func TestPort_AddPrimaryKeyCascadesNotNullToExistingChild(t *testing.T) {
	c := startAddPKNotNullCluster(t, "addpk-notnull-cascade-child")

	for _, stmt := range []string{
		"CREATE TABLE pk_parent (a int, b int)",
		"CREATE TABLE pk_child () INHERITS (pk_parent)",
		"ALTER TABLE pk_parent ADD PRIMARY KEY (a)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := attnotnullOf(t, c, "pk_child", "a"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for pk_child.a = %q, want true (ADD PRIMARY KEY on the parent did not cascade to the pre-existing child)", got)
	}

	err := runSQLSimple(t, c, "INSERT INTO pk_child (a) VALUES (NULL)")
	if err == nil {
		t.Fatal("INSERT INTO pk_child (a) VALUES (NULL) succeeded after parent ADD PRIMARY KEY; " +
			"want 23502 (not-null violation) — the cascade did not reach the pre-existing child")
	}
	if !isNotNullViolation(err) {
		t.Fatalf("INSERT INTO pk_child (a) VALUES (NULL) error = %v, want SQLSTATE 23502", err)
	}
}
