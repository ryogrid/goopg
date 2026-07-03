package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
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
		_, err := op.Next()
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
// language-allowlist diagnostic. The Stage A executor accepts plpgsql, sql,
// and c (C-stub); other languages surface SQLSTATE 42704.
func TestExecCreateFunctionRejectsUnsupportedLanguage(t *testing.T) {
	cat := catalog.NewInMemory()
	// LANGUAGE C is now accepted as a stub — must not error.
	if err := runRoutineDDL(t,
		`CREATE FUNCTION f() RETURNS bool LANGUAGE c AS ''`,
		cat); err != nil {
		t.Fatalf("LANGUAGE C should be accepted as stub, got err=%v", err)
	}
	// Other unsupported languages still surface 42704.
	err := runRoutineDDL(t,
		`CREATE FUNCTION g() RETURNS int LANGUAGE python3 AS $$ pass $$`,
		cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42704" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42704 for python3", err)
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

// TestExecDropFunctionMissingSchemaQualifiedErrorMessage pins the DU-002
// slice 437 fix: DROP FUNCTION's "does not exist" / "could not find a
// function named" messages must include the schema qualifier the statement
// was given, matching real PG's NameListToString(funcname) (used by both
// func_signature_string, parse_func.c, and dropcmds.c's does_not_exist_skipping
// FUNCTION case) — before this fix goopg rendered the bare object name only,
// silently dropping an explicit schema qualifier from the error text.
func TestExecDropFunctionMissingSchemaQualifiedErrorMessage(t *testing.T) {
	cat := catalog.NewInMemory()

	err := runRoutineDDL(t, `DROP FUNCTION public.nope(int)`, cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42883", err)
	}
	if want := `function public.nope(integer) does not exist`; ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
	}

	err = runRoutineDDL(t, `DROP FUNCTION public.nope`, cat)
	ee, ok = err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42883", err)
	}
	if want := `could not find a function named "public.nope"`; ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
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

// TestExecDropProcedureRemovesEntry pins the standard autocommit happy path
// for DROP PROCEDURE, mirroring TestExecDropFunctionRemovesEntry.
func TestExecDropProcedureRemovesEntry(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE PROCEDURE p(int) LANGUAGE plpgsql AS $$ BEGIN END $$`,
		cat); err != nil {
		t.Fatal(err)
	}
	if err := runRoutineDDL(t, `DROP PROCEDURE p(int)`, cat); err != nil {
		t.Fatalf("DROP PROCEDURE: %v", err)
	}
	if _, ok := cat.Routines().Lookup(
		parser.ObjectName{Name: "p"}, []catalog.Type{{Name: "int"}}); ok {
		t.Error("Lookup after DROP must miss")
	}
}

// runRoutineDDLInSession runs a single DDL statement against ctx/sess (an
// explicit-transaction-aware session already wired into ctx), without
// draining/committing anything — the caller inspects/applies the deferred
// state itself. Distinct from runRoutineDDL, which always builds a fresh
// no-Session Context (autocommit).
func runRoutineDDLInSession(t *testing.T, ctx *Context, sql string, cat catalog.Catalog) error {
	t.Helper()
	plan := planOne(t, sql, cat)
	op, err := Build(plan)
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	defer op.Close()
	for {
		_, err := op.Next()
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TestExecDropProcedureDeferredToCommit pins the loop #74 unification: DROP
// PROCEDURE inside an explicit transaction defers its registry removal to
// COMMIT exactly like DROP FUNCTION (M0118-0009 `stats`) — the procedure
// stays resolvable until ApplyDeferredRoutineDrops runs, which is the single
// chokepoint that also WAL-logs the removal (never invoked on ROLLBACK).
func TestExecDropProcedureDeferredToCommit(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE PROCEDURE p(int) LANGUAGE plpgsql AS $$ BEGIN END $$`,
		cat); err != nil {
		t.Fatal(err)
	}

	sess := NewBasicSession()
	sess.BeginExplicitTransaction(mvcc.Transaction{}, mvcc.Snapshot{})
	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Session = sess

	if err := runRoutineDDLInSession(t, ctx, `DROP PROCEDURE p(int)`, cat); err != nil {
		t.Fatalf("DROP PROCEDURE (deferred): %v", err)
	}

	if _, ok := cat.Routines().Lookup(parser.ObjectName{Name: "p"}, []catalog.Type{{Name: "int"}}); !ok {
		t.Fatal("procedure must remain resolvable/callable until COMMIT")
	}

	ApplyDeferredRoutineDrops(ctx, sess)
	if _, ok := cat.Routines().Lookup(parser.ObjectName{Name: "p"}, []catalog.Type{{Name: "int"}}); ok {
		t.Fatal("procedure must be removed after ApplyDeferredRoutineDrops (COMMIT)")
	}
}

// TestExecDropProcedureRollbackLeavesEntry confirms the ROLLBACK side of the
// same unification: discarding the deferred entry (TakeDeferredRoutineDrops,
// as done by operators_tx.go execRollback / dispatch.go's simple-query
// TxRollback / EndExplicitTransaction) never touches the registry, so the
// procedure survives — unlike the pre-loop-#74 immediate-mutation design,
// which needed a separate rollback-undo (AddPendingRoutineDrop/Create) to
// achieve the same outcome.
func TestExecDropProcedureRollbackLeavesEntry(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE PROCEDURE p(int) LANGUAGE plpgsql AS $$ BEGIN END $$`,
		cat); err != nil {
		t.Fatal(err)
	}

	sess := NewBasicSession()
	sess.BeginExplicitTransaction(mvcc.Transaction{}, mvcc.Snapshot{})
	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Session = sess

	if err := runRoutineDDLInSession(t, ctx, `DROP PROCEDURE p(int)`, cat); err != nil {
		t.Fatalf("DROP PROCEDURE (deferred): %v", err)
	}

	sess.TakeDeferredRoutineDrops() // ROLLBACK: discard without applying
	sess.EndExplicitTransaction()

	if _, ok := cat.Routines().Lookup(parser.ObjectName{Name: "p"}, []catalog.Type{{Name: "int"}}); !ok {
		t.Fatal("procedure must survive ROLLBACK — the registry was never mutated")
	}
}
