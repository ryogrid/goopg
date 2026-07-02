package parser

import (
	"testing"
)

// TestParseGrantRoleMembership verifies that a `GRANT <role> TO <role>`
// statement (no `ON <object>` clause at all) is captured into
// CompatNoopStmt.RoleMembership, distinct from every other GRANT variant.
// M0119-0004-ACLHEAP.
func TestParseGrantRoleMembership(t *testing.T) {
	cases := []struct {
		sql           string
		wantRevoke    bool
		wantAdminOnly bool
		wantRoles     []string
		wantGrantees  []string
		wantWithAdmin bool
		wantGrantedBy string
	}{
		{
			sql:          "GRANT admin TO alice",
			wantRoles:    []string{"admin"},
			wantGrantees: []string{"alice"},
		},
		{
			sql:          "GRANT admin TO alice, bob",
			wantRoles:    []string{"admin"},
			wantGrantees: []string{"alice", "bob"},
		},
		{
			sql:          "GRANT admin, ops TO alice",
			wantRoles:    []string{"admin", "ops"},
			wantGrantees: []string{"alice"},
		},
		{
			sql:           "GRANT admin TO alice WITH ADMIN OPTION",
			wantRoles:     []string{"admin"},
			wantGrantees:  []string{"alice"},
			wantWithAdmin: true,
		},
		{
			sql:           "GRANT admin TO alice GRANTED BY postgres",
			wantRoles:     []string{"admin"},
			wantGrantees:  []string{"alice"},
			wantGrantedBy: "postgres",
		},
		{
			sql:        "REVOKE admin FROM alice",
			wantRevoke: true,
			wantRoles:  []string{"admin"},
			wantGrantees: []string{
				"alice",
			},
		},
		{
			sql:           "REVOKE ADMIN OPTION FOR admin FROM alice",
			wantRevoke:    true,
			wantAdminOnly: true,
			wantRoles:     []string{"admin"},
			wantGrantees:  []string{"alice"},
		},
		{
			sql:          "REVOKE admin FROM alice CASCADE",
			wantRevoke:   true,
			wantRoles:    []string{"admin"},
			wantGrantees: []string{"alice"},
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
		if ns.RoleMembership == nil {
			t.Fatalf("%q: RoleMembership is nil, want populated", tc.sql)
		}
		got := ns.RoleMembership
		if got.Revoke != tc.wantRevoke {
			t.Errorf("%q: Revoke = %v, want %v", tc.sql, got.Revoke, tc.wantRevoke)
		}
		if got.AdminOptionOnly != tc.wantAdminOnly {
			t.Errorf("%q: AdminOptionOnly = %v, want %v", tc.sql, got.AdminOptionOnly, tc.wantAdminOnly)
		}
		if got.WithAdminOption != tc.wantWithAdmin {
			t.Errorf("%q: WithAdminOption = %v, want %v", tc.sql, got.WithAdminOption, tc.wantWithAdmin)
		}
		if got.GrantedBy != tc.wantGrantedBy {
			t.Errorf("%q: GrantedBy = %q, want %q", tc.sql, got.GrantedBy, tc.wantGrantedBy)
		}
		if !eqStrs(got.Roles, tc.wantRoles) {
			t.Errorf("%q: Roles = %v, want %v", tc.sql, got.Roles, tc.wantRoles)
		}
		if !eqStrs(got.Grantees, tc.wantGrantees) {
			t.Errorf("%q: Grantees = %v, want %v", tc.sql, got.Grantees, tc.wantGrantees)
		}
	}
}

// TestParseGrantOnObjectLeavesRoleMembershipNil verifies the capture is
// scoped strictly to the no-`ON`-clause form — every ordinary object-privilege
// GRANT/REVOKE (which all require an `ON <object>` clause) leaves
// RoleMembership nil so the pre-existing ACL recorders are unaffected.
func TestParseGrantOnObjectLeavesRoleMembershipNil(t *testing.T) {
	for _, sql := range []string{
		"GRANT SELECT ON TABLE foo TO bar",
		"GRANT CREATE ON DATABASE postgres TO r",
		"GRANT USAGE ON TYPE mytype TO r",
		"GRANT SELECT (a) ON foo TO bar",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", sql, stmts[0])
		}
		if ns.RoleMembership != nil {
			t.Errorf("%q: RoleMembership = %+v, want nil", sql, ns.RoleMembership)
		}
	}
}
