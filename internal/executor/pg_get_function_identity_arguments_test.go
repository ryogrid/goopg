package executor

// pg_get_function_identity_arguments_test.go — DU-002 slice 43.
// pg_dump's dumpFunc EXECUTE projects pg_get_function_identity_arguments(p.oid)
// alongside pg_get_function_arguments / pg_get_function_result. The seed pg_proc
// already registered OID 2232 for it, but the executor lacked a dispatch case,
// so the call raised 42883 "function ... does not exist". These tests pin the
// new case: identity arguments mirror pg_get_function_arguments because goopg
// emits no DEFAULT clauses (upstream's only difference is print_defaults=false).

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// TestPgGetFunctionArgsQuotedCharRendersQuoted — M0119-0006 (92nd slice, deferral
// row 1358). canonicalTypeName now threads Routine.ArgTypeOIDs (captured by CREATE
// FUNCTION) into the pg_get_function_* renderers, so a quoted `"char"` arg
// (CHAROID 18) renders `"char"` while a bare `char` arg (BPCHAROID 1042) keeps
// rendering `character`. Mirrors PG's format_type_extended: BPCHAROID → "character"
// and there is no CHAROID case, so the default quote_qualified_identifier path
// yields `"char"` (postgres/src/backend/utils/adt/format_type.c:207-220,303-322).
// Sibling of the landed regprocedureArglist char arm, but with the OID-0 baseline
// kept as "character" here (canonicalTypeName's historical OID-less render for
// routineOrAggregateArgs / pre-90th routines). Arg DDL uses the unnamed form
// (`("char")` / `(char)`) — the same idiom as TestCreateFunctionCapturesCharArgOID,
// since the CREATE FUNCTION parser does not accept a `name "char"` argument.
func TestPgGetFunctionArgsQuotedCharRendersQuoted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name     string
		ddl      string
		wantOID  uint32
		wantType string // canonical type name the arg must render as
		other    string // the mutually-exclusive spelling that must NOT appear
	}{
		{"g_qchar", `CREATE FUNCTION g_qchar("char") RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDChar, `"char"`, "character"},
		{"g_bpchar", `CREATE FUNCTION g_bpchar(char) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDBpChar, "character", `"char"`},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.ddl); err != nil {
			t.Fatalf("create function %s: %v", tc.name, err)
		}
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		r := cands[0]
		// Sanity: CREATE FUNCTION captured the intended arg OID (18 vs 1042).
		if len(r.ArgTypeOIDs) != 1 || r.ArgTypeOIDs[0] != tc.wantOID {
			t.Errorf("%s: ArgTypeOIDs = %v, want [%d]", tc.name, r.ArgTypeOIDs, tc.wantOID)
		}
		for _, builtin := range []string{"pg_get_function_arguments", "pg_get_function_identity_arguments", "pg_get_functiondef"} {
			rows := runQuery(t, ctx, fmt.Sprintf("SELECT %s(%d)", builtin, r.OID))
			if len(rows) != 1 {
				t.Fatalf("%s(%d): rows = %d, want 1", builtin, r.OID, len(rows))
			}
			got := rows[0][0].StringValue()
			if !strings.Contains(got, tc.wantType) {
				t.Errorf("%s: %s(%d) = %q, want it to contain %q", tc.name, builtin, r.OID, got, tc.wantType)
			}
			if strings.Contains(got, tc.other) {
				t.Errorf("%s: %s(%d) = %q, must not contain %q", tc.name, builtin, r.OID, got, tc.other)
			}
		}
	}
}

func TestPgGetFunctionResultQuotedCharRendersQuoted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name     string
		ddl      string
		wantOID  uint32
		wantType string // canonical type name the RETURN type must render as
		other    string // the mutually-exclusive spelling that must NOT appear
	}{
		{"g_qchar_ret", `CREATE FUNCTION g_qchar_ret() RETURNS "char" LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDChar, `"char"`, "character"},
		{"g_bpchar_ret", `CREATE FUNCTION g_bpchar_ret() RETURNS char LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDBpChar, "character", `"char"`},
		{"g_int_ret", `CREATE FUNCTION g_int_ret() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`, 0, "integer", `"char"`},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.ddl); err != nil {
			t.Fatalf("create function %s: %v", tc.name, err)
		}
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		r := cands[0]
		// Sanity: CREATE FUNCTION captured the intended return-type OID (18 vs 1042 vs 0).
		if r.ReturnTypeOID != tc.wantOID {
			t.Errorf("%s: ReturnTypeOID = %d, want %d", tc.name, r.ReturnTypeOID, tc.wantOID)
		}
		// pg_get_function_result returns exactly the rendered return type.
		rows := runQuery(t, ctx, fmt.Sprintf("SELECT pg_get_function_result(%d)", r.OID))
		if len(rows) != 1 {
			t.Fatalf("pg_get_function_result(%d): rows = %d, want 1", r.OID, len(rows))
		}
		got := rows[0][0].StringValue()
		if got != tc.wantType {
			t.Errorf("%s: pg_get_function_result(%d) = %q, want %q", tc.name, r.OID, got, tc.wantType)
		}
		// pg_get_functiondef's RETURNS clause renders the same canonical type.
		rows = runQuery(t, ctx, fmt.Sprintf("SELECT pg_get_functiondef(%d)", r.OID))
		if len(rows) != 1 {
			t.Fatalf("pg_get_functiondef(%d): rows = %d, want 1", r.OID, len(rows))
		}
		got = rows[0][0].StringValue()
		if !strings.Contains(got, tc.wantType) {
			t.Errorf("%s: pg_get_functiondef(%d) = %q, want it to contain %q", tc.name, r.OID, got, tc.wantType)
		}
		if strings.Contains(got, tc.other) {
			t.Errorf("%s: pg_get_functiondef(%d) = %q, must not contain %q", tc.name, r.OID, got, tc.other)
		}
	}
}
