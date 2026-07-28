package testport

// End-to-end coverage of table-inheritance restart persistence (root-0031).
//
// Before this, pg_inherits was a purely VIRTUAL catalog synthesized from the
// in-memory Table.InheritsParentOIDs / catalog.inheritanceChildren state, which
// only CREATE TABLE ... INHERITS ever populated. Nothing wrote the parent→child
// edge to disk and no reload pass rebuilt it, so a restart silently dropped
// every inheritance relationship: the parent stopped returning its children's
// rows, pg_inherits went empty, and the child name-collision recursion in
// ALTER TABLE ... RENAME COLUMN became a no-op (which let goopg create a table
// with two identically-named columns).
//
// That last consequence is how this surfaced: the nightly regress suite
// restarts its cluster after any case times out, and every later case that
// depends on the test_setup person/emp/student/stud_emp inheritance fixture
// then diverged from PG — reported as "output mismatch; normalization rules
// need extension" (AI-20260725-011243-011 and siblings). See
// docs/design/root-0031-pg-inherits-restart-persistence.md.
//
// The fix writes real pg_inherits heap rows (base/<dbOid>/2611) from
// syncTableToCatalogHeap and reloads them via loadInheritanceFromHeap, the same
// shape B5 Slice B gave pg_attrdef.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_InheritanceSurvivesRestart builds the upstream regress inheritance
// fixture (person ← emp/student ← stud_emp, the multi-parent shape from
// postgres/src/test/regress/sql/test_setup.sql), restarts, and asserts every
// observable consequence of the parent→child edge is still there.
func TestPort_InheritanceSurvivesRestart(t *testing.T) {
	c, err := cluster.New("pg-inherits-durability", cluster.Options{
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

	for _, stmt := range []string{
		"CREATE TABLE person (name text, age int4)",
		"CREATE TABLE emp (salary int4, manager name) INHERITS (person)",
		"CREATE TABLE student (gpa float8) INHERITS (person)",
		"CREATE TABLE stud_emp (percent int4) INHERITS (emp, student)",
		"CREATE TABLE inh_gone (x int) INHERITS (person)",
		"DROP TABLE inh_gone",
		"INSERT INTO person VALUES ('p', 10)",
		"INSERT INTO emp VALUES ('e', 20, 100, 'boss')",
		"INSERT INTO student VALUES ('s', 30, 3.5)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Pre-restart baseline: 4 live edges (emp→person, student→person,
	// stud_emp→emp seqno 1, stud_emp→student seqno 2) and an inheritance scan
	// of person that reaches all three descendants.
	assertInheritanceIntact := func(when string) {
		t.Helper()
		if got := queryScalar(t, c, "SELECT count(*) FROM pg_inherits"); got != "4" {
			t.Fatalf("%s pg_inherits row count = %q, want 4", when, got)
		}
		// inhseqno must follow the INHERITS (...) declaration order — pg_dump
		// re-emits the clause from it, so a reload that loses the order silently
		// rewrites the schema.
		if got := queryScalar(t, c,
			"SELECT inhseqno FROM pg_inherits WHERE inhrelid = 'stud_emp'::regclass AND inhparent = 'student'::regclass"); got != "2" {
			t.Fatalf("%s stud_emp→student inhseqno = %q, want 2", when, got)
		}
		// The parent scan expands to the children (person's own row + emp's +
		// student's); ONLY person sees just its own.
		if got := queryScalar(t, c, "SELECT count(*) FROM person"); got != "3" {
			t.Fatalf("%s count(*) FROM person = %q, want 3 (children not expanded)", when, got)
		}
		if got := queryScalar(t, c, "SELECT count(*) FROM ONLY person"); got != "1" {
			t.Fatalf("%s count(*) FROM ONLY person = %q, want 1", when, got)
		}
		// A dropped child leaves no edge behind (the DROP stamps xmax on its
		// pg_inherits rows).
		if got := queryScalar(t, c,
			"SELECT count(*) FROM pg_inherits WHERE inhrelid::regclass::text = 'inh_gone'"); got != "0" {
			t.Fatalf("%s dropped child still has a pg_inherits edge (%q)", when, got)
		}
	}
	assertInheritanceIntact("pre-restart")

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	assertInheritanceIntact("post-restart")

	// The DDL consequence that made this a correctness bug rather than a
	// display one: renaming emp.salary to a name the CHILD already carries must
	// still fail, naming the child, exactly as PG's renameatt_internal does
	// (it recurses into the children before checking the target relation).
	err = runSQLSimple(t, c, "ALTER TABLE emp RENAME COLUMN salary TO manager")
	if err == nil {
		t.Fatal("post-restart ALTER TABLE emp RENAME COLUMN salary TO manager succeeded; " +
			"PG raises 42701 (inheritance child collision) — the rename would create a duplicate column")
	}
	// And the message must match PG's, which names the CHILD relation.
	if want := `column "manager" of relation "stud_emp" already exists`; !strings.Contains(err.Error(), want) {
		t.Fatalf("post-restart rename error = %q, want it to contain %q", err.Error(), want)
	}
}
