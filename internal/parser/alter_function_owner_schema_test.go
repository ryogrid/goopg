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
		sql     string
		name    string
		wantOps []FunctionConfigOp
	}{
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path TO app", name: "myfunc",
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app"}}},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path = app", name: "myfunc",
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app"}}},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path FROM CURRENT", name: "myfunc",
			wantOps: nil},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path TO DEFAULT", name: "myfunc",
			wantOps: nil},
		{sql: "ALTER FUNCTION myfunc(int4) RESET search_path", name: "myfunc",
			wantOps: []FunctionConfigOp{{Reset: true, Name: "search_path"}}},
		{sql: "ALTER FUNCTION myfunc(int4) RESET ALL", name: "myfunc",
			wantOps: []FunctionConfigOp{{ResetAll: true}}},
		{sql: "ALTER FUNCTION myfunc(int4) IMMUTABLE SET search_path = app", name: "myfunc",
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app"}}},
		// var_list form (gram.y): comma-separated values for list-valued
		// GUCs like search_path/temp_tablespaces.
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path = app, public", name: "myfunc",
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app,public"}}},
		{sql: "ALTER FUNCTION myfunc(int4) SET search_path TO app, public, pg_catalog", name: "myfunc",
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app,public,pg_catalog"}}},
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
			if len(af.ConfigOps) != len(tc.wantOps) {
				t.Fatalf("ConfigOps = %#v, want %#v", af.ConfigOps, tc.wantOps)
			}
			for i, op := range af.ConfigOps {
				if op != tc.wantOps[i] {
					t.Errorf("ConfigOps[%d] = %#v, want %#v", i, op, tc.wantOps[i])
				}
			}
		})
	}
}

// TestParseAlterFunctionSetSchemaDistinctFromConfigSet pins that ALTER
// FUNCTION's dedicated `SET SCHEMA` production (a separate top-level
// alter_type_cmd-style rule, not part of common_func_opt_item) never falls
// through into the generic config-SET path and vice versa. DU-002 proconfig
// follow-up to M0097-0150.
func TestParseAlterFunctionSetSchemaDistinctFromConfigSet(t *testing.T) {
	stmts, err := Parse("ALTER FUNCTION myfunc(int4) SET SCHEMA app")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	af := stmts[0].(*AlterFunctionStmt)
	if af.NewSchema != "app" {
		t.Errorf("NewSchema = %q, want %q", af.NewSchema, "app")
	}
	if len(af.ConfigOps) != 0 {
		t.Errorf("ConfigOps = %#v, want empty (SET SCHEMA must not be captured as a config op)", af.ConfigOps)
	}
}

// TestParseCreateFunctionSetClause pins the CREATE FUNCTION side of the same
// bug fixed for ALTER FUNCTION by M0097-0150: SET always lexes as the real
// keyword KwSet (never TokenIdent), so isFunctionAttribute()'s TokenIdent-only
// "set" case could never fire — any CREATE FUNCTION ... SET ... form was a
// syntax error, not the documented (never-actually-reachable) discard.
// DU-002 proconfig follow-up.
func TestParseCreateFunctionSetClause(t *testing.T) {
	cases := []struct {
		sql     string
		wantOps []FunctionConfigOp
	}{
		{
			sql:     `CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path = app AS $$ SELECT 1 $$`,
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app"}},
		},
		{
			// Combinable with other common_func_opt_item attributes in either order.
			sql:     `CREATE FUNCTION f() RETURNS int LANGUAGE sql STRICT SET search_path TO app, public AS $$ SELECT 1 $$`,
			wantOps: []FunctionConfigOp{{Name: "search_path", Value: "app,public"}},
		},
		{
			sql:     `CREATE FUNCTION f() RETURNS int LANGUAGE sql SET search_path FROM CURRENT AS $$ SELECT 1 $$`,
			wantOps: nil,
		},
		{
			sql:     `CREATE FUNCTION f() RETURNS int LANGUAGE sql RESET search_path AS $$ SELECT 1 $$`,
			wantOps: []FunctionConfigOp{{Reset: true, Name: "search_path"}},
		},
		{
			sql:     `CREATE FUNCTION f() RETURNS int LANGUAGE sql RESET ALL AS $$ SELECT 1 $$`,
			wantOps: []FunctionConfigOp{{ResetAll: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			cf, ok := stmts[0].(*CreateFunctionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *CreateFunctionStmt", stmts[0])
			}
			if len(cf.ConfigOps) != len(tc.wantOps) {
				t.Fatalf("ConfigOps = %#v, want %#v", cf.ConfigOps, tc.wantOps)
			}
			for i, op := range cf.ConfigOps {
				if op != tc.wantOps[i] {
					t.Errorf("ConfigOps[%d] = %#v, want %#v", i, op, tc.wantOps[i])
				}
			}
		})
	}
}
