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

// TestPgGetFunctionArgumentsDefault pins DU-002 slice 160: a trailing input arg
// with a DEFAULT must carry its ` DEFAULT <expr>` clause in the *full* argument
// list (pg_get_function_arguments, print_defaults=true) but be DROPPED in the
// identity form (pg_get_function_identity_arguments, print_defaults=false). PG's
// print_function_arguments only appends the default for input args, so an OUT
// arg with a (nonsensical) default value would never print one — argIsInput
// enforces that.
func TestPgGetFunctionArgumentsDefault(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:        "add_default",
		ArgNames:    []string{"a", "b"},
		ArgTypes:    []catalog.Type{{Name: "integer"}, {Name: "integer"}},
		ArgModes:    []string{"i", "i"},
		ArgDefaults: []string{"", "10"},
		ReturnType:  catalog.Type{Name: "integer"},
		Language:    "sql",
		Body:        "SELECT $1 + $2",
	}, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	oidArg := &planner.IntegerConst{Value: int64(created.OID)}

	full, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_arguments: %v", err)
	}
	if got, want := full.StringValue(), "a integer, b integer DEFAULT 10"; got != want {
		t.Errorf("full args = %q, want %q", got, want)
	}

	identity, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_identity_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_identity_arguments: %v", err)
	}
	if got, want := identity.StringValue(), "a integer, b integer"; got != want {
		t.Errorf("identity args = %q, want %q (DEFAULT must be omitted)", got, want)
	}
}

// TestPgGetFunctionResultReturnsTable pins DU-002 slice 165: a RETURNS TABLE
// function stores its table columns as trailing OUT args, but the catalog
// deparsers must re-render them as PG does — the table columns appear ONLY in
// pg_get_function_result's `TABLE(...)` clause and are EXCLUDED from
// pg_get_function_arguments / pg_get_function_identity_arguments. pg_dump's
// dumpFunc concatenates funcfullsig (from the arguments) + " RETURNS " +
// funcresult verbatim, so without this split a `CREATE FUNCTION f()
// RETURNS TABLE(a int)` would dump as `f(OUT a int) RETURNS SETOF record`
// (valid but divergent from upstream).
func TestPgGetFunctionResultReturnsTable(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:         "f_tab",
		ArgNames:     []string{"x", "a", "b"},
		ArgTypes:     []catalog.Type{{Name: "integer"}, {Name: "integer"}, {Name: "text"}},
		ArgModes:     []string{"i", "o", "o"},
		ReturnType:   catalog.Type{Name: "record"},
		ReturnsSet:   true,
		ReturnsTable: true,
		Language:     "sql",
		Body:         "SELECT 1, 'x'",
	}, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	oidArg := &planner.IntegerConst{Value: int64(created.OID)}

	// Arguments exclude the table columns; the lone IN arg keeps no mode prefix
	// (PG omits IN for plain functions once the TABLE columns are removed).
	args, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_arguments: %v", err)
	}
	if got, want := args.StringValue(), "x integer"; got != want {
		t.Errorf("arguments = %q, want %q (TABLE cols must be excluded)", got, want)
	}

	// Identity arguments share the same exclusion.
	ident, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_identity_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_identity_arguments: %v", err)
	}
	if got, want := ident.StringValue(), "x integer"; got != want {
		t.Errorf("identity arguments = %q, want %q", got, want)
	}

	// The result renders the TABLE(...) clause from the OUT-stored columns.
	result, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_result",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_result: %v", err)
	}
	if got, want := result.StringValue(), "TABLE(a integer, b text)"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

// TestPgGetFunctionResultReturnsTableNoArgs pins the zero-input-arg RETURNS
// TABLE form: the argument list is empty (pg_dump renders `f()`) and the result
// is the full TABLE(...) clause.
func TestPgGetFunctionResultReturnsTableNoArgs(t *testing.T) {
	cat := catalog.NewInMemory()
	created, err := cat.Routines().Create(&catalog.Routine{
		Name:         "f_tab0",
		ArgNames:     []string{"a"},
		ArgTypes:     []catalog.Type{{Name: "integer"}},
		ArgModes:     []string{"o"},
		ReturnType:   catalog.Type{Name: "record"},
		ReturnsSet:   true,
		ReturnsTable: true,
		Language:     "sql",
		Body:         "SELECT 1",
	}, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	oidArg := &planner.IntegerConst{Value: int64(created.OID)}

	args, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_arguments",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_arguments: %v", err)
	}
	if got, want := args.StringValue(), ""; got != want {
		t.Errorf("arguments = %q, want empty", got)
	}

	result, err := evalExpr(&planner.FuncCall{
		Name: "pg_get_function_result",
		Args: []planner.Expr{oidArg},
	}, nil, ctx)
	if err != nil {
		t.Fatalf("pg_get_function_result: %v", err)
	}
	if got, want := result.StringValue(), "TABLE(a integer)"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}
