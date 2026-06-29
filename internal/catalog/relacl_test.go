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

// TestRevokeAllFromOwnerEmptyArray covers DU-002 slice 341: a
// `REVOKE ALL … FROM owner` strips the owner's implicit default privileges and
// leaves relacl as a non-NULL *empty* aclitem array {} (distinct from the NULL
// state of a never-granted relation). pg_dump's buildACLCommands emits a bare
// `REVOKE ALL …` for {} but nothing for NULL, so the two must render
// differently. A subsequent GRANT re-materializes the array (clears {}), and a
// second owner-side revoke after the array is empty must not resurrect the
// owner's privileges.
func TestRevokeAllFromOwnerEmptyArray(t *testing.T) {
	c := NewInMemory()
	const relOID = 16970
	allTable := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"}

	// REVOKE ALL FROM postgres: materialize the owner default, then drop every
	// privilege → relacl is the empty array "{}", NOT NULL.
	c.MaterializeOwnerACL(relOID, "postgres", allTable)
	for _, p := range allTable {
		c.RevokeTablePrivilege(relOID, "postgres", p)
	}
	if got := relaclText(c, relOID); got != "{}" {
		t.Fatalf("relacl after REVOKE ALL FROM postgres = %q; want %q (empty array, not NULL)", got, "{}")
	}

	// A second owner-side revoke after the array is empty is a no-op: the owner
	// holds nothing, so MaterializeOwnerACL must not bring privileges back.
	c.MaterializeOwnerACL(relOID, "postgres", allTable)
	c.RevokeTablePrivilege(relOID, "postgres", "TRIGGER")
	if got := relaclText(c, relOID); got != "{}" {
		t.Fatalf("relacl after second owner revoke on empty array = %q; want %q", got, "{}")
	}

	// A relation that never had a grant stays NULL — the empty-array state must
	// not leak across OIDs.
	if got := relaclText(c, 16971); got != "" {
		t.Fatalf("relacl of unrelated never-granted relation = %q; want \"\" (NULL)", got)
	}

	// Re-granting a privilege to the owner re-materializes a real aclitem and
	// clears the empty-array state. (A grant to a *non-owner* after an owner
	// REVOKE ALL keeps the owner at zero, e.g. {bob=r/postgres}; that
	// owner-zero-coexists-with-grantee case is covered by
	// TestRevokeAllFromOwnerThenGrantGrantee — DU-002 slice 344.)
	c.GrantTablePrivilege(relOID, "postgres", "SELECT")
	if got := relaclText(c, relOID); got != "{postgres=r/postgres}" {
		t.Fatalf("relacl after owner re-GRANT following empty array = %q; want %q", got, "{postgres=r/postgres}")
	}
}

// TestRevokeAllFromSchemaOwnerEmptyArray covers DU-002 slice 342: a
// `REVOKE ALL … ON SCHEMA … FROM owner` strips the owner's implicit default
// schema privileges (USAGE, CREATE) and leaves pg_namespace.nspacl as a
// non-NULL *empty* aclitem array {} (distinct from the NULL of a never-granted
// schema). This is the namespace analogue of TestRevokeAllFromOwnerEmptyArray:
// the empty-array (relACLEmptied) state is object-type-agnostic, so the same
// MaterializeOwnerACL → RevokeTablePrivilege sequence applies, only with the
// schema owner-default set ("UC") instead of the table set.
func TestRevokeAllFromSchemaOwnerEmptyArray(t *testing.T) {
	c := NewInMemory()
	const schemaOID = 16980
	allSchema := []string{"USAGE", "CREATE"}

	// REVOKE ALL ON SCHEMA FROM postgres: materialize the owner default (UC), then
	// drop every privilege → nspacl is the empty array "{}", NOT NULL.
	c.MaterializeOwnerACL(schemaOID, "postgres", allSchema)
	for _, p := range allSchema {
		c.RevokeTablePrivilege(schemaOID, "postgres", p)
	}
	if got := c.NamespaceACLText(schemaOID); got != "{}" {
		t.Fatalf("nspacl after REVOKE ALL ON SCHEMA FROM postgres = %q; want %q (empty array, not NULL)", got, "{}")
	}

	// A second owner-side revoke after the array is empty is a no-op: the owner
	// holds nothing, so MaterializeOwnerACL must not bring the schema privileges
	// back.
	c.MaterializeOwnerACL(schemaOID, "postgres", allSchema)
	c.RevokeTablePrivilege(schemaOID, "postgres", "USAGE")
	if got := c.NamespaceACLText(schemaOID); got != "{}" {
		t.Fatalf("nspacl after second owner revoke on empty array = %q; want %q", got, "{}")
	}

	// A schema that never had a grant stays NULL — the empty-array state must not
	// leak across OIDs.
	if got := c.NamespaceACLText(16981); got != "" {
		t.Fatalf("nspacl of unrelated never-granted schema = %q; want \"\" (NULL)", got)
	}

	// Re-granting a privilege to the owner re-materializes a real aclitem and
	// clears the empty-array state.
	c.GrantTablePrivilege(schemaOID, "postgres", "USAGE")
	if got := c.NamespaceACLText(schemaOID); got != "{postgres=U/postgres}" {
		t.Fatalf("nspacl after owner re-GRANT following empty array = %q; want %q", got, "{postgres=U/postgres}")
	}
}

// TestRevokeAllFromSequenceOwnerEmptyArray covers DU-002 slice 343: a
// `REVOKE ALL … ON SEQUENCE … FROM owner` strips the owner's implicit default
// sequence privileges (USAGE, SELECT, UPDATE) and leaves pg_class.relacl as a
// non-NULL *empty* aclitem array {} (distinct from the NULL of a never-granted
// sequence). This is the sequence analogue of TestRevokeAllFromOwnerEmptyArray
// and TestRevokeAllFromSchemaOwnerEmptyArray: the empty-array (relACLEmptied)
// state is object-type-agnostic, so the same MaterializeOwnerACL →
// RevokeTablePrivilege sequence applies, rendered through relaclTextLockedSeq
// (sequence privilege order, owner-default "rwU"). The server already wires this
// path — recordTableRevoke passes allSequencePrivileges to MaterializeOwnerACL
// for an owner-side `REVOKE … ON SEQUENCE … FROM postgres` — so this pins the
// behaviour as a regression guard rather than introducing new code.
func TestRevokeAllFromSequenceOwnerEmptyArray(t *testing.T) {
	c := NewInMemory()
	const seqOID = 16990
	allSeq := []string{"USAGE", "SELECT", "UPDATE"}

	// REVOKE ALL ON SEQUENCE FROM postgres: materialize the owner default (rwU),
	// then drop every privilege → relacl is the empty array "{}", NOT NULL.
	c.MaterializeOwnerACL(seqOID, "postgres", allSeq)
	for _, p := range allSeq {
		c.RevokeTablePrivilege(seqOID, "postgres", p)
	}
	if got := relaclTextSeq(c, seqOID); got != "{}" {
		t.Fatalf("seq relacl after REVOKE ALL ON SEQUENCE FROM postgres = %q; want %q (empty array, not NULL)", got, "{}")
	}

	// A second owner-side revoke after the array is empty is a no-op: the owner
	// holds nothing, so MaterializeOwnerACL must not bring the sequence privileges
	// back.
	c.MaterializeOwnerACL(seqOID, "postgres", allSeq)
	c.RevokeTablePrivilege(seqOID, "postgres", "USAGE")
	if got := relaclTextSeq(c, seqOID); got != "{}" {
		t.Fatalf("seq relacl after second owner revoke on empty array = %q; want %q", got, "{}")
	}

	// A sequence that never had a grant stays NULL — the empty-array state must
	// not leak across OIDs.
	if got := relaclTextSeq(c, 16991); got != "" {
		t.Fatalf("seq relacl of unrelated never-granted sequence = %q; want \"\" (NULL)", got)
	}

	// Re-granting a privilege to the owner re-materializes a real aclitem and
	// clears the empty-array state.
	c.GrantTablePrivilege(seqOID, "postgres", "USAGE")
	if got := relaclTextSeq(c, seqOID); got != "{postgres=U/postgres}" {
		t.Fatalf("seq relacl after owner re-GRANT following empty array = %q; want %q", got, "{postgres=U/postgres}")
	}
}

// TestRevokeAllFromOwnerThenGrantGrantee covers DU-002 slice 344: after a full
// owner-side `REVOKE ALL … FROM owner` empties relacl to {}, a subsequent
// `GRANT <priv> … TO <grantee>` re-materializes the array but the owner stays at
// zero (absent) — PostgreSQL stores `{grantee=…/postgres}` with NO owner entry.
// pg_dump diffs that against acldefault('r', 10) = {postgres=arwdDxtm/postgres}
// and emits BOTH the owner's `REVOKE ALL …` and the grantee `GRANT …`. The
// previous owner-default-fallback model wrongly re-inserted the owner's full
// default ({postgres=arwdDxtm/postgres,bob=r/postgres}), which would drop the
// owner REVOKE on round-trip and silently restore the owner default on restore.
func TestRevokeAllFromOwnerThenGrantGrantee(t *testing.T) {
	c := NewInMemory()
	const relOID = 17000
	allTable := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"}

	// REVOKE ALL FROM postgres → empty array {}.
	c.MaterializeOwnerACL(relOID, "postgres", allTable)
	for _, p := range allTable {
		c.RevokeTablePrivilege(relOID, "postgres", p)
	}
	if got := relaclText(c, relOID); got != "{}" {
		t.Fatalf("relacl after REVOKE ALL FROM postgres = %q; want %q", got, "{}")
	}

	// GRANT SELECT TO bob → owner stays absent; only the grantee renders.
	c.GrantTablePrivilege(relOID, "bob", "SELECT")
	if got := relaclText(c, relOID); got != "{bob=r/postgres}" {
		t.Fatalf("relacl after GRANT SELECT TO bob following owner REVOKE ALL = %q; want %q (owner absent)", got, "{bob=r/postgres}")
	}

	// A second grantee privilege still keeps the owner absent.
	c.GrantTablePrivilege(relOID, "bob", "UPDATE")
	if got := relaclText(c, relOID); got != "{bob=rw/postgres}" {
		t.Fatalf("relacl after additional grantee GRANT = %q; want %q", got, "{bob=rw/postgres}")
	}

	// Revoking the grantee's last privilege drops back to the empty array {} —
	// the owner is still zeroed (relACLEmptied survives the grantee removal).
	c.RevokeTablePrivilege(relOID, "bob", "SELECT")
	c.RevokeTablePrivilege(relOID, "bob", "UPDATE")
	if got := relaclText(c, relOID); got != "{}" {
		t.Fatalf("relacl after revoking the grantee back out = %q; want %q (owner still zeroed)", got, "{}")
	}

	// Now a GRANT to the *owner* re-materializes a real owner aclitem and clears
	// the zeroed state, so a later grantee coexists with the owner entry.
	c.GrantTablePrivilege(relOID, "postgres", "SELECT")
	c.GrantTablePrivilege(relOID, "bob", "INSERT")
	if got := relaclText(c, relOID); got != "{postgres=r/postgres,bob=a/postgres}" {
		t.Fatalf("relacl after owner re-GRANT then grantee = %q; want %q", got, "{postgres=r/postgres,bob=a/postgres}")
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

// TestProcACLText pins the pg_proc.proacl projection that lets a function-level
// GRANT round-trip through pg_dump (DU-002 slice 345). A function's acldefault
// is "{=X/postgres,postgres=X/postgres}" — owner AND PUBLIC both hold EXECUTE —
// so the recorder seeds the PUBLIC entry explicitly and the owner entry is
// supplied by ownerFunctionACLString. pg_dump diffs the materialized proacl
// against acldefault('f', 10) and re-emits the GRANT for the new grantee only.
func TestProcACLText(t *testing.T) {
	c := NewInMemory()
	const procOID = 131072 // FirstRoutineOID

	// No grants → NULL proacl (matches acldefault, so pg_dump emits no GRANT).
	if got := c.ProcACLText(procOID); got != "" {
		t.Fatalf("proacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT EXECUTE … TO a grantee seeds the implicit PUBLIC default plus the
	// grantee; the owner entry is rendered from the owner-default string. The
	// rendered order is owner first, then grantees sorted (grantee_fn before
	// the empty PUBLIC grantee). pg_dump parses the array as a set, so this is
	// equivalent to PostgreSQL's "{=X/postgres,postgres=X/postgres,grantee_fn=X/postgres}".
	c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
	c.GrantTablePrivilege(procOID, "grantee_fn", "EXECUTE")
	want := "{postgres=X/postgres,grantee_fn=X/postgres,=X/postgres}"
	if got := c.ProcACLText(procOID); got != want {
		t.Fatalf("proacl after GRANT EXECUTE TO grantee_fn = %q; want %q", got, want)
	}

	// A non-EXECUTE keyword is not a function privilege and renders nothing for
	// that grantee (relaclTextLockedFor skips letters outside functionACLPrivOrder).
	c.GrantTablePrivilege(procOID, "noise_role", "SELECT")
	if got := c.ProcACLText(procOID); got != want {
		t.Fatalf("proacl must ignore a non-function privilege: got %q; want %q", got, want)
	}
}

// TestProcACLGrantWithGrantOption pins the grant-option (`*`) projection for a
// function-level GRANT … WITH GRANT OPTION (DU-002 slice 348). The grantee's
// EXECUTE is recorded with the grant-option flag so the materialized proacl
// renders "grantee_fn=X*/postgres"; the implicit owner/PUBLIC default entries
// stay plain (acldefault carries no grant option). pg_dump's buildACLCommands
// routes the grant-option privilege to its privswgo branch and re-emits the
// trailing `WITH GRANT OPTION`. Verified byte-identical to real pg_dump 18.3.
func TestProcACLGrantWithGrantOption(t *testing.T) {
	c := NewInMemory()
	const procOID = 131074 // a routine OID distinct from the other ProcACL tests

	// Mirror recordFunctionGrant's WITH-GRANT-OPTION sequence: seed the implicit
	// PUBLIC default plain, then grant the grantee EXECUTE with the grant option.
	c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
	c.GrantTablePrivilegeWithGrantOption(procOID, "grantee_fn", "EXECUTE", true)
	want := "{postgres=X/postgres,grantee_fn=X*/postgres,=X/postgres}"
	if got := c.ProcACLText(procOID); got != want {
		t.Fatalf("proacl after GRANT EXECUTE WITH GRANT OPTION = %q; want %q", got, want)
	}
}

// TestProcACLRevokeFromPublic exercises the function-level REVOKE … FROM PUBLIC
// primitive composition (DU-002 slice 346). A function's implicit default proacl
// grants EXECUTE to BOTH the owner and PUBLIC; PostgreSQL leaves proacl NULL
// until the first GRANT/REVOKE. `REVOKE EXECUTE … FROM PUBLIC` on a never-granted
// routine materializes the owner's default entry (the PUBLIC half is implicit and
// simply omitted once revoked), yielding proacl = "{postgres=X/postgres}", which
// pg_dump diffs against acldefault('f', 10) to emit `REVOKE ALL ON FUNCTION …
// FROM PUBLIC;`. Verified byte-identical to real pg_dump 18.3.
func TestProcACLRevokeFromPublic(t *testing.T) {
	c := NewInMemory()
	const procOID = 131073 // a routine OID distinct from TestProcACLText's

	// No grants → NULL proacl.
	if got := c.ProcACLText(procOID); got != "" {
		t.Fatalf("proacl with no grants = %q; want \"\" (NULL)", got)
	}

	// REVOKE EXECUTE … FROM PUBLIC: materialize the owner default, then remove the
	// (absent, implicit) PUBLIC EXECUTE. Mirrors recordFunctionRevoke's sequence.
	c.MaterializeOwnerACL(procOID, "postgres", []string{"EXECUTE"})
	c.RevokeTablePrivilege(procOID, "PUBLIC", "EXECUTE")
	want := "{postgres=X/postgres}"
	if got := c.ProcACLText(procOID); got != want {
		t.Fatalf("proacl after REVOKE EXECUTE FROM PUBLIC = %q; want %q", got, want)
	}
}

// TestProcACLRevokeFromOwner pins the owner-side function REVOKE (DU-002 slice
// 347). A function's acldefault('f', owner) grants EXECUTE to BOTH the owner and
// PUBLIC, so revoking the owner's implicit default leaves PUBLIC's entry behind
// ("{=X/postgres}", verified against PostgreSQL 18.3), distinct from a sequence
// or table whose default grants only the owner (REVOKE ALL FROM owner → "{}").
// Revoking BOTH the owner and PUBLIC empties the array to "{}".
func TestProcACLRevokeFromOwner(t *testing.T) {
	t.Run("owner_only_leaves_public", func(t *testing.T) {
		c := NewInMemory()
		const procOID = 131074 // distinct from the other proacl tests

		// First mutation expands the implicit default {owner=X, =X}, then the
		// owner-side revoke removes the owner — mirrors recordFunctionRevoke.
		c.MaterializeOwnerACL(procOID, "postgres", []string{"EXECUTE"})
		c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
		c.RevokeTablePrivilege(procOID, "postgres", "EXECUTE")
		if want, got := "{=X/postgres}", c.ProcACLText(procOID); got != want {
			t.Fatalf("proacl after REVOKE EXECUTE FROM postgres = %q; want %q", got, want)
		}
	})

	t.Run("owner_and_public_empties", func(t *testing.T) {
		c := NewInMemory()
		const procOID = 131075

		c.MaterializeOwnerACL(procOID, "postgres", []string{"EXECUTE"})
		c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
		// Revoke PUBLIC last so relACLEmptied stays unset (the owner is not the
		// final entry removed); relACLOwnerRevoked must still render "{}".
		c.RevokeTablePrivilege(procOID, "postgres", "EXECUTE")
		c.RevokeTablePrivilege(procOID, "PUBLIC", "EXECUTE")
		if want, got := "{}", c.ProcACLText(procOID); got != want {
			t.Fatalf("proacl after REVOKE EXECUTE FROM postgres,PUBLIC = %q; want %q", got, want)
		}
	})

	t.Run("owner_grant_rematerializes", func(t *testing.T) {
		c := NewInMemory()
		const procOID = 131076

		c.MaterializeOwnerACL(procOID, "postgres", []string{"EXECUTE"})
		c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
		c.RevokeTablePrivilege(procOID, "postgres", "EXECUTE") // {=X/postgres}
		// A later owner-side GRANT clears relACLOwnerRevoked and restores the owner.
		c.GrantTablePrivilege(procOID, "postgres", "EXECUTE")
		if want, got := "{postgres=X/postgres,=X/postgres}", c.ProcACLText(procOID); got != want {
			t.Fatalf("proacl after re-GRANT to postgres = %q; want %q", got, want)
		}
	})
}
