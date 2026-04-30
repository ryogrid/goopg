package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestExecCreateProcedureRegistersInCatalog pins CREATE PROCEDURE DDL.
func TestExecCreateProcedureRegistersInCatalog(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE PROCEDURE p(x int) LANGUAGE plpgsql AS $$ BEGIN END $$`,
		cat); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	got, ok := cat.Routines().Lookup(parser.ObjectName{Name: "p"}, []catalog.Type{{Name: "int"}})
	if !ok {
		t.Fatal("Lookup did not find registered procedure")
	}
	if got.Schema != "public" {
		t.Errorf("Schema = %q, want public (default)", got.Schema)
	}
	if got.Language != "plpgsql" {
		t.Errorf("Language = %q, want plpgsql", got.Language)
	}
	if got.ReturnType.Name != "" {
		t.Errorf("ReturnType = %q, want empty for procedure", got.ReturnType.Name)
	}
}

// TestExecCreateProcedureDuplicateRejected pins SQLSTATE 42723.
func TestExecCreateProcedureDuplicateRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	src := `CREATE PROCEDURE p() LANGUAGE plpgsql AS $$ BEGIN END $$`
	if err := runRoutineDDL(t, src, cat); err != nil {
		t.Fatal(err)
	}
	err := runRoutineDDL(t, src, cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42723" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42723", err)
	}
}

// TestExecCreateProcedureRejectsUnsupportedLanguage pins SQLSTATE 42704.
func TestExecCreateProcedureRejectsUnsupportedLanguage(t *testing.T) {
	cat := catalog.NewInMemory()
	err := runRoutineDDL(t,
		`CREATE PROCEDURE p() LANGUAGE python AS $$ pass $$`,
		cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42704" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42704", err)
	}
}

// TestExecCallProcedure runs a minimal PL/pgSQL procedure via CALL.
func TestExecCallProcedure(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := runRoutineDDL(t,
		`CREATE PROCEDURE greet(x int) LANGUAGE plpgsql AS $$
		BEGIN
			RETURN x + 1;
		END $$`,
		cat); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	plan := planOne(t, "CALL greet(42)", cat)
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx := NewContext()
	ctx.Catalog = cat
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer op.Close()
	for {
		_, err := op.Next()
		if err == EOF {
			return
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
}

// TestExecCallProcedureNotFound pins SQLSTATE 42883 for missing procedure.
func TestExecCallProcedureNotFound(t *testing.T) {
	cat := catalog.NewInMemory()
	plan := planOne(t, "CALL nonexistent()", cat)
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx := NewContext()
	ctx.Catalog = cat
	err = op.Open(ctx)
	if err == nil {
		op.Close()
		t.Fatal("expected error from Open, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("got err=%v, want ExecError SQLSTATE 42883", err)
	}
}
