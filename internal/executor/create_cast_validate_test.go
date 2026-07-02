package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestValidateCreateCast exercises the PostgreSQL CreateCast argument/return-type
// rules ported in DU-002 slice 398 (functioncmds.c CreateCast). Each case states
// the source/target types, the resolved WITH FUNCTION routine (nil for
// WITHOUT FUNCTION / WITH INOUT), and whether PG would accept the definition.
func TestValidateCreateCast(t *testing.T) {
	// fn builds a normal SQL function routine with the given input arg type names
	// and return type name.
	fn := func(ret string, args ...string) *catalog.Routine {
		argTypes := make([]catalog.Type, len(args))
		for i, a := range args {
			argTypes[i] = catalog.Type{Name: a}
		}
		return &catalog.Routine{Name: "f", ArgTypes: argTypes, ReturnType: catalog.Type{Name: ret}}
	}

	cases := []struct {
		name    string
		source  string
		target  string
		method  string // "b" binary, "i" inout, "f" function
		routine *catalog.Routine
		wantErr string // substring of the expected error message; "" = must succeed
	}{
		// --- WITHOUT FUNCTION (binary) -----------------------------------------
		{name: "binary distinct types ok", source: "text", target: "bytea", method: "b", wantErr: ""},
		{name: "binary same type rejected", source: "text", target: "text", method: "b",
			wantErr: "source data type and target data type are the same"},
		{name: "binary same type via alias rejected", source: "integer", target: "int4", method: "b",
			wantErr: "source data type and target data type are the same"},

		// --- WITH INOUT ---------------------------------------------------------
		{name: "inout distinct types ok", source: "text", target: "integer", method: "i", wantErr: ""},
		{name: "inout same type rejected", source: "text", target: "text", method: "i",
			wantErr: "source data type and target data type are the same"},

		// --- WITH FUNCTION ------------------------------------------------------
		{name: "function 1-arg ok", source: "text", target: "integer", method: "f",
			routine: fn("integer", "text"), wantErr: ""},
		{name: "function arg alias ok", source: "integer", target: "text", method: "f",
			routine: fn("text", "int4"), wantErr: ""},
		{name: "function wrong source arg rejected", source: "text", target: "integer", method: "f",
			routine: fn("integer", "boolean"),
			wantErr: "argument of cast function must match or be binary-coercible from source data type"},
		{name: "function wrong return rejected", source: "text", target: "integer", method: "f",
			routine: fn("bigint", "text"),
			wantErr: "return data type of cast function must match or be binary-coercible to target data type"},
		{name: "function zero args rejected", source: "text", target: "integer", method: "f",
			routine: fn("integer"),
			wantErr: "cast function must take one to three arguments"},
		{name: "function four args rejected", source: "text", target: "integer", method: "f",
			routine: fn("integer", "text", "integer", "boolean", "text"),
			wantErr: "cast function must take one to three arguments"},
		{name: "function second arg must be int4", source: "text", target: "integer", method: "f",
			routine: fn("integer", "text", "text"),
			wantErr: "second argument of cast function must be type integer"},
		{name: "function third arg must be bool", source: "text", target: "integer", method: "f",
			routine: fn("integer", "text", "integer", "integer"),
			wantErr: "third argument of cast function must be type boolean"},
		{name: "function length coercion same type ok", source: "text", target: "text", method: "f",
			routine: fn("text", "text", "integer"), wantErr: ""},
		{name: "function same type single arg rejected", source: "text", target: "text", method: "f",
			routine: fn("text", "text"),
			wantErr: "source data type and target data type are the same"},
		{name: "function returning set rejected", source: "text", target: "integer", method: "f",
			routine: &catalog.Routine{Name: "f", ArgTypes: []catalog.Type{{Name: "text"}},
				ReturnType: catalog.Type{Name: "integer"}, ReturnsSet: true},
			wantErr: "cast function must not return a set"},
		{name: "procedure rejected", source: "text", target: "integer", method: "f",
			routine: &catalog.Routine{Name: "f", ArgTypes: []catalog.Type{{Name: "text"}},
				ReturnType: catalog.Type{Name: "integer"}, IsProcedure: true},
			wantErr: "cast function must be a normal function"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &parser.CompatNoopStmt{
				ObjType:    "cast",
				ArgTypes:   []string{tc.source, tc.target},
				CastMethod: tc.method,
			}
			err := validateCreateCast(s, tc.routine)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			if ee, ok := err.(*ExecError); ok {
				if ee.Code != "42P17" {
					t.Fatalf("expected SQLSTATE 42P17, got %q", ee.Code)
				}
			} else {
				t.Fatalf("expected *ExecError, got %T", err)
			}
		})
	}
}
