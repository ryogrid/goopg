package executor

// pg_get_function_sqlbody_test.go — DU-002 slice 44.
// pg_dump's dumpFunc EXECUTE projects pg_get_function_sqlbody(p.oid) (PG14+):
// the deparsed SQL-standard body of a `LANGUAGE sql ... BEGIN ATOMIC` function,
// or NULL for any routine without a parsed standard body. The seed pg_proc
// already registered OID 6197 for it, but the executor lacked a dispatch case,
// so the call raised 42883 "function ... does not exist". goopg has no BEGIN
// ATOMIC support, so the builtin returns NULL for every routine — these tests
// pin that invariant.

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestPgGetFunctionSqlbody(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:       "f_sql",
		ArgNames:   []string{"a"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "sql",
		Body:       "SELECT $1",
	}, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	body, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_sqlbody",
		Args: []planner.Expr{&planner.IntegerConst{Value: int64(created.OID)}},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_sqlbody: %v", err)
	}
	// goopg never parses a BEGIN ATOMIC standard body, so NULL for every
	// routine — matching what pg_dump's dumpFunc expects for quoted-body
	// SQL functions.
	if !body.IsNull() {
		t.Errorf("pg_get_function_sqlbody = %q, want NULL", body.StringValue())
	}
}

// TestPgGetFunctionSqlbodyUnknownOID pins that an OID with no matching routine
// also yields NULL (not an error), since the function is a no-op stub.
func TestPgGetFunctionSqlbodyUnknownOID(t *testing.T) {
	cat := catalog.NewInMemory()
	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	body, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_sqlbody",
		Args: []planner.Expr{&planner.IntegerConst{Value: 999999}},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_sqlbody: %v", err)
	}
	if !body.IsNull() {
		t.Errorf("pg_get_function_sqlbody = %q, want NULL", body.StringValue())
	}
}
