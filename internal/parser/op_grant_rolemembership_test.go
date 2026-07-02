package parser

import (
	"testing"
)

// TestParseGrantRoleMembership verifies that a `GRANT <role> TO <role>`
// statement (no `ON <object>` clause at all) is captured into
// CompatNoopStmt.RoleMembership, distinct from every other GRANT variant.
// M0119-0004-ACLHEAP.
func TestParseGrantRoleMembership(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	cases := []struct {
		sql            string
		wantRevoke     bool
		wantAdminOnly  bool
		wantRoles      []string
		wantGrantees   []string
		wantWithAdmin  bool
		wantGrantedBy  string
		wantAdminOpt   *bool
		wantInheritOpt *bool
		wantSetOpt     *bool
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
			wantAdminOpt:  bp(true),
		},
		{
			sql:           "GRANT admin TO alice GRANTED BY postgres",
			wantRoles:     []string{"admin"},
			wantGrantees:  []string{"alice"},
			wantGrantedBy: "postgres",
		},
		{
			// grant_role_opt_list (gram.y): a comma-separated ADMIN/INHERIT/SET
			// run, each as OPTION/TRUE/FALSE, followed by GRANTED BY.
			sql:            "GRANT admin TO alice WITH INHERIT TRUE, SET FALSE GRANTED BY postgres",
			wantRoles:      []string{"admin"},
			wantGrantees:   []string{"alice"},
			wantGrantedBy:  "postgres",
			wantInheritOpt: bp(true),
			wantSetOpt:     bp(false),
		},
		{
			sql:            "GRANT admin TO alice WITH ADMIN FALSE, INHERIT FALSE",
			wantRoles:      []string{"admin"},
			wantGrantees:   []string{"alice"},
			wantAdminOpt:   bp(false),
			wantInheritOpt: bp(false),
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
	eqBoolPtr := func(a, b *bool) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return *a == *b
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
		if !eqBoolPtr(got.AdminOption, tc.wantAdminOpt) {
			t.Errorf("%q: AdminOption = %v, want %v", tc.sql, got.AdminOption, tc.wantAdminOpt)
		}
		if !eqBoolPtr(got.InheritOption, tc.wantInheritOpt) {
			t.Errorf("%q: InheritOption = %v, want %v", tc.sql, got.InheritOption, tc.wantInheritOpt)
		}
		if !eqBoolPtr(got.SetOption, tc.wantSetOpt) {
			t.Errorf("%q: SetOption = %v, want %v", tc.sql, got.SetOption, tc.wantSetOpt)
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
