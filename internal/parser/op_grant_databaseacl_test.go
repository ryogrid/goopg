package parser

import (
	"testing"
)

// TestParseGrantDatabaseACL verifies that a GRANT/REVOKE … ON DATABASE …
// statement is captured into CompatNoopStmt.DatabaseACLChange with its
// privilege list, database names, grantees, and grant-option flag, while the
// pre-existing DatabaseACL bool (intra-grant-inplace xmax lock-wait,
// M0118-0098) stays set unconditionally. M0119-0004-ACLHEAP (datacl half).
func TestParseGrantDatabaseACL(t *testing.T) {
	cases := []struct {
		sql        string
		wantRevoke bool
		wantPrivs  []string
		wantNames  []string
		wantRoles  []string
		wantWGO    bool
	}{
		{
			sql:       "GRANT CREATE ON DATABASE postgres TO r",
			wantPrivs: []string{"CREATE"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r"},
		},
		{
			sql:        "REVOKE CONNECT ON DATABASE postgres FROM PUBLIC",
			wantRevoke: true,
			wantPrivs:  []string{"CONNECT"},
			wantNames:  []string{"postgres"},
			wantRoles:  []string{"public"}, // lexer lower-cases the unquoted PUBLIC keyword
		},
		{
			sql:       "GRANT ALL ON DATABASE postgres TO r",
			wantPrivs: []string{"ALL"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT ALL PRIVILEGES ON DATABASE postgres TO r",
			wantPrivs: []string{"ALL PRIVILEGES"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT TEMP ON DATABASE postgres TO r WITH GRANT OPTION",
			wantPrivs: []string{"TEMP"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r"},
			wantWGO:   true,
		},
		{
			sql:       "GRANT CREATE ON DATABASE postgres, template1 TO r",
			wantPrivs: []string{"CREATE"},
			wantNames: []string{"postgres", "template1"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT CREATE ON DATABASE postgres TO r1, r2",
			wantPrivs: []string{"CREATE"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r1", "r2"},
		},
		{
			sql:        "REVOKE CREATE ON DATABASE postgres FROM r CASCADE",
			wantRevoke: true,
			wantPrivs:  []string{"CREATE"},
			wantNames:  []string{"postgres"},
			wantRoles:  []string{"r"},
		},
		{
			sql:       "GRANT CREATE ON DATABASE postgres TO r GRANTED BY postgres",
			wantPrivs: []string{"CREATE"},
			wantNames: []string{"postgres"},
			wantRoles: []string{"r"},
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", tc.sql, stmts[0])
		}
		if !ns.DatabaseACL {
			t.Errorf("%q: DatabaseACL (bool) = false, want true", tc.sql)
		}
		if ns.DatabaseACLChange == nil {
			t.Fatalf("%q: DatabaseACLChange is nil, want populated", tc.sql)
		}
		got := ns.DatabaseACLChange
		if got.Revoke != tc.wantRevoke {
			t.Errorf("%q: Revoke = %v, want %v", tc.sql, got.Revoke, tc.wantRevoke)
		}
		if got.WithGrantOption != tc.wantWGO {
			t.Errorf("%q: WithGrantOption = %v, want %v", tc.sql, got.WithGrantOption, tc.wantWGO)
		}
		if !eqStrs(got.Privileges, tc.wantPrivs) {
			t.Errorf("%q: Privileges = %v, want %v", tc.sql, got.Privileges, tc.wantPrivs)
		}
		if !eqStrs(got.Grantees, tc.wantRoles) {
			t.Errorf("%q: Grantees = %v, want %v", tc.sql, got.Grantees, tc.wantRoles)
		}
		if !eqStrs(got.DatabaseNames, tc.wantNames) {
			t.Errorf("%q: DatabaseNames = %v, want %v", tc.sql, got.DatabaseNames, tc.wantNames)
		}
	}
}

// TestParseGrantNonDatabaseLeavesDatabaseACLChangeNil verifies the capture is
// scoped strictly to ON DATABASE — every other object class leaves
// DatabaseACLChange nil so the existing virtual-path recorders are
// unaffected.
func TestParseGrantNonDatabaseLeavesDatabaseACLChangeNil(t *testing.T) {
	for _, sql := range []string{
		"GRANT SELECT ON TABLE foo TO bar",
		"GRANT SELECT ON intra_grant_inplace TO PUBLIC",
		"REVOKE SELECT ON public.foo FROM bar",
		"GRANT USAGE ON TYPE t TO bar",
		"GRANT USAGE ON SCHEMA s TO bar",
		"GRANT USAGE ON SEQUENCE seq1 TO bar",
		"GRANT EXECUTE ON FUNCTION public.f(integer) TO bar",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", sql, stmts[0])
		}
		if ns.DatabaseACLChange != nil {
			t.Errorf("%q: DatabaseACLChange = %+v, want nil", sql, ns.DatabaseACLChange)
		}
	}
}
