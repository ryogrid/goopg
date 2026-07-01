package parser

import "testing"

// TestParseAlterPublicationOwner pins the `ALTER PUBLICATION name OWNER TO
// {role | CURRENT_USER | SESSION_USER | CURRENT_ROLE}` shape. M0119-0004
// (DU-002, loop #65 ledger follow-up).
func TestParseAlterPublicationOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER PUBLICATION mypub OWNER TO newrole", name: "mypub", newOwner: "newrole"},
		{sql: "ALTER PUBLICATION mypub OWNER TO CURRENT_USER", name: "mypub", newOwner: "current_user"},
		{sql: "ALTER PUBLICATION mypub OWNER TO SESSION_USER", name: "mypub", newOwner: "current_user"},
		{sql: "ALTER PUBLICATION mypub OWNER TO CURRENT_ROLE", name: "mypub", newOwner: "current_user"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterPublicationOwnerStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterPublicationOwnerStmt", stmts[0])
			}
			if ac.Name != tc.name {
				t.Errorf("Name = %q, want %q", ac.Name, tc.name)
			}
			if ac.NewOwner != tc.newOwner {
				t.Errorf("NewOwner = %q, want %q", ac.NewOwner, tc.newOwner)
			}
		})
	}
}

// TestParseAlterSubscriptionOwner is TestParseAlterPublicationOwner's ALTER
// SUBSCRIPTION counterpart.
func TestParseAlterSubscriptionOwner(t *testing.T) {
	sql := "ALTER SUBSCRIPTION mysub OWNER TO newrole"
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	ac, ok := stmts[0].(*AlterSubscriptionOwnerStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterSubscriptionOwnerStmt", stmts[0])
	}
	if ac.Name != "mysub" {
		t.Errorf("Name = %q, want %q", ac.Name, "mysub")
	}
	if ac.NewOwner != "newrole" {
		t.Errorf("NewOwner = %q, want %q", ac.NewOwner, "newrole")
	}
}

// TestParseAlterPublicationOtherFormsStillNoop confirms an unmodelled
// ALTER PUBLICATION/SUBSCRIPTION tail (RENAME TO, SET, ADD/DROP/SET TABLE)
// still parses without error as the pre-existing compatibility no-op,
// rather than erroring or being misrouted into the OWNER TO path.
func TestParseAlterPublicationOtherFormsStillNoop(t *testing.T) {
	cases := []string{
		"ALTER PUBLICATION mypub RENAME TO otherpub",
		"ALTER PUBLICATION mypub ADD TABLE t1",
		"ALTER PUBLICATION mypub SET (publish = 'insert')",
		"ALTER SUBSCRIPTION mysub RENAME TO othersub",
		"ALTER SUBSCRIPTION mysub SET PUBLICATION p2",
		"ALTER SUBSCRIPTION mysub REFRESH PUBLICATION",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			stmts, err := Parse(sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", sql, err)
			}
			if _, ok := stmts[0].(*AlterTableStmt); !ok {
				t.Fatalf("stmt type = %T, want *AlterTableStmt (no-op stub)", stmts[0])
			}
		})
	}
}
