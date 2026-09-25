package executor

import "testing"

// Every typed GRANT path rejects WITH GRANT OPTION to PUBLIC with PostgreSQL's
// 0LP01 (merge_acl_with_grant, aclchk.c:208 — shared by every object class and
// by ALTER DEFAULT PRIVILEGES), before recording anything. Expected SQLSTATE and
// text captured from a live PG 18.3; REVOKE and a role grantee are the controls.
func TestGrantOptionToPublicIsRejectedOnEveryTypedPath(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE t (a int)",
		"CREATE TYPE ty AS (x int)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	for _, stmt := range []string{
		"GRANT USAGE ON TYPE ty TO PUBLIC WITH GRANT OPTION",
		"GRANT SELECT (a) ON t TO PUBLIC WITH GRANT OPTION",
		"GRANT CONNECT ON DATABASE postgres TO PUBLIC WITH GRANT OPTION",
		"GRANT SET ON PARAMETER work_mem TO PUBLIC WITH GRANT OPTION",
		"ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO PUBLIC WITH GRANT OPTION",
		"GRANT USAGE ON TYPE ty TO postgres, PUBLIC WITH GRANT OPTION",
	} {
		err := runDDL(t, ctx, stmt)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "0LP01" || ee.Message != "grant options can only be granted to roles" {
			t.Errorf("%s: err = %v, want 0LP01 grant options can only be granted to roles", stmt, err)
		}
	}
	for _, stmt := range []string{
		"GRANT USAGE ON TYPE ty TO postgres WITH GRANT OPTION",
		"GRANT USAGE ON TYPE ty TO PUBLIC",
		"REVOKE GRANT OPTION FOR USAGE ON TYPE ty FROM PUBLIC",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Errorf("control %s: unexpected error %v", stmt, err)
		}
	}
}
