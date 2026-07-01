package parser

import "testing"

// TestParseAlterAggregateOwner pins the `ALTER AGGREGATE name(args) OWNER TO
// ...` parse shape (M0119-0004, loop #57 ledger follow-up). Previously this
// form fell into the "other ALTER AGGREGATE forms: consume as no-op" branch
// and parsed to a bare *AlterTableStmt, discarding both the target aggregate
// and the new owner.
func TestParseAlterAggregateOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER AGGREGATE newavg(int4) OWNER TO newrole", name: "newavg", newOwner: "newrole"},
		{sql: "ALTER AGGREGATE newavg(int4) OWNER TO CURRENT_USER", name: "newavg", newOwner: "current_user"},
		{sql: "ALTER AGGREGATE newavg(int4) OWNER TO SESSION_USER", name: "newavg", newOwner: "current_user"},
		{sql: "ALTER AGGREGATE newavg(int4) OWNER TO CURRENT_ROLE", name: "newavg", newOwner: "current_user"},
		{sql: "ALTER AGGREGATE zeroarg(*) OWNER TO newrole", name: "zeroarg", newOwner: "newrole"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ao, ok := stmts[0].(*AlterAggregateOwnerStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterAggregateOwnerStmt", stmts[0])
			}
			if ao.Name.Name != tc.name {
				t.Errorf("Name.Name = %q, want %q", ao.Name.Name, tc.name)
			}
			if ao.NewOwner != tc.newOwner {
				t.Errorf("NewOwner = %q, want %q", ao.NewOwner, tc.newOwner)
			}
		})
	}
}

// TestParseAlterAggregateRenameStillWorks guards against a regression where
// adding the OWNER TO branch shadows the pre-existing RENAME TO branch.
func TestParseAlterAggregateRenameStillWorks(t *testing.T) {
	stmts, err := Parse("ALTER AGGREGATE newavg(int4) RENAME TO renamedavg")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ar, ok := stmts[0].(*AlterAggregateRenameStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterAggregateRenameStmt", stmts[0])
	}
	if ar.OldName.Name != "newavg" || ar.NewName != "renamedavg" {
		t.Errorf("got OldName=%q NewName=%q, want newavg/renamedavg", ar.OldName.Name, ar.NewName)
	}
}
