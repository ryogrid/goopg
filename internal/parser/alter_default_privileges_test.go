package parser

import (
	"reflect"
	"testing"
)

// TestParseAlterDefaultPrivileges verifies that ALTER DEFAULT PRIVILEGES
// (gram.y's AlterDefaultPrivilegesStmt/DefACLAction) parses into a fully
// populated AlterDefaultPrivilegesStmt across the FOR ROLE/IN SCHEMA option
// combinations and every defacl_privilege_target keyword. M0110-0001
// (DU-002 slice 438 follow-up).
func TestParseAlterDefaultPrivileges(t *testing.T) {
	cases := []struct {
		sql  string
		want AlterDefaultPrivilegesStmt
	}{
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "table",
				Privileges: []string{"SELECT"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES FOR ROLE bob IN SCHEMA myschema GRANT SELECT, INSERT ON TABLES TO alice, PUBLIC",
			want: AlterDefaultPrivilegesStmt{
				Roles:      []string{"bob"},
				Schemas:    []string{"myschema"},
				ObjType:    "table",
				Privileges: []string{"SELECT", "INSERT"},
				Grantees:   []string{"alice", "public"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES IN SCHEMA s1, s2 FOR USER bob GRANT ALL ON SEQUENCES TO alice WITH GRANT OPTION",
			want: AlterDefaultPrivilegesStmt{
				Roles:           []string{"bob"},
				Schemas:         []string{"s1", "s2"},
				ObjType:         "sequence",
				Privileges:      []string{"ALL"},
				Grantees:        []string{"alice"},
				WithGrantOption: true,
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "function",
				Privileges: []string{"EXECUTE"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON ROUTINES TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "function",
				Privileges: []string{"EXECUTE"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT USAGE ON TYPES TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "type",
				Privileges: []string{"USAGE"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT USAGE ON SCHEMAS TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "schema",
				Privileges: []string{"USAGE"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES GRANT SELECT ON LARGE OBJECTS TO alice",
			want: AlterDefaultPrivilegesStmt{
				ObjType:    "largeobject",
				Privileges: []string{"SELECT"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES REVOKE SELECT ON TABLES FROM alice",
			want: AlterDefaultPrivilegesStmt{
				Revoke:     true,
				ObjType:    "table",
				Privileges: []string{"SELECT"},
				Grantees:   []string{"alice"},
			},
		},
		{
			sql: "ALTER DEFAULT PRIVILEGES REVOKE GRANT OPTION FOR SELECT ON TABLES FROM alice CASCADE",
			want: AlterDefaultPrivilegesStmt{
				Revoke:         true,
				GrantOptionFor: true,
				ObjType:        "table",
				Privileges:     []string{"SELECT"},
				Grantees:       []string{"alice"},
			},
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.sql, err)
		}
		got, ok := stmts[0].(*AlterDefaultPrivilegesStmt)
		if !ok {
			t.Fatalf("%q: expected *AlterDefaultPrivilegesStmt, got %T", tc.sql, stmts[0])
		}
		got.pos = 0
		if !reflect.DeepEqual(*got, tc.want) {
			t.Errorf("%q:\n got  %+v\n want %+v", tc.sql, *got, tc.want)
		}
	}
}
