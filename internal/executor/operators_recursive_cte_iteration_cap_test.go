package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0134-0086 — a WITH RECURSIVE query whose recursive term never reaches
// EOF within a single fixpoint iteration must error out with a bounded,
// catchable 54001 instead of growing iterRows without bound until the host
// OOMs.
//
// Repro (real, PG-accepted SQL — postgres/src/test/regress/sql/with.sql,
// "with recursive q as (... union all (with recursive x as (... union all
// (select * from q union all select * from x)) select * from x)) select *
// from q limit 32"): the inner WITH RECURSIVE x's recursive term unions a
// reference back out to the still-open outer q. Live PG evaluates
// nodeRecursiveunion.c row-at-a-time so the outer LIMIT can cut the pull
// chain short; goopg's recursiveUnionOp.Next() (operators_recursive_cte.go)
// instead fully drains one iteration of the recursive term into iterRows
// before returning anything, so with this query graph it never finishes
// even the first iteration — maxRecursiveDepth (which only advances between
// COMPLETED iterations) never triggers, and the process's RSS climbs
// without bound (confirmed live: 22+ GB and rising before being killed).
//
// This test shrinks maxRecursiveIterationRows so the guard fires promptly
// instead of after millions of rows, and asserts it fires with the correct
// SQLSTATE instead of hanging/OOMing the test process.
func TestRecursiveUnionCapsRunawaySingleIteration(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.Ctx = context.Background()

	orig := maxRecursiveIterationRows
	maxRecursiveIterationRows = 200
	defer func() { maxRecursiveIterationRows = orig }()

	if err := runDDL(t, ctx, "CREATE TABLE m0134_0086_dept(id int, parent_department int, name text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO m0134_0086_dept VALUES (0, NULL, 'ROOT'), (1, 0, 'A')")

	sql := `with recursive q as (
	      select * from m0134_0086_dept
	    union all
	      (with recursive x as (
	           select * from m0134_0086_dept
	         union all
	           (select * from q union all select * from x)
	        )
	       select * from x)
	    )
	select * from q limit 32`

	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	ctx.CTERowCache = nil
	ctx.MaterializedCTEs = nil

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer op.Close()

	var gotErr error
	for {
		_, err := op.Next()
		if err == EOF {
			t.Fatalf("query reached EOF without erroring — the runaway-iteration guard did not fire")
		}
		if err != nil {
			gotErr = err
			break
		}
	}

	execErr, ok := gotErr.(*ExecError)
	if !ok {
		t.Fatalf("Next() error = %v (%T), want *ExecError", gotErr, gotErr)
	}
	if execErr.Code != "54001" {
		t.Fatalf("Next() error code = %q, want 54001", execErr.Code)
	}
	if !strings.Contains(execErr.Message, "recursion depth") {
		t.Fatalf("Next() error message = %q, want it to mention recursion depth", execErr.Message)
	}
}
