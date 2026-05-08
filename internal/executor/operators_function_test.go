package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// runRoutineDDL is a thin helper specific to the function-DDL
// tests below — distinct from the existing `runDDL(ctx, sql)`
// helper in storage_ddl_test.go which seeds a Pool/Manager-backed
// Context. Routines live entirely in the catalog so a bare
// Context with Catalog set suffices.
func runRoutineDDL(t *testing.T, sql string, cat catalog.Catalog) error {
	t.Helper()
	plan := planOne(t, sql, cat)
	op, err := Build(plan)
	if err != nil {
		return err
	}
	ctx := NewContext()
	ctx.Catalog = cat
	if err := op.Open(ctx); err != nil {
		return err
	}
	defer op.Close()
	for {
		_, err := NextRow(op)
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TestExecCreateFunctionRegistersInCatalog pins the M0015 Stage A
// step 3 happy path: a CREATE FUNCTION statement flows
// parser → planner DDL → executor and lands a row in
// `cat.Routines()`.
func TestExecCreateFunctionRegistersInCatalog(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE FUNCTION add_one(x int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN x + 1; END $$`,
		cat); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	got, ok := cat.Routines().Lookup(parser.ObjectName{Name: "add_one"}, []catalog.Type{{Name: "int"}})
	if !ok {
		t.Fatal("Lookup did not find registered routine")
	}
	if got.Schema != "public" {
		t.Errorf("Schema = %q, want public (default)", got.Schema)
	}
	if got.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", got.Language)
	}
	if !strings.Contains(got.Body, "RETURN x + 1") {
		t.Errorf("Body = %q, want it to contain RETURN x + 1", got.Body)
	}
	if got.ReturnType.Name != "int" {
		t.Errorf("ReturnType = %q, want int", got.ReturnType.Name)
	}
}

// TestExecCreateFunctionDuplicateRejected pins the SQLSTATE 42723
// path for duplicate-without-OR-REPLACE.
func TestExecCreateFunctionDuplicateRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	src := `CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`
	if err := runRoutineDDL(t, src, cat); err != nil {
		t.Fatalf("first CREATE: %v", err)
	}
	err := runRoutineDDL(t, src, cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42723" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42723", err)
	}
}

// TestExecCreateOrReplaceFunctionUpdatesBody pins that OR REPLACE
// preserves the OID and updates the body in place.
func TestExecCreateOrReplaceFunctionUpdatesBody(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ v1 $$`, cat); err != nil {
		t.Fatal(err)
	}
	v1, _ := cat.Routines().Lookup(parser.ObjectName{Name: "f"}, nil)
	if err := runRoutineDDL(t,
		`CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ v2 $$`, cat); err != nil {
		t.Fatal(err)
	}
	v2, _ := cat.Routines().Lookup(parser.ObjectName{Name: "f"}, nil)
	if v2.OID != v1.OID {
		t.Errorf("OID changed across OR REPLACE: %d -> %d", v1.OID, v2.OID)
	}
	if !strings.Contains(v2.Body, "v2") {
		t.Errorf("Body = %q, want v2", v2.Body)
	}
}

// TestExecCreateFunctionRejectsUnsupportedLanguage pins the
// language-allowlist diagnostic. The Stage A executor only
// accepts plpgsql / sql; anything else surfaces SQLSTATE 42704
// "undefined object" with a specific message.
func TestExecCreateFunctionRejectsUnsupportedLanguage(t *testing.T) {
	cat := catalog.NewInMemory()
	err := runRoutineDDL(t,
		`CREATE FUNCTION f() RETURNS int LANGUAGE c AS $$ noop $$`,
		cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42704" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42704", err)
	}
}

// TestExecDropFunctionRemovesEntry pins the standard happy path
// for DROP FUNCTION with an explicit signature.
func TestExecDropFunctionRemovesEntry(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE FUNCTION f(int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
		cat); err != nil {
		t.Fatal(err)
	}
	if err := runRoutineDDL(t, `DROP FUNCTION f(int)`, cat); err != nil {
		t.Fatalf("DROP FUNCTION: %v", err)
	}
	if _, ok := cat.Routines().Lookup(
		parser.ObjectName{Name: "f"}, []catalog.Type{{Name: "int"}}); ok {
		t.Error("Lookup after DROP must miss")
	}
}

// TestExecDropFunctionMissingNoIfExistsErrors pins SQLSTATE 42883
// "undefined function" when the target signature doesn't resolve
// and IF EXISTS was not given.
func TestExecDropFunctionMissingNoIfExistsErrors(t *testing.T) {
	cat := catalog.NewInMemory()
	err := runRoutineDDL(t, `DROP FUNCTION nope(int)`, cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42883", err)
	}
}

// TestExecDropFunctionIfExistsSwallowsMissing pins the IF EXISTS
// contract: missing routine is a no-op.
func TestExecDropFunctionIfExistsSwallowsMissing(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t, `DROP FUNCTION IF EXISTS nope(int)`, cat); err != nil {
		t.Errorf("DROP FUNCTION IF EXISTS missing should be no-op, got %v", err)
	}
}

// TestExecDropFunctionAmbiguousBareName pins SQLSTATE 42725 when
// bare-name DROP collides with multiple overloads.
func TestExecDropFunctionAmbiguousBareName(t *testing.T) {
	cat := catalog.NewInMemory()
	for _, src := range []string{
		`CREATE FUNCTION f(int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
		`CREATE FUNCTION f(text) RETURNS int LANGUAGE plpgsql AS $$ BEGIN END $$`,
	} {
		if err := runRoutineDDL(t, src, cat); err != nil {
			t.Fatal(err)
		}
	}
	err := runRoutineDDL(t, `DROP FUNCTION f`, cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42725" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42725", err)
	}
}
