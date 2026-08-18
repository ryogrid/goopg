package testport

// TestPort_CreateTable*SyncsAttnotnullToHeap and its siblings are the guards
// for M0134-0005y: `execCreateTable` (internal/executor/operators_ddl.go)
// calls syncTableToCatalogHeap exactly ONCE, before any constraint-processing
// arm runs. The named table-level PK, anonymous/inline-column PK, and
// INHERITS/LIKE NOT-NULL-merge blocks all flip col.NotNull / call
// tbl.AddNotNull AFTER that single sync, so pg_attribute.attnotnull never
// reached the heap even though the "Not-null constraints:" footer (driven by
// tbl.NotNullConstraints, not the heap) was already correct — pg_class is
// virtual but pg_attribute is heap-backed
// (goopg_pg_class_virtual_pg_attribute_heap project fact), so \d+/pg_dump
// read the stale heap row. Fixture drawn from
// postgres/src/test/regress/sql/constraints.sql:744-748.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func startCreateTablePKNotNullCluster(t *testing.T, name string) *cluster.Cluster {
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

// TestPort_CreateTableNamedPrimaryKeySyncsAttnotnullToHeap is acceptance
// criterion 1: `CREATE TABLE cnn_pk (a int, b int, CONSTRAINT cnn_primarykey
// PRIMARY KEY (b))` must flip pg_attribute.attnotnull for b, not just the
// in-memory catalog.Column. Pre-fix, the named-constraint PK block
// (operators_ddl.go ~:3634-3642 at HEAD 2ee3a987) set col.NotNull=true after
// the single early sync, so this query reported 'f'.
func TestPort_CreateTableNamedPrimaryKeySyncsAttnotnullToHeap(t *testing.T) {
	c := startCreateTablePKNotNullCluster(t, "createtable-notnull-named-heap")

	if err := runSQLSimple(t, c,
		"CREATE TABLE cnn_pk (a int, b int, CONSTRAINT cnn_primarykey PRIMARY KEY (b))"); err != nil {
		t.Fatalf("CREATE TABLE cnn_pk: %v", err)
	}

	if got := attnotnullOf(t, c, "cnn_pk", "b"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for cnn_pk.b = %q, want true (named table-level PRIMARY KEY did not reach the heap)", got)
	}
	if got := attnotnullOf(t, c, "cnn_pk", "a"); got != "false" {
		t.Fatalf("pg_attribute.attnotnull for cnn_pk.a = %q, want false (non-PK column must stay nullable)", got)
	}
}

// TestPort_CreateTableInheritsChildSyncsAttnotnullToHeap is acceptance
// criterion 2: the INHERITS child of the same fixture
// (postgres/src/test/regress/sql/constraints.sql:744-748) must also see the
// inherited PK column's attnotnull flip — the INHERITS/LIKE NOT-NULL-merge
// block (tbl.AddNotNull) is downstream of the same single early sync inside
// the SAME execCreateTable call.
func TestPort_CreateTableInheritsChildSyncsAttnotnullToHeap(t *testing.T) {
	c := startCreateTablePKNotNullCluster(t, "createtable-notnull-inherits-child-heap")

	for _, stmt := range []string{
		"CREATE TABLE cnn_pk (a int, b int, CONSTRAINT cnn_primarykey PRIMARY KEY (b))",
		"CREATE TABLE cnn_pk_child () INHERITS (cnn_pk)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := attnotnullOf(t, c, "cnn_pk_child", "b"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for cnn_pk_child.b = %q, want true (INHERITS child did not inherit the heap-level NOT NULL)", got)
	}
}

// TestPort_CreateTableAnonymousTableLevelPrimaryKeySyncsAttnotnullToHeap is
// acceptance criterion 3, the twin of the named-constraint form: `CREATE
// TABLE t (a int, b int, PRIMARY KEY (b))` (no CONSTRAINT name) funnels
// through the SEPARATE anonymous/inline pkCols block (operators_ddl.go
// ~:3653-3703 at HEAD 2ee3a987), which is its own mutation site distinct
// from the named-constraint one above. A green result on the named form
// proves nothing about this one (Rule #2 twin). NOTE: the inline-column
// spelling `b int PRIMARY KEY` is NOT a twin of this bug — the parser
// (internal/parser/ddl.go, both the bare and `CONSTRAINT name`
// inline-column PRIMARY KEY arms) sets col.NotNull=true directly on the
// ColumnDef at parse time, so that form's NotNull is already present when
// execCreateTable builds the column BEFORE the single early sync and was
// never affected by this bug — verified by a stashed-fix run that still
// passed for that spelling.
func TestPort_CreateTableAnonymousTableLevelPrimaryKeySyncsAttnotnullToHeap(t *testing.T) {
	c := startCreateTablePKNotNullCluster(t, "createtable-notnull-anon-tablelevel-heap")

	if err := runSQLSimple(t, c, "CREATE TABLE cnn_pk_anon (a int, b int, PRIMARY KEY (b))"); err != nil {
		t.Fatalf("CREATE TABLE cnn_pk_anon: %v", err)
	}

	if got := attnotnullOf(t, c, "cnn_pk_anon", "b"); got != "true" {
		t.Fatalf("pg_attribute.attnotnull for cnn_pk_anon.b = %q, want true (anonymous table-level PRIMARY KEY did not reach the heap)", got)
	}
}

// TestPort_CreateTableNoConstraintsDoesNotDuplicateColumns is acceptance
// criterion 4 (the notNullHeapDirty=false path): a table with NO PK/NOT NULL
// constraints must not trigger the delete-then-resync quadruple at all, and
// must list each column exactly once in pg_attribute either way. This guards
// against a regression where the resync is made unconditional (which would
// still be correct IF delete-then-resync is used, but the brief requires the
// no-mutation path to skip the resync entirely).
func TestPort_CreateTableNoConstraintsDoesNotDuplicateColumns(t *testing.T) {
	c := startCreateTablePKNotNullCluster(t, "createtable-notnull-plain-no-dup")

	if err := runSQLSimple(t, c, "CREATE TABLE cnn_plain (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE cnn_plain: %v", err)
	}

	if got := queryScalar(t, c, "SELECT count(*) FROM pg_attribute WHERE attrelid = 'cnn_plain'::regclass AND attnum > 0"); got != "2" {
		t.Fatalf("pg_attribute row count for cnn_plain = %s, want 2 (no duplicate rows on the notNullHeapDirty=false path)", got)
	}
}

// TestPort_CreateTableInlinePrimaryKeyDoesNotDuplicateColumns is acceptance
// criterion 4 for the notNullHeapDirty=true path: syncTableToCatalogHeap is
// append-only (writeHeapRowCanonical), so the added resync MUST run the
// delete-old-then-resync pattern (deleteCatalogRowsForOID stamps xmax on the
// stale rows first) — a bare second sync would leave duplicate live
// pg_attribute rows for every column, not just the PK one.
func TestPort_CreateTableInlinePrimaryKeyDoesNotDuplicateColumns(t *testing.T) {
	c := startCreateTablePKNotNullCluster(t, "createtable-notnull-inline-no-dup")

	if err := runSQLSimple(t, c, "CREATE TABLE cnn_pk_nodup (a int, b int, PRIMARY KEY (b))"); err != nil {
		t.Fatalf("CREATE TABLE cnn_pk_nodup: %v", err)
	}

	if got := queryScalar(t, c, "SELECT count(*) FROM pg_attribute WHERE attrelid = 'cnn_pk_nodup'::regclass AND attnum > 0"); got != "2" {
		t.Fatalf("pg_attribute row count for cnn_pk_nodup = %s, want 2 (delete-then-resync must not leave duplicate live rows)", got)
	}
}
