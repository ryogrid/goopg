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

// TestNamespaceACLText pins the pg_namespace.nspacl projection that lets a
// GRANT … ON SCHEMA round-trip through pg_dump (DU-002 slice 335). A schema's
// owner default is "UC" (USAGE/CREATE; schemas grant PUBLIC nothing by
// default), which pg_dump diffs against via acldefault('n', owner); grantee
// privileges render in canonical aclitemout order USAGE('U') < CREATE('C').
// Schemas share the OID-keyed ACL store with relations, so the same
// GrantTablePrivilege* recorders apply.
func TestNamespaceACLText(t *testing.T) {
	c := NewInMemory()
	const schemaOID = 16800

	// No grants → NULL nspacl (matches acldefault, so pg_dump emits no GRANT).
	if got := c.NamespaceACLText(schemaOID); got != "" {
		t.Fatalf("nspacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT USAGE materializes the owner entry (UC) plus the grantee ("U").
	c.GrantTablePrivilege(schemaOID, "schema_role", "USAGE")
	want := "{postgres=UC/postgres,schema_role=U/postgres}"
	if got := c.NamespaceACLText(schemaOID); got != want {
		t.Fatalf("nspacl after GRANT USAGE = %q; want %q", got, want)
	}

	// Multiple privileges render in canonical order USAGE('U') before CREATE('C');
	// table/sequence-only privileges (e.g. SELECT) are not part of the schema
	// order and are dropped from the rendering.
	c.GrantTablePrivilege(schemaOID, "schema_role", "CREATE")
	c.GrantTablePrivilege(schemaOID, "schema_role", "SELECT")
	want = "{postgres=UC/postgres,schema_role=UC/postgres}"
	if got := c.NamespaceACLText(schemaOID); got != want {
		t.Fatalf("nspacl after multi-priv GRANT = %q; want %q", got, want)
	}

	// A grant-option schema privilege renders "<letter>*".
	c.GrantTablePrivilegeWithGrantOption(schemaOID, "schema_role", "CREATE", true)
	want = "{postgres=UC/postgres,schema_role=UC*/postgres}"
	if got := c.NamespaceACLText(schemaOID); got != want {
		t.Fatalf("nspacl after GRANT CREATE WITH GRANT OPTION = %q; want %q", got, want)
	}

	// GRANT … ON SCHEMA … TO PUBLIC materializes the empty grantee.
	const pubSchemaOID = 16801
	c.GrantTablePrivilege(pubSchemaOID, "PUBLIC", "USAGE")
	want = "{postgres=UC/postgres,=U/postgres}"
	if got := c.NamespaceACLText(pubSchemaOID); got != want {
		t.Fatalf("nspacl after GRANT USAGE TO PUBLIC = %q; want %q", got, want)
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

// TestRelaclTextQuotedGrantee pins the aclitemout/putid name-quoting that lets a
// GRANT to a role whose name contains characters outside [A-Za-z0-9_] round-trip
// through pg_dump (DU-002 slice 336). PostgreSQL double-quotes such a grantee in
// the aclitem text (doubling an internal quote); pg_dump's getid parser relies on
// that to read the whole name. An all-alnum/underscore name and the empty PUBLIC
// grantee are emitted verbatim.
func TestRelaclTextQuotedGrantee(t *testing.T) {
	c := NewInMemory()
	const relOID = 16800

	// A hyphenated role name must be double-quoted: "weird-role"=r/postgres.
	c.GrantTablePrivilege(relOID, "weird-role", "SELECT")
	want := `{postgres=arwdDxtm/postgres,"weird-role"=r/postgres}`
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT to hyphenated role = %q; want %q", got, want)
	}
	c.DropTableACL(relOID)

	// A plain alphanumeric/underscore name is never quoted; PUBLIC stays the
	// (unquoted) empty grantee even though they sort/coexist.
	c.GrantTablePrivilege(relOID, "plain_role", "INSERT")
	c.GrantTablePrivilege(relOID, "PUBLIC", "SELECT")
	want = "{postgres=arwdDxtm/postgres,plain_role=a/postgres,=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl with plain role + PUBLIC = %q; want %q", got, want)
	}
}

// TestRelaclTextMixedCaseGrantee verifies that a grantee spelled with a
// case-significant (double-quoted) name preserves its exact case in relacl,
// even though the ACL store keys privileges by the lower-cased name for
// case-insensitive lookups. PostgreSQL role names are case-significant when
// double-quoted; aclitemout renders the role's true name, and pg_dump's
// getid/fmtId re-emit GRANT … TO "MixedCase". DU-002 slice 337.
func TestRelaclTextMixedCaseGrantee(t *testing.T) {
	c := NewInMemory()
	const relOID = 16801

	// A mixed-case name is all-alnum, so putid leaves it BARE in the aclitem
	// (no double quotes) but must preserve the original case: MixedCase=r/...
	c.GrantTablePrivilege(relOID, "MixedCase", "SELECT")
	want := "{postgres=arwdDxtm/postgres,MixedCase=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT to mixed-case role = %q; want %q", got, want)
	}

	// A case-insensitive lookup still resolves the lower-cased key.
	if !c.HasTablePrivilege(relOID, "mixedcase", "SELECT") {
		t.Fatalf("HasTablePrivilege(mixedcase) = false; want true")
	}
	if !c.HasTablePrivilege(relOID, "MIXEDCASE", "SELECT") {
		t.Fatalf("HasTablePrivilege(MIXEDCASE) = false; want true")
	}
	c.DropTableACL(relOID)

	// Mixed case AND a quoting-required character compose: the name is both
	// double-quoted (hyphen) and case-preserved.
	c.GrantTablePrivilege(relOID, "Weird-Role", "SELECT")
	want = `{postgres=arwdDxtm/postgres,"Weird-Role"=r/postgres}`
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT to quoted mixed-case role = %q; want %q", got, want)
	}
}

// TestRevokeTablePrivilege pins the REVOKE recording that lets a GRANT followed
// by a partial REVOKE round-trip through pg_dump (DU-002 slice 338). Revoking
// one privilege of a multi-privilege grantee leaves the remaining letters; once
// the grantee's set is empty its aclitem disappears, and once no grantees remain
// the relacl falls back to NULL (matching acldefault, so pg_dump emits nothing).
func TestRevokeTablePrivilege(t *testing.T) {
	c := NewInMemory()
	const relOID = 16900

	// GRANT SELECT, INSERT → grantee carries both letters ("ar").
	c.GrantTablePrivilege(relOID, "grantee_role", "SELECT")
	c.GrantTablePrivilege(relOID, "grantee_role", "INSERT")
	want := "{postgres=arwdDxtm/postgres,grantee_role=ar/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after GRANT SELECT,INSERT = %q; want %q", got, want)
	}

	// REVOKE INSERT drops 'a', leaving the lone SELECT ('r').
	c.RevokeTablePrivilege(relOID, "grantee_role", "INSERT")
	want = "{postgres=arwdDxtm/postgres,grantee_role=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after REVOKE INSERT = %q; want %q", got, want)
	}
	if c.HasTablePrivilege(relOID, "grantee_role", "INSERT") {
		t.Fatalf("HasTablePrivilege(INSERT) after REVOKE = true; want false")
	}
	if !c.HasTablePrivilege(relOID, "grantee_role", "SELECT") {
		t.Fatalf("HasTablePrivilege(SELECT) after REVOKE INSERT = false; want true")
	}

	// Revoking a privilege the role never held is a no-op (DELETE was never
	// granted) — the relacl is unchanged.
	c.RevokeTablePrivilege(relOID, "grantee_role", "DELETE")
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after no-op REVOKE = %q; want %q", got, want)
	}

	// REVOKE the last remaining privilege drops the grantee entry entirely, and
	// with no grantees left the relacl returns to NULL.
	c.RevokeTablePrivilege(relOID, "grantee_role", "SELECT")
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl after REVOKE of last priv = %q; want \"\" (NULL)", got)
	}

	// A case-insensitive grantee resolves through the revoke too: GRANT under the
	// original case, REVOKE under a different case removes it.
	c.GrantTablePrivilege(relOID, "MixedCase", "SELECT")
	c.RevokeTablePrivilege(relOID, "MIXEDCASE", "SELECT")
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl after case-insensitive REVOKE = %q; want \"\" (NULL)", got)
	}
}

// TestMaterializeOwnerACL pins the owner-side revoke-of-default recording that
// lets a `REVOKE <priv> ON TABLE t FROM postgres` round-trip through pg_dump
// (DU-002 slice 340). PostgreSQL leaves relacl NULL while the owner holds its
// implicit default privileges; the first owner-side REVOKE materializes the
// owner's full default set, so the relacl renders the owner's default minus the
// revoked bits and pg_dump re-emits the equivalent REVOKE/GRANT pair.
func TestMaterializeOwnerACL(t *testing.T) {
	c := NewInMemory()
	const relOID = 16950

	// No grants yet → NULL relacl.
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl before any grant = %q; want \"\" (NULL)", got)
	}

	// REVOKE TRIGGER FROM postgres: materialize the owner's full table default
	// ("arwdDxtm"), then drop TRIGGER ('t') → "arwdDxm".
	c.MaterializeOwnerACL(relOID, "postgres", []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"})
	c.RevokeTablePrivilege(relOID, "postgres", "TRIGGER")
	want := "{postgres=arwdDxm/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after REVOKE TRIGGER FROM postgres = %q; want %q", got, want)
	}

	// A second owner-side REVOKE must NOT re-materialize (clobber) the first: the
	// owner entry already exists, so MaterializeOwnerACL is a no-op and only the
	// new privilege is dropped. REVOKE MAINTAIN ('m') → "arwdDx".
	c.MaterializeOwnerACL(relOID, "postgres", []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"})
	c.RevokeTablePrivilege(relOID, "postgres", "MAINTAIN")
	want = "{postgres=arwdDx/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after second owner REVOKE = %q; want %q", got, want)
	}

	// A grantee added alongside the reduced owner entry still renders after the
	// owner (owner is always first), and the owner's reduced set is preserved.
	c.GrantTablePrivilege(relOID, "grantee_role", "SELECT")
	want = "{postgres=arwdDx/postgres,grantee_role=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl with owner-revoke + grantee = %q; want %q", got, want)
	}
}

// TestACLQuoteName unit-checks the putid emulation directly, including the
// double-quote-doubling and multibyte (high-bit) cases that are awkward to drive
// through a full GRANT.
func TestACLQuoteName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},                      // PUBLIC pseudo-grantee
		{"postgres", "postgres"},      // owner / common case
		{"plain_role99", "plain_role99"},
		{"weird-role", `"weird-role"`}, // hyphen
		{"has space", `"has space"`},   // space
		{`a"b`, `"a""b"`},              // internal quote doubled
		{"café", `"café"`},             // multibyte (high-bit) byte → unsafe
	}
	for _, tc := range cases {
		if got := aclQuoteName(tc.in); got != tc.want {
			t.Errorf("aclQuoteName(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
