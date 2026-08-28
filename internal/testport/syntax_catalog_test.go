package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_Catalog_PgClass exercises pg_catalog.pg_class.
func TestSyntax_Catalog_PgClass(t *testing.T) {
	c := newCluster(t, "syntax_cat_class")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE cat_t (id int)")
	rows := runSQL(t, c, "SELECT relname FROM pg_catalog.pg_class WHERE relname = 'cat_t'")
	if len(rows) != 1 || rows[0][0] != "cat_t" {
		t.Fatalf("pg_class lookup = %v, want [[cat_t]]", rows)
	}
	runSQL(t, c, "DROP TABLE cat_t")
}

// TestSyntax_Catalog_PgProc exercises pg_catalog.pg_proc.
func TestSyntax_Catalog_PgProc(t *testing.T) {
	c := newCluster(t, "syntax_cat_proc")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE FUNCTION cat_f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$")
	rows := runSQL(t, c, "SELECT proname FROM pg_catalog.pg_proc WHERE proname = 'cat_f'")
	if len(rows) != 1 || rows[0][0] != "cat_f" {
		t.Fatalf("pg_proc lookup = %v, want [[cat_f]]", rows)
	}
	runSQL(t, c, "DROP FUNCTION cat_f()")
}

// TestSyntax_Catalog_PgStatActivity exercises pg_catalog.pg_stat_activity.
func TestSyntax_Catalog_PgStatActivity(t *testing.T) {
	c := newCluster(t, "syntax_cat_act")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, "SELECT pid, state, backend_type FROM pg_catalog.pg_stat_activity WHERE backend_type = 'client backend'")
	if len(rows) < 1 {
		t.Fatal("pg_stat_activity returned zero rows for client_backend")
	}
	if rows[0][0] == "" {
		t.Error("pid is empty")
	}
	if rows[0][1] == "" {
		t.Error("state is empty")
	}
	if rows[0][2] != "client_backend" {
		t.Errorf("backend_type = %q, want upstream literal %q", rows[0][2], "client backend")
	}
}

// TestSyntax_Catalog_PgTables exercises pg_catalog.pg_tables.
func TestSyntax_Catalog_PgTables(t *testing.T) {
	c := newCluster(t, "syntax_cat_tbls")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE cat_t2 (id int)")
	rows := runSQL(t, c, "SELECT tablename FROM pg_catalog.pg_tables WHERE tablename = 'cat_t2'")
	if len(rows) != 1 || rows[0][0] != "cat_t2" {
		t.Fatalf("pg_tables lookup = %v, want [[cat_t2]]", rows)
	}
	runSQL(t, c, "DROP TABLE cat_t2")
}

// TestSyntax_Catalog_PgIndexes exercises pg_catalog.pg_indexes.
func TestSyntax_Catalog_PgIndexes(t *testing.T) {
	c := newCluster(t, "syntax_cat_idx")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE cat_t3 (id int)")
	runSQL(t, c, "CREATE INDEX cat_i ON cat_t3 (id)")
	rows := runSQL(t, c, "SELECT indexname FROM pg_catalog.pg_indexes WHERE indexname = 'cat_i'")
	if len(rows) != 1 || rows[0][0] != "cat_i" {
		t.Fatalf("pg_indexes lookup = %v, want [[cat_i]]", rows)
	}
	runSQL(t, c, "DROP TABLE cat_t3")
}

// TestSyntax_Catalog_PgRoles exercises pg_catalog.pg_roles.
func TestSyntax_Catalog_PgRoles(t *testing.T) {
	c := newCluster(t, "syntax_cat_roles")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, "SELECT rolname FROM pg_catalog.pg_roles")
	if len(rows) == 0 {
		t.Fatal("pg_roles returned zero rows")
	}
}
