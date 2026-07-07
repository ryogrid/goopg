package parser

import "testing"

// TestParseAlterFunctionOwner pins the `ALTER FUNCTION/PROCEDURE/ROUTINE
// name(args) OWNER TO ...` parse shape (M0097-0150). Previously OWNER TO was
// parsed and silently discarded (a documented no-op in goopg v0); this test
// guards the new NewOwner field.
func TestParseAlterFunctionOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER FUNCTION myfunc(int4) OWNER TO newrole", name: "myfunc", newOwner: "newrole"},
		{sql: "ALTER FUNCTION myfunc(int4) OWNER TO CURRENT_USER", name: "myfunc", newOwner: "current_user"},
		{sql: "ALTER FUNCTION myfunc(int4) OWNER TO SESSION_USER", name: "myfunc", newOwner: "current_user"},
		{sql: "ALTER FUNCTION myfunc(int4) OWNER TO CURRENT_ROLE", name: "myfunc", newOwner: "current_user"},
		{sql: "ALTER PROCEDURE myproc(int4) OWNER TO newrole", name: "myproc", newOwner: "newrole"},
		{sql: "ALTER ROUTINE myfunc(int4) OWNER TO newrole", name: "myfunc", newOwner: "newrole"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			af, ok := stmts[0].(*AlterFunctionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterFunctionStmt", stmts[0])
			}
			if af.Name.Name != tc.name {
				t.Errorf("Name.Name = %q, want %q", af.Name.Name, tc.name)
			}
			if af.NewOwner != tc.newOwner {
				t.Errorf("NewOwner = %q, want %q", af.NewOwner, tc.newOwner)
			}
			if af.NewSchema != "" {
				t.Errorf("NewSchema = %q, want empty", af.NewSchema)
			}
		})
	}
}

// TestParseAlterFunctionSetSchema pins the `ALTER FUNCTION/PROCEDURE/ROUTINE
// name(args) SET SCHEMA newschema` parse shape (M0097-0150). Previously SET
// SCHEMA was parsed and silently discarded.
func TestParseAlterFunctionSetSchema(t *testing.T) {
	cases := []struct {
		sql       string
		name      string
		newSchema string
	}{
		{sql: "ALTER FUNCTION myfunc(int4) SET SCHEMA app", name: "myfunc", newSchema: "app"},
		{sql: "ALTER PROCEDURE myproc(int4) SET SCHEMA app", name: "myproc", newSchema: "app"},
		{sql: "ALTER ROUTINE myfunc(int4) SET SCHEMA app", name: "myfunc", newSchema: "app"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			af, ok := stmts[0].(*AlterFunctionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterFunctionStmt", stmts[0])
			}
			if af.Name.Name != tc.name {
				t.Errorf("Name.Name = %q, want %q", af.Name.Name, tc.name)
			}
			if af.NewSchema != tc.newSchema {
				t.Errorf("NewSchema = %q, want %q", af.NewSchema, tc.newSchema)
			}
			if af.NewOwner != "" {
				t.Errorf("NewOwner = %q, want empty", af.NewOwner)
			}
		})
	}
}

// TestParseAlterFunctionRenameAndVolatileStillWork guards against a
// regression where adding OWNER TO/SET SCHEMA capture shadows the
// pre-existing RENAME TO / attribute-clause branches sharing the same
// attribute-consuming loop.
func TestParseAlterFunctionRenameAndVolatileStillWork(t *testing.T) {
	stmts, err := Parse("ALTER FUNCTION myfunc(int4) RENAME TO renamedfunc")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	af, ok := stmts[0].(*AlterFunctionStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterFunctionStmt", stmts[0])
	}
	if af.RenameTo != "renamedfunc" {
		t.Errorf("RenameTo = %q, want renamedfunc", af.RenameTo)
	}

	stmts, err = Parse("ALTER FUNCTION myfunc(int4) IMMUTABLE STRICT")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	af, ok = stmts[0].(*AlterFunctionStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterFunctionStmt", stmts[0])
	}
	if af.Volatile == nil || *af.Volatile != "i" {
		t.Errorf("Volatile = %v, want \"i\"", af.Volatile)
	}
	if af.Strict == nil || !*af.Strict {
		t.Errorf("Strict = %v, want true", af.Strict)
	}
}

// TestParseAlterFunctionGenericSetReset pins the generic `ALTER FUNCTION
// name(args) SET guc_name {TO|=} value | SET guc_name FROM CURRENT | RESET
// guc_name | RESET ALL` forms (gram.y's `common_func_opt_item:
// FunctionSetResetClause`). Before this fix these all hit a syntax error:
// the outer SET-detection gate matched only TokenIdent "set" (SET actually
// lexes as the keyword KwSet), and even once that was fixed
// (M0097-0150's OWNER TO/SET SCHEMA loop), `=` lexes as TokenOperator not
// TokenSymbol so `acceptSymbol("=")` never matched, and the FROM-CURRENT
// branch looked for a literal "from" token immediately after SET instead of
// parsing the guc name first. goopg has no per-function GUC-override
// storage, so all of these remain no-ops (parse-only, no dedicated
// AlterFunctionStmt field) — this test only guards that they parse without
// error and don't clobber unrelated fields/leave the statement stream
// desynced.
func TestParseAlterFunctionGenericSetReset(t *testing.T) {
	cases := []struct {
		sql  string
		name string
	}{
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path TO app", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path = app", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path FROM CURRENT", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path TO DEFAULT", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) RESET search_path", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) RESET ALL", name: "myfunc"},
		{sql: "ALTER FUNCTION myfunc(int4) IMMUTABLE SET search_path = app", name: "myfunc"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			af, ok := stmts[0].(*AlterFunctionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterFunctionStmt", stmts[0])
			}
			if af.Name.Name != tc.name {
				t.Errorf("Name.Name = %q, want %q", af.Name.Name, tc.name)
			}
			if af.NewSchema != "" {
				t.Errorf("NewSchema = %q, want empty (SET guc_name is a distinct no-op form from SET SCHEMA)", af.NewSchema)
			}
		})
	}
}
