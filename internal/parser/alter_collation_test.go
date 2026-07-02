package parser

import "testing"

// TestParseAlterCollationRename pins the `ALTER COLLATION [IF EXISTS] name
// RENAME TO newname` shape. M0119-0004 (DU-002, loop #50 ledger follow-up).
func TestParseAlterCollationRename(t *testing.T) {
	cases := []struct {
		sql      string
		schema   string
		name     string
		ifExists bool
		newName  string
	}{
		{sql: "ALTER COLLATION mycoll RENAME TO newcoll", name: "mycoll", newName: "newcoll"},
		{sql: "ALTER COLLATION public.mycoll RENAME TO newcoll", schema: "public", name: "mycoll", newName: "newcoll"},
		{sql: "ALTER COLLATION IF EXISTS mycoll RENAME TO newcoll", name: "mycoll", ifExists: true, newName: "newcoll"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterCollationStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterCollationStmt", stmts[0])
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

// TestParseAlterCollationOwner pins the `ALTER COLLATION name OWNER TO
// {role | CURRENT_USER | SESSION_USER | CURRENT_ROLE}` shape.
func TestParseAlterCollationOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER COLLATION mycoll OWNER TO newrole", name: "mycoll", newOwner: "newrole"},
		{sql: "ALTER COLLATION mycoll OWNER TO CURRENT_USER", name: "mycoll", newOwner: "current_user"},
		{sql: "ALTER COLLATION mycoll OWNER TO SESSION_USER", name: "mycoll", newOwner: "current_user"},
		{sql: "ALTER COLLATION mycoll OWNER TO CURRENT_ROLE", name: "mycoll", newOwner: "current_user"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterCollationStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterCollationStmt", stmts[0])
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

// TestParseAlterCollationRefreshVersion pins `ALTER COLLATION name REFRESH
// VERSION` and confirms an unmodelled trailing form (SET SCHEMA) still parses
// without error, as a no-op (Action == "").
func TestParseAlterCollationRefreshVersion(t *testing.T) {
	stmts, err := Parse("ALTER COLLATION mycoll REFRESH VERSION")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ac, ok := stmts[0].(*AlterCollationStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterCollationStmt", stmts[0])
	}
	if ac.Action != "refresh" {
		t.Errorf("Action = %q, want %q", ac.Action, "refresh")
	}

	stmts, err = Parse("ALTER COLLATION mycoll SET SCHEMA other")
	if err != nil {
		t.Fatalf("Parse (SET SCHEMA): %v", err)
	}
	ac, ok = stmts[0].(*AlterCollationStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterCollationStmt", stmts[0])
	}
	if ac.Action != "" {
		t.Errorf("Action = %q, want empty (unmodelled no-op)", ac.Action)
	}
}
