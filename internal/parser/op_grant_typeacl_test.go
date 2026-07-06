package parser

import (
	"testing"
)

// TestParseGrantTypeACL verifies that a GRANT/REVOKE … ON TYPE|DOMAIN …
// statement is captured into CompatNoopStmt.TypeACL with its privilege list,
// type names, grantees, grant-option flag, and TYPE-vs-DOMAIN class, while
// leaving TableACL empty (pg_type is not pg_class). Every other object class
// leaves TypeACL nil. M0119-0004-ACLHEAP.
func TestParseGrantTypeACL(t *testing.T) {
	cases := []struct {
		sql           string
		wantRevoke    bool
		wantDomain    bool
		wantPrivs     []string
		wantNames     []ObjectName
		wantRoles     []string
		wantWGO       bool
		wantGrantedBy string
	}{
		{
			sql:       "GRANT USAGE ON TYPE public.t TO r",
			wantPrivs: []string{"USAGE"},
			wantNames: []ObjectName{{Schema: "public", Name: "t"}},
			wantRoles: []string{"r"},
		},
		{
			sql:        "GRANT USAGE ON DOMAIN d TO r",
			wantDomain: true,
			wantPrivs:  []string{"USAGE"},
			wantNames:  []ObjectName{{Name: "d"}},
			wantRoles:  []string{"r"},
		},
		{
			sql:        "REVOKE USAGE ON TYPE t FROM PUBLIC",
			wantRevoke: true,
			wantPrivs:  []string{"USAGE"},
			wantNames:  []ObjectName{{Name: "t"}},
			wantRoles:  []string{"public"}, // lexer lower-cases the unquoted PUBLIC keyword
		},
		{
			sql:       "GRANT ALL ON TYPE t TO r",
			wantPrivs: []string{"ALL"},
			wantNames: []ObjectName{{Name: "t"}},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT ALL PRIVILEGES ON TYPE t TO r",
			wantPrivs: []string{"ALL PRIVILEGES"},
			wantNames: []ObjectName{{Name: "t"}},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT USAGE ON TYPE t TO r WITH GRANT OPTION",
			wantPrivs: []string{"USAGE"},
			wantNames: []ObjectName{{Name: "t"}},
			wantRoles: []string{"r"},
			wantWGO:   true,
		},
		{
			sql:       "GRANT USAGE ON TYPE a, public.b TO r",
			wantPrivs: []string{"USAGE"},
			wantNames: []ObjectName{{Name: "a"}, {Schema: "public", Name: "b"}},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT USAGE ON TYPE t TO r1, r2",
			wantPrivs: []string{"USAGE"},
			wantNames: []ObjectName{{Name: "t"}},
			wantRoles: []string{"r1", "r2"},
		},
		{
			sql:        "REVOKE USAGE ON TYPE t FROM r CASCADE",
			wantRevoke: true,
			wantPrivs:  []string{"USAGE"},
			wantNames:  []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			sql:           "GRANT USAGE ON TYPE t TO r GRANTED BY postgres",
			wantPrivs:     []string{"USAGE"},
			wantNames:     []ObjectName{{Name: "t"}},
			wantRoles:     []string{"r"},
			wantGrantedBy: "postgres",
		},
		{
			// GRANTED BY following WITH GRANT OPTION (gram.y's
			// opt_grant_grant_option opt_granted_by order) — regression case
			// for scanGrantTrailingClause's continue-after-WITH fix.
			sql:           "GRANT USAGE ON TYPE t TO r WITH GRANT OPTION GRANTED BY postgres",
			wantPrivs:     []string{"USAGE"},
			wantNames:     []ObjectName{{Name: "t"}},
			wantRoles:     []string{"r"},
			wantWGO:       true,
			wantGrantedBy: "postgres",
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
		if ns.TypeACL == nil {
			t.Fatalf("%q: TypeACL is nil, want populated", tc.sql)
		}
		// ON TYPE/DOMAIN is never a pg_class ACL change.
		if ns.TableACL != "" {
			t.Errorf("%q: TableACL = %q, want empty", tc.sql, ns.TableACL)
		}
		got := ns.TypeACL
		if got.Revoke != tc.wantRevoke {
			t.Errorf("%q: Revoke = %v, want %v", tc.sql, got.Revoke, tc.wantRevoke)
		}
		if got.IsDomain != tc.wantDomain {
			t.Errorf("%q: IsDomain = %v, want %v", tc.sql, got.IsDomain, tc.wantDomain)
		}
		if got.WithGrantOption != tc.wantWGO {
			t.Errorf("%q: WithGrantOption = %v, want %v", tc.sql, got.WithGrantOption, tc.wantWGO)
		}
		if got.GrantedBy != tc.wantGrantedBy {
			t.Errorf("%q: GrantedBy = %q, want %q", tc.sql, got.GrantedBy, tc.wantGrantedBy)
		}
		if !eqStrs(got.Privileges, tc.wantPrivs) {
			t.Errorf("%q: Privileges = %v, want %v", tc.sql, got.Privileges, tc.wantPrivs)
		}
		if !eqStrs(got.Grantees, tc.wantRoles) {
			t.Errorf("%q: Grantees = %v, want %v", tc.sql, got.Grantees, tc.wantRoles)
		}
		if len(got.TypeNames) != len(tc.wantNames) {
			t.Fatalf("%q: TypeNames = %v, want %v", tc.sql, got.TypeNames, tc.wantNames)
		}
		for i := range tc.wantNames {
			if got.TypeNames[i].Schema != tc.wantNames[i].Schema || got.TypeNames[i].Name != tc.wantNames[i].Name {
				t.Errorf("%q: TypeNames[%d] = %+v, want %+v", tc.sql, i, got.TypeNames[i], tc.wantNames[i])
			}
		}
	}
}

// TestParseGrantNonTypeLeavesTypeACLNil verifies the capture is scoped strictly
// to ON TYPE|DOMAIN — every other object class (and a default/bare table grant)
// leaves TypeACL nil so the existing virtual-path recorders are unaffected.
func TestParseGrantNonTypeLeavesTypeACLNil(t *testing.T) {
	for _, sql := range []string{
		"GRANT SELECT ON TABLE foo TO bar",
		"GRANT SELECT ON intra_grant_inplace TO PUBLIC",
		"REVOKE SELECT ON public.foo FROM bar",
		"GRANT TEMP ON DATABASE postgres TO PUBLIC",
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
		if ns.TypeACL != nil {
			t.Errorf("%q: TypeACL = %+v, want nil", sql, ns.TypeACL)
		}
	}
}
