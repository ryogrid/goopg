package parser

import (
	"testing"
)

// TestParseGrantParameterACL verifies that a GRANT/REVOKE … ON PARAMETER …
// statement is captured into CompatNoopStmt.ParameterACLChange with its
// privilege list, (lower-cased, dot-preserved) parameter names, grantees, and
// grant-option flag. M0119-0004-ACLHEAP (parameter ACL half).
func TestParseGrantParameterACL(t *testing.T) {
	cases := []struct {
		sql           string
		wantRevoke    bool
		wantPrivs     []string
		wantNames     []string
		wantRoles     []string
		wantWGO       bool
		wantGrantedBy string
	}{
		{
			sql:       "GRANT SET ON PARAMETER work_mem TO r",
			wantPrivs: []string{"SET"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r"},
		},
		{
			sql:        "REVOKE SET ON PARAMETER work_mem FROM PUBLIC",
			wantRevoke: true,
			wantPrivs:  []string{"SET"},
			wantNames:  []string{"work_mem"},
			wantRoles:  []string{"public"}, // lexer lower-cases the unquoted PUBLIC keyword
		},
		{
			sql:       "GRANT ALTER SYSTEM ON PARAMETER work_mem TO r",
			wantPrivs: []string{"ALTER SYSTEM"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT ALL ON PARAMETER work_mem TO r",
			wantPrivs: []string{"ALL"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT ALL PRIVILEGES ON PARAMETER work_mem TO r",
			wantPrivs: []string{"ALL PRIVILEGES"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r"},
		},
		// Dotted GUC name (custom-extension namespace): the dot must NOT be
		// split into a schema/name pair the way an object name would be.
		{
			sql:       "GRANT SET ON PARAMETER pgaudit.log TO r",
			wantPrivs: []string{"SET"},
			wantNames: []string{"pgaudit.log"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT SET ON PARAMETER work_mem, statement_timeout TO r",
			wantPrivs: []string{"SET"},
			wantNames: []string{"work_mem", "statement_timeout"},
			wantRoles: []string{"r"},
		},
		{
			sql:       "GRANT SET ON PARAMETER work_mem TO r1, r2",
			wantPrivs: []string{"SET"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r1", "r2"},
		},
		{
			sql:       "GRANT SET ON PARAMETER work_mem TO r WITH GRANT OPTION",
			wantPrivs: []string{"SET"},
			wantNames: []string{"work_mem"},
			wantRoles: []string{"r"},
			wantWGO:   true,
		},
		{
			sql:        "REVOKE SET ON PARAMETER work_mem FROM r CASCADE",
			wantRevoke: true,
			wantPrivs:  []string{"SET"},
			wantNames:  []string{"work_mem"},
			wantRoles:  []string{"r"},
		},
		{
			sql:           "GRANT SET ON PARAMETER work_mem TO r GRANTED BY postgres",
			wantPrivs:     []string{"SET"},
			wantNames:     []string{"work_mem"},
			wantRoles:     []string{"r"},
			wantGrantedBy: "postgres",
		},
		{
			sql:           "GRANT SET ON PARAMETER work_mem TO r WITH GRANT OPTION GRANTED BY postgres",
			wantPrivs:     []string{"SET"},
			wantNames:     []string{"work_mem"},
			wantRoles:     []string{"r"},
			wantWGO:       true,
			wantGrantedBy: "postgres",
		},
		// A mixed-case GUC name must be folded to lower case, mirroring
		// convert_GUC_name_for_parameter_acl.
		{
			sql:       `GRANT SET ON PARAMETER "Work_Mem" TO r`,
			wantPrivs: []string{"SET"},
			wantNames: []string{"work_mem"},
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
		if ns.ParameterACLChange == nil {
			t.Fatalf("%q: ParameterACLChange is nil, want populated", tc.sql)
		}
		got := ns.ParameterACLChange
		if got.Revoke != tc.wantRevoke {
			t.Errorf("%q: Revoke = %v, want %v", tc.sql, got.Revoke, tc.wantRevoke)
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
		if !eqStrs(got.ParamNames, tc.wantNames) {
			t.Errorf("%q: ParamNames = %v, want %v", tc.sql, got.ParamNames, tc.wantNames)
		}
	}
}

// TestParseGrantNonParameterLeavesParameterACLChangeNil verifies the capture
// is scoped strictly to ON PARAMETER — every other object class leaves
// ParameterACLChange nil so the existing recorders are unaffected.
func TestParseGrantNonParameterLeavesParameterACLChangeNil(t *testing.T) {
	for _, sql := range []string{
		"GRANT SELECT ON TABLE foo TO bar",
		"REVOKE SELECT ON public.foo FROM bar",
		"GRANT USAGE ON TYPE t TO bar",
		"GRANT USAGE ON SCHEMA s TO bar",
		"GRANT USAGE ON SEQUENCE seq1 TO bar",
		"GRANT EXECUTE ON FUNCTION public.f(integer) TO bar",
		"GRANT CREATE ON DATABASE postgres TO bar",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", sql, stmts[0])
		}
		if ns.ParameterACLChange != nil {
			t.Errorf("%q: ParameterACLChange = %+v, want nil", sql, ns.ParameterACLChange)
		}
	}
}
