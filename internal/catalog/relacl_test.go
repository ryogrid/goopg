package catalog

import "testing"

// relaclText is a tiny test helper that takes c.mu around relaclTextLocked the
// same way the pg_class VirtualRows builder does.
func relaclText(c *InMemory, relOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLocked(relOID)
}

// TestRelaclText pins the pg_class.relacl projection that lets a table-level
// GRANT round-trip through pg_dump (DU-002 slice 331). PostgreSQL leaves relacl
// NULL until the first GRANT, then materializes an aclitem[] with the owner's
// full default privileges first (grantor = owner), followed by each grantee's
// entry; pg_dump diffs this against acldefault('r', owner) and re-emits the
// GRANT for the grantee only. goopg's single bootstrap superuser "postgres"
// (OID 10) is every table's owner and grantor.
func TestRelaclText(t *testing.T) {
	c := NewInMemory()
	const relOID = 16400

	// No grants → NULL relacl (matches acldefault, so pg_dump emits no GRANT).
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl with no grants = %q; want \"\" (NULL)", got)
	}

	// A single SELECT grant materializes the owner entry plus the grantee.
	c.GrantTablePrivilege(relOID, "grantee_role", "SELECT")
	want := "{postgres=arwdDxtm/postgres,grantee_role=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT SELECT = %q; want %q", got, want)
	}

	// Multiple privileges on one grantee render in canonical ACL order
	// ("arwdDxtm"): INSERT('a') before SELECT('r') before UPDATE('w').
	c.GrantTablePrivilege(relOID, "grantee_role", "INSERT")
	c.GrantTablePrivilege(relOID, "grantee_role", "UPDATE")
	want = "{postgres=arwdDxtm/postgres,grantee_role=arw/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after multi-priv GRANT = %q; want %q", got, want)
	}

	// A second grantee sorts deterministically after the first; the owner entry
	// stays at the head.
	c.GrantTablePrivilege(relOID, "another_role", "DELETE")
	want = "{postgres=arwdDxtm/postgres,another_role=d/postgres,grantee_role=arw/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl with two grantees = %q; want %q", got, want)
	}

	// DropTableACL reverts relacl back to NULL.
	c.DropTableACL(relOID)
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl after DropTableACL = %q; want \"\" (NULL)", got)
	}
}

// TestRelaclTextGrantOption pins the grant-option (`*`) projection that lets a
// GRANT … WITH GRANT OPTION round-trip through pg_dump (DU-002 slice 332).
// aclitemout renders a grant-option privilege as "<letter>*" (e.g. "r*" for
// SELECT WITH GRANT OPTION); pg_dump's buildACLCommands splits those into a
// separate `GRANT … WITH GRANT OPTION;`.
func TestRelaclTextGrantOption(t *testing.T) {
	c := NewInMemory()
	const relOID = 16500

	// SELECT WITH GRANT OPTION renders "r*".
	c.GrantTablePrivilegeWithGrantOption(relOID, "g_role", "SELECT", true)
	want := "{postgres=arwdDxtm/postgres,g_role=r*/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT SELECT WITH GRANT OPTION = %q; want %q", got, want)
	}

	// A plain GRANT of another privilege (no option) on the same grantee renders
	// the new letter without a star; the option on SELECT is retained, and the
	// letters keep canonical ACL order (INSERT 'a' before SELECT 'r*').
	c.GrantTablePrivilege(relOID, "g_role", "INSERT")
	want = "{postgres=arwdDxtm/postgres,g_role=ar*/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after mixed-option GRANT = %q; want %q", got, want)
	}

	// A later plain GRANT of an already-option privilege must NOT clear the
	// option (PostgreSQL keeps it until REVOKE GRANT OPTION FOR).
	c.GrantTablePrivilege(relOID, "g_role", "SELECT")
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after redundant plain GRANT = %q; want %q (option retained)", got, want)
	}
}
