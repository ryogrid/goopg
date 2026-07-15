package parser

import "testing"

// TestParseAlterConversionRename pins the `ALTER CONVERSION [IF EXISTS] name
// RENAME TO newname` shape. Mirrors TestParseAlterCollationRename.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
func TestParseAlterConversionRename(t *testing.T) {
	cases := []struct {
		sql      string
		schema   string
		name     string
		ifExists bool
		newName  string
	}{
		{sql: "ALTER CONVERSION myconv RENAME TO newconv", name: "myconv", newName: "newconv"},
		{sql: "ALTER CONVERSION public.myconv RENAME TO newconv", schema: "public", name: "myconv", newName: "newconv"},
		{sql: "ALTER CONVERSION IF EXISTS myconv RENAME TO newconv", name: "myconv", ifExists: true, newName: "newconv"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterConversionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterConversionStmt", stmts[0])
			}
			if ac.Name.Schema != tc.schema || ac.Name.Name != tc.name {
				t.Errorf("Name = %+v, want schema=%q name=%q", ac.Name, tc.schema, tc.name)
			}
			if ac.IfExists != tc.ifExists {
				t.Errorf("IfExists = %v, want %v", ac.IfExists, tc.ifExists)
			}
			if ac.Action != "rename" {
				t.Errorf("Action = %q, want %q", ac.Action, "rename")
			}
			if ac.NewName != tc.newName {
				t.Errorf("NewName = %q, want %q", ac.NewName, tc.newName)
			}
		})
	}
}

// TestParseAlterConversionOwner pins the `ALTER CONVERSION name OWNER TO
// {role | CURRENT_USER | SESSION_USER | CURRENT_ROLE}` shape — the exact
// form pg_dump's dumpConversion emits via the generic archive-owner
// mechanism, and the DU-002 round-trip probe's actual blocker
// (`ALTER CONVERSION public.aliasconv OWNER TO postgres;`).
func TestParseAlterConversionOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER CONVERSION myconv OWNER TO newrole", name: "myconv", newOwner: "newrole"},
		{sql: "ALTER CONVERSION public.aliasconv OWNER TO postgres", name: "aliasconv", newOwner: "postgres"},
		{sql: "ALTER CONVERSION myconv OWNER TO CURRENT_USER", name: "myconv", newOwner: "current_user"},
		{sql: "ALTER CONVERSION myconv OWNER TO SESSION_USER", name: "myconv", newOwner: "current_user"},
		{sql: "ALTER CONVERSION myconv OWNER TO CURRENT_ROLE", name: "myconv", newOwner: "current_user"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterConversionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterConversionStmt", stmts[0])
			}
			if ac.Name.Name != tc.name {
				t.Errorf("Name.Name = %q, want %q", ac.Name.Name, tc.name)
			}
			if ac.Action != "owner" {
				t.Errorf("Action = %q, want %q", ac.Action, "owner")
			}
			if ac.NewOwner != tc.newOwner {
				t.Errorf("NewOwner = %q, want %q", ac.NewOwner, tc.newOwner)
			}
		})
	}
}

// TestParseAlterConversionSetSchema pins the `ALTER CONVERSION name SET
// SCHEMA newschema` shape. Mirrors TestParseAlterCollationSetSchema.
func TestParseAlterConversionSetSchema(t *testing.T) {
	stmts, err := Parse("ALTER CONVERSION myconv SET SCHEMA other")
	if err != nil {
		t.Fatalf("Parse (SET SCHEMA): %v", err)
	}
	ac, ok := stmts[0].(*AlterConversionStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterConversionStmt", stmts[0])
	}
	if ac.Action != "setschema" {
		t.Errorf("Action = %q, want %q", ac.Action, "setschema")
	}
	if ac.NewSchema != "other" {
		t.Errorf("NewSchema = %q, want %q", ac.NewSchema, "other")
	}
}
