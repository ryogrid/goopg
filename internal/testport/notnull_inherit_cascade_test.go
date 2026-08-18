package testport

// TestPort_NotNullSetNotNullCascadesToChildren and its siblings are the
// guards for M0134-0005i: `ALTER TABLE <parent> ALTER COLUMN <c> SET NOT
// NULL` and `ALTER TABLE <parent> ADD CONSTRAINT <name> NOT NULL (<c>)` must
// propagate the NOT NULL constraint to already-existing inheritance children
// and partitions, recursively, with PG 18.3's merge-not-duplicate accounting
// (postgres/src/backend/commands/tablecmds.c:7913 ATExecSetNotNull,
// :8062-8079 recursive cascade, :7971-7993 merge). Before the fix, both
// handlers mutated only the table named in the ALTER, so children silently
// kept accepting NULLs.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// isNotNullViolation reports whether err is a NOT-NULL constraint violation
// (SQLSTATE 23502).
func isNotNullViolation(err error) bool {
	if err == nil {
		return false
	}
	if pe, ok := err.(*pq.Error); ok {
		return pe.Code == "23502"
	}
	return false
}

func startNotNullCascadeCluster(t *testing.T, name string) *cluster.Cluster {
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

// TestPort_NotNullSetNotNullCascadesToChildren is acceptance criterion 1: SET
// NOT NULL on a parent with a pre-existing child causes an INSERT of NULL
// into the child to be rejected with 23502. Pre-fix, this INSERT succeeds —
// a silent integrity gap.
func TestPort_NotNullSetNotNullCascadesToChildren(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-cascade-setnn")

	for _, stmt := range []string{
		"CREATE TABLE p (a int, b int)",
		"CREATE TABLE ch () INHERITS (p)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if err := runSQLSimple(t, c, "ALTER TABLE p ALTER COLUMN a SET NOT NULL"); err != nil {
		t.Fatalf("ALTER TABLE p ALTER COLUMN a SET NOT NULL: %v", err)
	}

	err := runSQLSimple(t, c, "INSERT INTO ch (a) VALUES (NULL)")
	if err == nil {
		t.Fatal("INSERT INTO ch (a) VALUES (NULL) succeeded after parent SET NOT NULL; " +
			"want 23502 (not-null violation) — the cascade did not reach the child")
	}
	if !isNotNullViolation(err) {
		t.Fatalf("INSERT INTO ch (a) VALUES (NULL) error = %v, want SQLSTATE 23502", err)
	}
}

// TestPort_NotNullAddConstraintCascadesUnderParentName is acceptance
// criterion 2: the named form `ADD CONSTRAINT p_a_nn NOT NULL a` produces a
// constraint row on the child under the PARENT's name, conislocal='f',
// coninhcount=1; COMMENT ON CONSTRAINT p_a_nn ON c then succeeds (it errors
// "does not exist" pre-fix).
func TestPort_NotNullAddConstraintCascadesUnderParentName(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-cascade-addnn")

	for _, stmt := range []string{
		"CREATE TABLE p (a int, b int)",
		"CREATE TABLE ch () INHERITS (p)",
		"ALTER TABLE p ADD CONSTRAINT p_a_nn NOT NULL a",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := c.Query(ctx,
		"SELECT conislocal, coninhcount FROM pg_constraint WHERE conname = 'p_a_nn' AND conrelid = 'ch'::regclass")
	if err != nil {
		t.Fatalf("query pg_constraint for p_a_nn on ch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pg_constraint rows for p_a_nn on ch = %d, want 1 (child did not inherit the parent's named constraint)", len(rows))
	}
	if got := rows[0][0]; got != "false" {
		t.Fatalf("ch's p_a_nn conislocal = %q, want false", got)
	}
	if got := rows[0][1]; got != "1" {
		t.Fatalf("ch's p_a_nn coninhcount = %q, want 1", got)
	}

	if err := runSQLSimple(t, c, "COMMENT ON CONSTRAINT p_a_nn ON ch IS 'x'"); err != nil {
		t.Fatalf("COMMENT ON CONSTRAINT p_a_nn ON ch: %v (pre-fix: \"constraint \\\"p_a_nn\\\" for table \\\"ch\\\" does not exist\")", err)
	}
}

// TestPort_NotNullCascadesMultiLevel is acceptance criterion 3: a grandchild
// (INHERITS the direct child) also gets the constraint, proving the walk
// recurses rather than stopping at one level.
func TestPort_NotNullCascadesMultiLevel(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-cascade-multilevel")

	for _, stmt := range []string{
		"CREATE TABLE p (a int, b int)",
		"CREATE TABLE ch () INHERITS (p)",
		"CREATE TABLE gc () INHERITS (ch)",
		"ALTER TABLE p ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runSQLSimple(t, c, "INSERT INTO gc (a) VALUES (NULL)")
	if err == nil {
		t.Fatal("INSERT INTO gc (a) VALUES (NULL) succeeded; want 23502 — the cascade did not reach the grandchild")
	}
	if !isNotNullViolation(err) {
		t.Fatalf("INSERT INTO gc (a) VALUES (NULL) error = %v, want SQLSTATE 23502", err)
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_constraint WHERE contype = 'n' AND conrelid = 'gc'::regclass"); got != "1" {
		t.Fatalf("pg_constraint NOT NULL rows on gc = %q, want 1", got)
	}
}

// TestPort_NotNullCascadeMergesExistingChildConstraint is acceptance
// criterion 4: if the child already declared `a NOT NULL` itself, the
// cascade must increment coninhcount rather than creating a duplicate row.
func TestPort_NotNullCascadeMergesExistingChildConstraint(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-cascade-merge")

	for _, stmt := range []string{
		"CREATE TABLE p (a int, b int)",
		"CREATE TABLE ch (a int NOT NULL) INHERITS (p)",
		"ALTER TABLE p ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ch'::regclass"); got != "1" {
		t.Fatalf("pg_constraint NOT NULL rows on ch = %q, want exactly 1 (merge must not duplicate — ch declares only column a)", got)
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ch'::regclass"); got != "1" {
		t.Fatalf("ch's merged NOT NULL constraint coninhcount = %q, want 1 (incremented, not duplicated)", got)
	}
}

// TestPort_NotNullCascadeSkipsUnrelatedSibling is acceptance criterion 5: an
// unrelated sibling table that is NOT a child of p is unaffected by the
// cascade.
func TestPort_NotNullCascadeSkipsUnrelatedSibling(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-cascade-sibling")

	for _, stmt := range []string{
		"CREATE TABLE p (a int, b int)",
		"CREATE TABLE ch () INHERITS (p)",
		"CREATE TABLE sib (a int, b int)",
		"ALTER TABLE p ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runSQLSimple(t, c, "INSERT INTO sib (a) VALUES (NULL)"); err != nil {
		t.Fatalf("INSERT INTO sib (a) VALUES (NULL) failed; sib is not a child of p and must be unaffected: %v", err)
	}
}
