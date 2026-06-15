package executor

// pg_get_function_identity_arguments_test.go — DU-002 slice 43.
// pg_dump's dumpFunc EXECUTE projects pg_get_function_identity_arguments(p.oid)
// alongside pg_get_function_arguments / pg_get_function_result. The seed pg_proc
// already registered OID 2232 for it, but the executor lacked a dispatch case,
// so the call raised 42883 "function ... does not exist". These tests pin the
// new case: identity arguments mirror pg_get_function_arguments because goopg
// emits no DEFAULT clauses (upstream's only difference is print_defaults=false).

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestPgGetFunctionIdentityArguments(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:       "f_two_in",
		ArgNames:   []string{"a", "b"},
		ArgTypes:   []catalog.Type{{Name: "integer"}, {Name: "text"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	oidArg := &planner.IntegerConst{Value: int64(created.OID)}

	identity, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_identity_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_identity_arguments: %v", err)
	}
	if identity.Kind != KindString {
		t.Fatalf("identity kind = %v, want string", identity.Kind)
	}
	const want = "a integer, b text"
	if identity.StringValue() != want {
		t.Errorf("identity = %q, want %q", identity.StringValue(), want)
	}

	// For an all-IN function with no defaults, identity arguments and the full
	// argument list are byte-identical — the documented invariant for goopg.
	full, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_arguments: %v", err)
	}
	if full.StringValue() != identity.StringValue() {
		t.Errorf("full args %q != identity args %q", full.StringValue(), identity.StringValue())
	}
}

// TestPgGetFunctionIdentityArgumentsOutMode pins that OUT/INOUT mode prefixes
// survive in the identity form, matching upstream print_function_arguments
// (which prints all non-TABLE args regardless of mode).
func TestPgGetFunctionIdentityArgumentsOutMode(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:       "f_inout",
		ArgNames:   []string{"x", "y"},
		ArgTypes:   []catalog.Type{{Name: "integer"}, {Name: "integer"}},
		ArgModes:   []string{"i", "o"},
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

	identity, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_identity_arguments",
		Args: []planner.Expr{&planner.IntegerConst{Value: int64(created.OID)}},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_identity_arguments: %v", err)
	}
	const want = "IN x integer, OUT y integer"
	if identity.StringValue() != want {
		t.Errorf("identity = %q, want %q", identity.StringValue(), want)
	}
}
