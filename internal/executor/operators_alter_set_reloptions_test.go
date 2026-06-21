package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// reloptCell returns the pg_class.reloptions text[] literal (column index 32)
// for the named relation, or "" when the option array is empty (SQL NULL).
func reloptCell(t *testing.T, cat catalog.Catalog, rel string) string {
	t.Helper()
	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && r[1] == rel {
			return r[32]
		}
	}
	t.Fatalf("pg_class row for %q not found", rel)
	return ""
}

// TestAlterTableSetReloptionsMatchesCreateWith is the sibling-path lock for the
// table-level reloptions feature: `ALTER TABLE … SET (param = value, …)` must
// land the exact same catalog state (and therefore the same pg_class.reloptions
// rendering pg_dump round-trips) as the equivalent `CREATE TABLE … WITH (…)`.
// The two paths share no code, so this test pins them together — a divergence
// (the project's most expensive failure mode) fails here. M0118-0001.
func TestAlterTableSetReloptionsMatchesCreateWith(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// Reference: options declared at CREATE time.
	if err := runDDL(t, ctx, `CREATE TABLE viacreate (id integer PRIMARY KEY) WITH (fillfactor=70, parallel_workers=4)`); err != nil {
		t.Fatalf("CREATE TABLE viacreate: %v", err)
	}
	// Subject: same options applied afterwards via ALTER TABLE SET.
	if err := runDDL(t, ctx, `CREATE TABLE viaalter (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE viaalter: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE viaalter SET (fillfactor=70, parallel_workers=4)`); err != nil {
		t.Fatalf("ALTER TABLE viaalter SET: %v", err)
	}

	create, _ := cat.LookupTable(parser.ObjectName{Name: "viacreate"})
	alter, _ := cat.LookupTable(parser.ObjectName{Name: "viaalter"})
	if create.Fillfactor != alter.Fillfactor || alter.Fillfactor != 70 {
		t.Errorf("Fillfactor: create=%d alter=%d, want both 70", create.Fillfactor, alter.Fillfactor)
	}
	if create.ParallelWorkers != alter.ParallelWorkers || !alter.ParallelWorkersSet || alter.ParallelWorkers != 4 {
		t.Errorf("ParallelWorkers: create=%d(set=%v) alter=%d(set=%v), want both 4/true",
			create.ParallelWorkers, create.ParallelWorkersSet, alter.ParallelWorkers, alter.ParallelWorkersSet)
	}

	// The rendered pg_class.reloptions text[] literal must be byte-for-byte equal.
	cRel, aRel := reloptCell(t, cat, "viacreate"), reloptCell(t, cat, "viaalter")
	if cRel != aRel {
		t.Errorf("pg_class.reloptions diverged: create=%q alter=%q", cRel, aRel)
	}
	if aRel != "{fillfactor=70,parallel_workers=4}" {
		t.Errorf("ALTER reloptions = %q, want %q", aRel, "{fillfactor=70,parallel_workers=4}")
	}
}

// TestAlterTableSetReloptionsMerge verifies that ALTER TABLE SET only touches the
// named options, preserving any previously-set storage parameters (PG merges
// rather than replaces), and that RESET clears just the named ones. M0118-0001.
func TestAlterTableSetReloptionsMerge(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE m (id integer PRIMARY KEY) WITH (fillfactor=60)`); err != nil {
		t.Fatalf("CREATE TABLE m: %v", err)
	}
	// SET a different option — fillfactor must survive the merge.
	if err := runDDL(t, ctx, `ALTER TABLE m SET (parallel_workers=2)`); err != nil {
		t.Fatalf("ALTER TABLE m SET parallel_workers: %v", err)
	}
	if got := reloptCell(t, cat, "m"); got != "{fillfactor=60,parallel_workers=2}" {
		t.Errorf("after merge SET, reloptions = %q, want %q", got, "{fillfactor=60,parallel_workers=2}")
	}
	// RESET one option — the other must survive.
	if err := runDDL(t, ctx, `ALTER TABLE m RESET (parallel_workers)`); err != nil {
		t.Fatalf("ALTER TABLE m RESET parallel_workers: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "m"})
	if tbl.ParallelWorkersSet {
		t.Errorf("RESET (parallel_workers) left ParallelWorkersSet=true")
	}
	if got := reloptCell(t, cat, "m"); got != "{fillfactor=60}" {
		t.Errorf("after RESET, reloptions = %q, want %q", got, "{fillfactor=60}")
	}
}

// TestAlterTableSetReloptionsBounds verifies that ALTER TABLE SET enforces the
// same value bounds and SQLSTATEs as CREATE TABLE WITH, and silently ignores an
// unrecognized (lowercase) option name (matching the CREATE path). M0118-0001.
func TestAlterTableSetReloptionsBounds(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE b (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE b: %v", err)
	}
	// Out-of-range parallel_workers → 22023, same as CREATE.
	err := runDDL(t, ctx, `ALTER TABLE b SET (parallel_workers=2000)`)
	if err == nil {
		t.Fatal("expected out-of-bounds error for parallel_workers=2000, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22023" {
		t.Errorf("parallel_workers=2000: error = %v, want *ExecError 22023", err)
	}
	// An unrecognized lowercase option is accepted and ignored (CREATE parity);
	// it must not error and must not appear in reloptions.
	if err := runDDL(t, ctx, `ALTER TABLE b SET (no_such_option=1)`); err != nil {
		t.Errorf("ALTER TABLE b SET (no_such_option=1): unexpected error %v", err)
	}
	if got := reloptCell(t, cat, "b"); got != "" {
		t.Errorf("reloptions after ignored option = %q, want \"\" (NULL)", got)
	}
}

// TestAlterTableSetReloptionsAtomic verifies that a multi-option SET in which one
// value is out of range applies NONE of the options — matching PG's statement
// atomicity. A field-by-field apply over Go's unordered map would otherwise leave
// a nondeterministic partial state on the rejected statement. M0118-0001.
func TestAlterTableSetReloptionsAtomic(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE a (id integer PRIMARY KEY) WITH (fillfactor=60)`); err != nil {
		t.Fatalf("CREATE TABLE a: %v", err)
	}
	// fillfactor=90 is valid but parallel_workers=2000 is out of range; the whole
	// statement must be rejected and the table left at its prior fillfactor=60.
	err := runDDL(t, ctx, `ALTER TABLE a SET (fillfactor=90, parallel_workers=2000)`)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22023" {
		t.Fatalf("expected *ExecError 22023, got %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "a"})
	if tbl.Fillfactor != 60 {
		t.Errorf("rejected SET partially applied: Fillfactor=%d, want 60 (unchanged)", tbl.Fillfactor)
	}
	if tbl.ParallelWorkersSet {
		t.Errorf("rejected SET partially applied: ParallelWorkersSet=true, want false")
	}
	if got := reloptCell(t, cat, "a"); got != "{fillfactor=60}" {
		t.Errorf("reloptions after rejected SET = %q, want %q (unchanged)", got, "{fillfactor=60}")
	}
}
