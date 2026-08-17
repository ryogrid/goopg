package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// alterTableOfTypeTable returns the live catalog.Table for a fixture relation,
// so a test can assert on the OfTypeOID (pg_class.reloftype) mutation.
func alterTableOfTypeTable(t *testing.T, cat catalog.Catalog, name string) *catalog.Table {
	t.Helper()
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
	if !ok {
		t.Fatalf("relation %q not found", name)
	}
	return tbl
}

// wantAlterError asserts that err is an *ExecError carrying the exact SQLSTATE
// and byte-exact message (mirrors the sibling alter-* tests' style).
func wantAlterError(t *testing.T, err error, code, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected *ExecError %s %q, got nil", code, message)
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != code {
		t.Errorf("Code=%q want %q (err=%v)", ee.Code, code, err)
	}
	if ee.Message != message {
		t.Errorf("Message=%q want %q", ee.Message, message)
	}
}

// TestAlterTableOfNotOfRegressMatrix pins the full `ALTER TABLE ... OF / NOT OF`
// validation matrix from postgres/src/test/regress/sql/alter_table.sql:2062-2086,
// matching PG 18.3's messages and SQLSTATEs exactly (ATExecAddOf / ATExecDropOf,
// postgres/src/backend/commands/tablecmds.c:18216-18390). Every error below is
// the byte-exact string PG emits; a false "different type" on tt0 (i.e. the
// canonical Type derivation disagreeing with CREATE) fails here first.
// M0134-0002 C2 slice 11.
func TestAlterTableOfNotOfRegressMatrix(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// Setup block straight from the regress script.
	for _, s := range []string{
		`CREATE TYPE tt_t0 AS (z inet, x int, y numeric(8,2))`,
		`ALTER TYPE tt_t0 DROP ATTRIBUTE z`,
		`CREATE TABLE tt0 (x int NOT NULL, y numeric(8,2))`,  // OK
		`CREATE TABLE tt1 (x int, y bigint)`,                 // wrong base type
		`CREATE TABLE tt2 (x int, y numeric(9,2))`,           // wrong typmod
		`CREATE TABLE tt3 (y numeric(8,2), x int)`,           // wrong column order
		`CREATE TABLE tt4 (x int)`,                           // too few columns
		`CREATE TABLE tt5 (x int, y numeric(8,2), z int)`,    // extra column
		`CREATE TABLE tt6 () INHERITS (tt0)`,                 // can't have a parent
		`CREATE TABLE tt7 (x int, q text, y numeric(8,2))`,
		`ALTER TABLE tt7 DROP q`, // OK
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	// tt0 — OF tt_t0 must succeed (attnotnull need not match), stamping
	// OfTypeOID and leaving tt0.x NOT NULL untouched.
	if err := runDDL(t, ctx, `ALTER TABLE tt0 OF tt_t0`); err != nil {
		t.Fatalf("tt0 OF tt_t0: %v", err)
	}
	tt0 := alterTableOfTypeTable(t, cat, "tt0")
	if tt0.OfTypeOID == 0 {
		t.Errorf("tt0.OfTypeOID = 0 after OF tt_t0, want the composite type OID")
	}
	if !tt0.Columns[0].NotNull {
		t.Errorf("tt0.x NotNull was cleared by ALTER TABLE OF; want untouched")
	}

	// tt1 — wrong base type (bigint vs numeric(8,2)).
	err := runDDL(t, ctx, `ALTER TABLE tt1 OF tt_t0`)
	wantAlterError(t, err, "42804", `table "tt1" has different type for column "y"`)

	// tt2 — wrong typmod (numeric(9,2) vs numeric(8,2)).
	err = runDDL(t, ctx, `ALTER TABLE tt2 OF tt_t0`)
	wantAlterError(t, err, "42804", `table "tt2" has different type for column "y"`)

	// tt3 — wrong column order.
	err = runDDL(t, ctx, `ALTER TABLE tt3 OF tt_t0`)
	wantAlterError(t, err, "42804", `table has column "y" where type requires "x"`)

	// tt4 — too few columns (missing y).
	err = runDDL(t, ctx, `ALTER TABLE tt4 OF tt_t0`)
	wantAlterError(t, err, "42804", `table is missing column "y"`)

	// tt5 — leftover non-dropped column z.
	err = runDDL(t, ctx, `ALTER TABLE tt5 OF tt_t0`)
	wantAlterError(t, err, "42804", `table has extra column "z"`)

	// tt6 — has an INHERITS parent → 42809.
	err = runDDL(t, ctx, `ALTER TABLE tt6 OF tt_t0`)
	wantAlterError(t, err, "42809", `typed tables cannot inherit`)
}

// TestAlterTableOfReassignAndNotOf covers the reassign path (re-tag an
// already-typed table with a new type) and `NOT OF`: clearing reloftype restores
// the plain-table state (OfTypeOID back to 0), and NOT OF on a never-typed table
// is 42809 `"%s" is not a typed table`. ATExecDropOf, tablecmds.c:18358-18390.
func TestAlterTableOfReassignAndNotOf(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		`CREATE TYPE tt_t0 AS (z inet, x int, y numeric(8,2))`,
		`ALTER TYPE tt_t0 DROP ATTRIBUTE z`,
		`CREATE TABLE tt7 (x int, q text, y numeric(8,2))`,
		`ALTER TABLE tt7 DROP q`, // OK
		`CREATE TYPE tt_t1 AS (x int, y numeric(8,2))`,
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	// OF tt_t0 passes (q is dropped on the table side; z dropped on the type
	// side), then reassign to tt_t1.
	if err := runDDL(t, ctx, `ALTER TABLE tt7 OF tt_t0`); err != nil {
		t.Fatalf("tt7 OF tt_t0: %v", err)
	}
	if got := alterTableOfTypeTable(t, cat, "tt7").OfTypeOID; got == 0 {
		t.Errorf("tt7.OfTypeOID = 0 after OF tt_t0, want non-zero")
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt7 OF tt_t1`); err != nil {
		t.Fatalf("tt7 OF tt_t1 (reassign): %v", err)
	}

	// NOT OF clears reloftype back to 0.
	if err := runDDL(t, ctx, `ALTER TABLE tt7 NOT OF`); err != nil {
		t.Fatalf("tt7 NOT OF: %v", err)
	}
	if got := alterTableOfTypeTable(t, cat, "tt7").OfTypeOID; got != 0 {
		t.Errorf("tt7.OfTypeOID = %d after NOT OF, want 0", got)
	}

	// NOT OF on the now-plain (never-typed) table → 42809.
	err := runDDL(t, ctx, `ALTER TABLE tt7 NOT OF`)
	wantAlterError(t, err, "42809", `"tt7" is not a typed table`)

	// A never-typed plain table is likewise not a typed table.
	if err := runDDL(t, ctx, `CREATE TABLE plain (a int)`); err != nil {
		t.Fatalf("CREATE TABLE plain: %v", err)
	}
	err = runDDL(t, ctx, `ALTER TABLE plain NOT OF`)
	wantAlterError(t, err, "42809", `"plain" is not a typed table`)
}
