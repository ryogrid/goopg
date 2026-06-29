package catalog

import "testing"

// relaclText is a tiny test helper that takes c.mu around relaclTextLocked the
// same way the pg_class VirtualRows builder does.
func relaclText(c *InMemory, relOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLocked(relOID)
}

// relaclTextSeq is relaclText for a sequence (relkind 'S').
func relaclTextSeq(c *InMemory, relOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedSeq(relOID)
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

// TestRelaclTextPublic pins the PUBLIC pseudo-role projection that lets a
// GRANT … TO PUBLIC round-trip through pg_dump (DU-002 slice 334). PostgreSQL
// stores a grant to PUBLIC with an empty grantee in the aclitem
// ("=<privs>/postgres"), and pg_dump's buildACLCommands renders an empty
// grantee as the keyword PUBLIC. goopg records the grant under the reserved
// role name "public" (case-insensitively), which must materialize as the empty
// grantee.
func TestRelaclTextPublic(t *testing.T) {
	c := NewInMemory()
	const relOID = 16700

	// GRANT SELECT TO PUBLIC materializes the owner entry plus an empty-grantee
	// entry ("=r/postgres"), NOT "public=r/postgres".
	c.GrantTablePrivilege(relOID, "PUBLIC", "SELECT")
	want := "{postgres=arwdDxtm/postgres,=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT SELECT TO PUBLIC = %q; want %q", got, want)
	}

	// A named grantee and PUBLIC coexist; the named role keeps its name and
	// PUBLIC stays the empty grantee. Sort order places "" (PUBLIC, stored as
	// "public") after "named_role".
	c.GrantTablePrivilege(relOID, "named_role", "INSERT")
	want = "{postgres=arwdDxtm/postgres,named_role=a/postgres,=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl with PUBLIC + named grantee = %q; want %q", got, want)
	}
}

// TestRelaclTextSequence pins the sequence relacl projection that lets a
// GRANT … ON SEQUENCE round-trip through pg_dump (DU-002 slice 333). A
// sequence's owner default is "rwU" (USAGE/SELECT/UPDATE), which pg_dump diffs
// against via acldefault('s', owner); grantee privileges render in canonical
// aclitemout order SELECT('r') < UPDATE('w') < USAGE('U').
func TestRelaclTextSequence(t *testing.T) {
	c := NewInMemory()
	const seqOID = 16600

	// No grants → NULL relacl.
	if got := relaclTextSeq(c, seqOID); got != "" {
		t.Fatalf("seq relacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT USAGE materializes the owner entry (rwU) plus the grantee ("U").
	c.GrantTablePrivilege(seqOID, "seq_role", "USAGE")
	want := "{postgres=rwU/postgres,seq_role=U/postgres}"
	if got := relaclTextSeq(c, seqOID); got != want {
		t.Fatalf("seq relacl after GRANT USAGE = %q; want %q", got, want)
	}

	// Multiple privileges render in canonical order: SELECT('r') before
	// USAGE('U'); table-only privileges (e.g. INSERT) are not part of the
	// sequence order and are dropped from the rendering.
	c.GrantTablePrivilege(seqOID, "seq_role", "SELECT")
	c.GrantTablePrivilege(seqOID, "seq_role", "INSERT")
	want = "{postgres=rwU/postgres,seq_role=rU/postgres}"
	if got := relaclTextSeq(c, seqOID); got != want {
		t.Fatalf("seq relacl after multi-priv GRANT = %q; want %q", got, want)
	}

	// A grant-option sequence privilege renders "<letter>*".
	c.GrantTablePrivilegeWithGrantOption(seqOID, "seq_role", "UPDATE", true)
	want = "{postgres=rwU/postgres,seq_role=rw*U/postgres}"
	if got := relaclTextSeq(c, seqOID); got != want {
		t.Fatalf("seq relacl after GRANT UPDATE WITH GRANT OPTION = %q; want %q", got, want)
	}
}
