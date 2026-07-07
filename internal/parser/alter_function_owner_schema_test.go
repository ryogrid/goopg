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
