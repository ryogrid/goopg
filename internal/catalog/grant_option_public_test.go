package catalog

import "testing"

// PostgreSQL treats the RoleSpec keyword PUBLIC (any case unquoted, or the
// quoted lower-case "public") as the PUBLIC pseudo-role, and forbids WITH GRANT
// OPTION to it (aclchk.c:208). A quoted "PUBLIC" names an ordinary role.
func TestGrantOptionToPublic(t *testing.T) {
	for _, tc := range []struct {
		with     bool
		grantees []string
		want     bool
	}{
		{true, []string{"PUBLIC"}, true},
		{true, []string{"r1", "public"}, true},
		{true, []string{`"public"`}, true},
		{true, []string{`"PUBLIC"`}, false},
		{true, []string{"r1"}, false},
		{false, []string{"PUBLIC"}, false},
	} {
		if got := GrantOptionToPublic(tc.with, tc.grantees); got != tc.want {
			t.Errorf("GrantOptionToPublic(%v, %q) = %v, want %v", tc.with, tc.grantees, got, tc.want)
		}
	}
}
