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

// TestRoleNameForOID pins the OID→name reverse resolver the pg_type.typacl
// heap-decode path uses to render grantee/grantor role NAMES back from the
// stored AclItem OIDs (M0119-0004-ACLHEAP). OID 0 = PUBLIC (empty grantee),
// OID 10 = the bootstrap superuser "postgres", a registered role resolves to
// its case-preserved spelling, and an unknown OID falls back to its numeric
// form (PG's rendering of a since-dropped role).
func TestRoleNameForOID(t *testing.T) {
	c := NewInMemory()
	if got := c.RoleNameForOID(0); got != "" {
		t.Errorf("RoleNameForOID(0) = %q, want \"\" (PUBLIC)", got)
	}
	if got := c.RoleNameForOID(10); got != "postgres" {
		t.Errorf("RoleNameForOID(10) = %q, want \"postgres\"", got)
	}
	c.RegisterRole("typg_grantee")
	oid, ok := c.RoleOID("typg_grantee")
	if !ok || oid == 0 {
		t.Fatalf("RoleOID(typg_grantee) = %d, %v; want a non-zero OID", oid, ok)
	}
	if got := c.RoleNameForOID(oid); got != "typg_grantee" {
		t.Errorf("RoleNameForOID(%d) = %q, want \"typg_grantee\"", oid, got)
	}
	// A case-significant (double-quoted) role keeps its original spelling.
	c.GrantTablePrivilegeWithGrantOption(999, "MixedCase", "USAGE", false)
	c.RegisterRole("MixedCase")
	mc, _ := c.RoleOID("MixedCase")
	if got := c.RoleNameForOID(mc); got != "MixedCase" {
		t.Errorf("RoleNameForOID(%d) = %q, want \"MixedCase\" (case-preserved)", mc, got)
	}
	if got := c.RoleNameForOID(4242); got != "4242" {
		t.Errorf("RoleNameForOID(4242) = %q, want \"4242\" (numeric fallback)", got)
	}
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

	// A second grantee is appended in GRANT order (not alphabetical): PostgreSQL's
	// aclupdate appends a brand-new grantee's aclitem to the end of relacl, so
	// grantee_role (granted first) precedes another_role. Verified against real PG
	// 18.3: GRANT … TO grantee_role then another_role →
	// {postgres=…,grantee_role=arw/postgres,another_role=d/postgres}. The owner
	// entry stays at the head. DU-002 slice 354.
	c.GrantTablePrivilege(relOID, "another_role", "DELETE")
	want = "{postgres=arwdDxtm/postgres,grantee_role=arw/postgres,another_role=d/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl with two grantees = %q; want %q", got, want)
	}

	// DropTableACL reverts relacl back to NULL.
	c.DropTableACL(relOID)
	if got := relaclText(c, relOID); got != "" {
		t.Fatalf("relacl after DropTableACL = %q; want \"\" (NULL)", got)
	}
}

// TestRelaclTextRegrantAfterRevokeMovesToEnd pins the grant-order semantics of a
// REVOKE that fully drops a grantee followed by a re-GRANT to that same grantee:
// PostgreSQL's aclupdate (src/backend/utils/adt/acl.c) DELETES the revoked
// grantee's aclitem, then a later GRANT APPENDS a fresh aclitem at the END of the
// array — it does not restore the grantee's original slot. So the re-granted
// grantee renders AFTER grantees that have held their privilege continuously,
// even if it was granted first and sorts first. Verified against real PG 18.3:
// GRANT SELECT TO a; GRANT SELECT TO b; REVOKE SELECT FROM a; GRANT INSERT TO a →
// {postgres=arwdDxtm/postgres,b=r/postgres,a=a/postgres}. This guards the
// dropTableACLOrderRole teardown + re-append path (DU-002 slice 354/355).
func TestRelaclTextRegrantAfterRevokeMovesToEnd(t *testing.T) {
	c := NewInMemory()
	const relOID = 16600

	c.GrantTablePrivilege(relOID, "rg_a", "SELECT")
	c.GrantTablePrivilege(relOID, "rg_b", "SELECT")
	// Both held continuously → grant order a, b.
	want := "{postgres=arwdDxtm/postgres,rg_a=r/postgres,rg_b=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl before revoke = %q; want %q", got, want)
	}

	// Fully revoke rg_a's only privilege: its aclitem is deleted (dropped from the
	// grant-order list), leaving rg_b alone.
	c.RevokeTablePrivilege(relOID, "rg_a", "SELECT")
	want = "{postgres=arwdDxtm/postgres,rg_b=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after revoke of rg_a = %q; want %q", got, want)
	}

	// Re-GRANT a different privilege to rg_a: a fresh aclitem is appended at the
	// END, so rg_a now follows rg_b even though it was originally granted first.
	c.GrantTablePrivilege(relOID, "rg_a", "INSERT")
	want = "{postgres=arwdDxtm/postgres,rg_b=r/postgres,rg_a=a/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after re-grant of rg_a = %q; want %q", got, want)
	}
}

// TestRelaclTextPartialRevokeKeepsSlot pins the complement of slice 355: a
// PARTIAL revoke (the grantee retains at least one privilege) must keep that
// grantee's aclitem in its original grant-order slot — it does NOT move to the
// end. PostgreSQL's aclupdate (src/backend/utils/adt/acl.c) modifies the
// existing aclitem in place when bits are removed but the entry survives, so its
// array index is unchanged; only a full revoke (entry deleted) followed by a
// re-GRANT appends a fresh aclitem at the end. goopg mirrors this: RevokeTable-
// Privilege only calls dropTableACLOrderRole when the grantee's privilege set
// becomes empty, so a partial revoke leaves tableACLOrder untouched. Verified
// against real PG 18.3: GRANT SELECT,INSERT TO pr_a; GRANT SELECT TO pr_b;
// REVOKE INSERT FROM pr_a → {postgres=arwdDxtm/postgres,pr_a=r/postgres,
// pr_b=r/postgres} (pr_a stays ahead of pr_b). DU-002 slice 356.
func TestRelaclTextPartialRevokeKeepsSlot(t *testing.T) {
	c := NewInMemory()
	const relOID = 16620

	// pr_a granted first with two privileges; pr_b granted second with one.
	c.GrantTablePrivilege(relOID, "pr_a", "SELECT")
	c.GrantTablePrivilege(relOID, "pr_a", "INSERT")
	c.GrantTablePrivilege(relOID, "pr_b", "SELECT")
	want := "{postgres=arwdDxtm/postgres,pr_a=ar/postgres,pr_b=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl before revoke = %q; want %q", got, want)
	}

	// Partial revoke of pr_a (drops INSERT 'a', keeps SELECT 'r'): pr_a's aclitem
	// is modified in place, so pr_a stays ahead of pr_b — it does NOT migrate to
	// the end as a full-revoke-then-re-grant would (cf. slice 355).
	c.RevokeTablePrivilege(relOID, "pr_a", "INSERT")
	want = "{postgres=arwdDxtm/postgres,pr_a=r/postgres,pr_b=r/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after partial REVOKE INSERT FROM pr_a = %q; want %q (pr_a keeps its slot)", got, want)
	}

	// Contrast guard: now FULLY revoke pr_a then re-GRANT — pr_a's slot is
	// released and the re-grant appends at the end, landing pr_a after pr_b.
	c.RevokeTablePrivilege(relOID, "pr_a", "SELECT")
	c.GrantTablePrivilege(relOID, "pr_a", "DELETE")
	want = "{postgres=arwdDxtm/postgres,pr_b=r/postgres,pr_a=d/postgres}"
	if got := relaclText(c, relOID); got != want {
		t.Fatalf("relacl after full revoke + re-grant of pr_a = %q; want %q (pr_a moves to end)", got, want)
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
	// PUBLIC stays the empty grantee. Grantees render in GRANT order, so PUBLIC
	// (granted first) precedes named_role. Verified against real PG 18.3: GRANT
	// SELECT TO PUBLIC then GRANT INSERT TO named_role →
	// {postgres=…,=r/postgres,named_role=a/postgres}. DU-002 slice 354.
	c.GrantTablePrivilege(relOID, "named_role", "INSERT")
	want = "{postgres=arwdDxtm/postgres,=r/postgres,named_role=a/postgres}"
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

// TestForeignServerACLText pins the pg_foreign_server.srvacl projection that
// lets a `GRANT … ON FOREIGN SERVER …` round-trip through pg_dump (DU-002
// slice 427). A foreign server's owner default is "U" (USAGE only), which
// pg_dump diffs against via acldefault('S', owner); unlike a function/type, a
// foreign server grants PUBLIC nothing by default (world default
// ACL_NO_RIGHTS), so no implicit PUBLIC entry appears unless a GRANT … TO
// PUBLIC is recorded explicitly. Foreign servers share the OID-keyed ACL store
// with relations/schemas/routines/types, so the same GrantTablePrivilege*
// recorders apply.
func TestForeignServerACLText(t *testing.T) {
	c := NewInMemory()
	const srvOID = 16900

	// No grants → NULL srvacl (matches acldefault, so pg_dump emits no GRANT).
	if got := c.ForeignServerACLText(srvOID); got != "" {
		t.Fatalf("srvacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT USAGE materializes the owner entry ("U") plus the grantee ("U").
	c.GrantTablePrivilege(srvOID, "srv_role", "USAGE")
	want := "{postgres=U/postgres,srv_role=U/postgres}"
	if got := c.ForeignServerACLText(srvOID); got != want {
		t.Fatalf("srvacl after GRANT USAGE = %q; want %q", got, want)
	}

	// A grant-option USAGE renders "U*".
	const goSrvOID = 16901
	c.GrantTablePrivilegeWithGrantOption(goSrvOID, "srv_role", "USAGE", true)
	want = "{postgres=U/postgres,srv_role=U*/postgres}"
	if got := c.ForeignServerACLText(goSrvOID); got != want {
		t.Fatalf("srvacl after GRANT USAGE WITH GRANT OPTION = %q; want %q", got, want)
	}

	// GRANT … ON FOREIGN SERVER … TO PUBLIC materializes the empty grantee.
	const pubSrvOID = 16902
	c.GrantTablePrivilege(pubSrvOID, "PUBLIC", "USAGE")
	want = "{postgres=U/postgres,=U/postgres}"
	if got := c.ForeignServerACLText(pubSrvOID); got != want {
		t.Fatalf("srvacl after GRANT USAGE TO PUBLIC = %q; want %q", got, want)
	}

	// REVOKE ALL ON FOREIGN SERVER FROM postgres: materialize the owner default
	// ("U"), then drop it → srvacl is the empty array "{}", NOT NULL (mirrors
	// TestRevokeAllFromSchemaOwnerEmptyArray's MaterializeOwnerACL →
	// RevokeTablePrivilege sequence, only with the foreign-server owner-default
	// set).
	const revSrvOID = 16903
	c.MaterializeOwnerACL(revSrvOID, "postgres", []string{"USAGE"})
	c.RevokeTablePrivilege(revSrvOID, "postgres", "USAGE")
	want = "{}"
	if got := c.ForeignServerACLText(revSrvOID); got != want {
		t.Fatalf("srvacl after owner REVOKE ALL = %q; want %q", got, want)
	}
}

// TestForeignDataWrapperACLText pins the pg_foreign_data_wrapper.fdwacl
// projection that lets a `GRANT … ON FOREIGN DATA WRAPPER …` round-trip
// through pg_dump (DU-002 slice 428). An FDW's owner default is "U" (USAGE
// only), which pg_dump diffs against via acldefault('F', owner); like a
// foreign server, an FDW grants PUBLIC nothing by default (world default
// ACL_NO_RIGHTS), so no implicit PUBLIC entry appears unless a GRANT … TO
// PUBLIC is recorded explicitly. FDWs share the OID-keyed ACL store with
// relations/schemas/routines/types/foreign servers, so the same
// GrantTablePrivilege* recorders apply.
func TestForeignDataWrapperACLText(t *testing.T) {
	c := NewInMemory()
	const fdwOID = 16910

	// No grants → NULL fdwacl (matches acldefault, so pg_dump emits no GRANT).
	if got := c.ForeignDataWrapperACLText(fdwOID); got != "" {
		t.Fatalf("fdwacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT USAGE materializes the owner entry ("U") plus the grantee ("U").
	c.GrantTablePrivilege(fdwOID, "fdw_role", "USAGE")
	want := "{postgres=U/postgres,fdw_role=U/postgres}"
	if got := c.ForeignDataWrapperACLText(fdwOID); got != want {
		t.Fatalf("fdwacl after GRANT USAGE = %q; want %q", got, want)
	}

	// A grant-option USAGE renders "U*".
	const goFdwOID = 16911
	c.GrantTablePrivilegeWithGrantOption(goFdwOID, "fdw_role", "USAGE", true)
	want = "{postgres=U/postgres,fdw_role=U*/postgres}"
	if got := c.ForeignDataWrapperACLText(goFdwOID); got != want {
		t.Fatalf("fdwacl after GRANT USAGE WITH GRANT OPTION = %q; want %q", got, want)
	}

	// GRANT … ON FOREIGN DATA WRAPPER … TO PUBLIC materializes the empty grantee.
	const pubFdwOID = 16912
	c.GrantTablePrivilege(pubFdwOID, "PUBLIC", "USAGE")
	want = "{postgres=U/postgres,=U/postgres}"
	if got := c.ForeignDataWrapperACLText(pubFdwOID); got != want {
		t.Fatalf("fdwacl after GRANT USAGE TO PUBLIC = %q; want %q", got, want)
	}

	// REVOKE ALL ON FOREIGN DATA WRAPPER FROM postgres: materialize the owner
	// default ("U"), then drop it → fdwacl is the empty array "{}", NOT NULL
	// (mirrors TestForeignServerACLText's owner REVOKE ALL case).
	const revFdwOID = 16913
	c.MaterializeOwnerACL(revFdwOID, "postgres", []string{"USAGE"})
	c.RevokeTablePrivilege(revFdwOID, "postgres", "USAGE")
	want = "{}"
	if got := c.ForeignDataWrapperACLText(revFdwOID); got != want {
		t.Fatalf("fdwacl after owner REVOKE ALL = %q; want %q", got, want)
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
	// grantee; the owner entry is rendered from the owner-default string. Grantees
	// render in GRANT order (PUBLIC granted first, then grantee_fn) with the owner
	// pulled to the head: "{postgres=X/postgres,=X/postgres,grantee_fn=X/postgres}".
	// Real PG 18.3 stores "{=X/postgres,postgres=X/postgres,grantee_fn=X/postgres}"
	// (owner/PUBLIC default ordering differs — a pre-existing owner-first rendering
	// choice), but grantee_fn lands LAST in both, and pg_dump parses the array as a
	// set so the round-trip is byte-identical. DU-002 slice 354.
	c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
	c.GrantTablePrivilege(procOID, "grantee_fn", "EXECUTE")
	want := "{postgres=X/postgres,=X/postgres,grantee_fn=X/postgres}"
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
	// Grantees render in GRANT order (PUBLIC seeded first, then grantee_fn) with
	// the owner at the head. DU-002 slice 354.
	c.GrantTablePrivilege(procOID, "PUBLIC", "EXECUTE")
	c.GrantTablePrivilegeWithGrantOption(procOID, "grantee_fn", "EXECUTE", true)
	want := "{postgres=X/postgres,=X/postgres,grantee_fn=X*/postgres}"
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

// TestTypeACLText pins the pg_type.typacl projection that the GRANT-on-TYPE
// round-trip needs (M0119-0004-ACLHEAP). A type/domain's acldefault('T', owner)
// is "{=U/postgres,postgres=U/postgres}" — owner AND PUBLIC both hold USAGE,
// structurally identical to a function's EXECUTE default — so the recorder seeds
// the PUBLIC entry explicitly and the owner entry is supplied by
// ownerTypeACLString. pg_dump's getTypes diffs the materialized typacl against
// acldefault('T', typowner) and re-emits the GRANT for the new grantee only.
// Unlike proacl this text must additionally be written back into the heap-backed
// pg_type row by the GRANT path; this test pins only the renderer half.
func TestTypeACLText(t *testing.T) {
	c := NewInMemory()
	const typeOID = 16500 // a user type OID distinct from the routine/relation tests

	// No grants → NULL typacl (matches acldefault, so pg_dump emits no GRANT).
	if got := c.TypeACLText(typeOID); got != "" {
		t.Fatalf("typacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT USAGE … TO a grantee seeds the implicit PUBLIC default plus the
	// grantee; the owner entry is rendered from the owner-default string. Grantees
	// render in GRANT order (PUBLIC granted first, then grantee_ty) with the owner
	// pulled to the head, mirroring the proacl projection.
	c.GrantTablePrivilege(typeOID, "PUBLIC", "USAGE")
	c.GrantTablePrivilege(typeOID, "grantee_ty", "USAGE")
	want := "{postgres=U/postgres,=U/postgres,grantee_ty=U/postgres}"
	if got := c.TypeACLText(typeOID); got != want {
		t.Fatalf("typacl after GRANT USAGE TO grantee_ty = %q; want %q", got, want)
	}

	// A non-USAGE keyword is not a type privilege and renders nothing for that
	// grantee (relaclTextLockedFor skips letters outside typeACLPrivOrder).
	c.GrantTablePrivilege(typeOID, "noise_role", "SELECT")
	if got := c.TypeACLText(typeOID); got != want {
		t.Fatalf("typacl must ignore a non-type privilege: got %q; want %q", got, want)
	}
}

// TestTypeACLGrantWithGrantOption pins the grant-option (`*`) projection for a
// type-level GRANT … WITH GRANT OPTION. The grantee's USAGE is recorded with the
// grant-option flag so typacl renders "grantee_ty=U*/postgres"; the implicit
// owner/PUBLIC default entries stay plain (acldefault carries no grant option).
func TestTypeACLGrantWithGrantOption(t *testing.T) {
	c := NewInMemory()
	const typeOID = 16501

	c.GrantTablePrivilege(typeOID, "PUBLIC", "USAGE")
	c.GrantTablePrivilegeWithGrantOption(typeOID, "grantee_ty", "USAGE", true)
	want := "{postgres=U/postgres,=U/postgres,grantee_ty=U*/postgres}"
	if got := c.TypeACLText(typeOID); got != want {
		t.Fatalf("typacl after GRANT USAGE WITH GRANT OPTION = %q; want %q", got, want)
	}
}

// TestTypeACLRevokeFromPublic exercises the type-level REVOKE … FROM PUBLIC
// primitive. A type's implicit default typacl grants USAGE to BOTH the owner and
// PUBLIC; PostgreSQL leaves typacl NULL until the first GRANT/REVOKE. `REVOKE
// USAGE … FROM PUBLIC` on a never-granted type materializes the owner's default
// entry (the PUBLIC half is implicit and simply omitted once revoked), yielding
// typacl = "{postgres=U/postgres}", which pg_dump diffs against acldefault('T',
// 10) to emit `REVOKE ALL ON TYPE … FROM PUBLIC;`.
func TestTypeACLRevokeFromPublic(t *testing.T) {
	c := NewInMemory()
	const typeOID = 16502

	if got := c.TypeACLText(typeOID); got != "" {
		t.Fatalf("typacl with no grants = %q; want \"\" (NULL)", got)
	}

	c.MaterializeOwnerACL(typeOID, "postgres", []string{"USAGE"})
	c.RevokeTablePrivilege(typeOID, "PUBLIC", "USAGE")
	want := "{postgres=U/postgres}"
	if got := c.TypeACLText(typeOID); got != want {
		t.Fatalf("typacl after REVOKE USAGE FROM PUBLIC = %q; want %q", got, want)
	}
}

// TestTypeACLRevokeFromOwner pins the owner-side type REVOKE. A type's
// acldefault('T', owner) grants USAGE to BOTH the owner and PUBLIC, so revoking
// the owner's implicit default leaves PUBLIC's entry behind ("{=U/postgres}"),
// distinct from a relation whose default grants only the owner. Revoking BOTH
// empties the array to "{}".
func TestTypeACLRevokeFromOwner(t *testing.T) {
	t.Run("owner_only_leaves_public", func(t *testing.T) {
		c := NewInMemory()
		const typeOID = 16503

		c.MaterializeOwnerACL(typeOID, "postgres", []string{"USAGE"})
		c.GrantTablePrivilege(typeOID, "PUBLIC", "USAGE")
		c.RevokeTablePrivilege(typeOID, "postgres", "USAGE")
		if want, got := "{=U/postgres}", c.TypeACLText(typeOID); got != want {
			t.Fatalf("typacl after REVOKE USAGE FROM postgres = %q; want %q", got, want)
		}
	})

	t.Run("owner_and_public_empties", func(t *testing.T) {
		c := NewInMemory()
		const typeOID = 16504

		c.MaterializeOwnerACL(typeOID, "postgres", []string{"USAGE"})
		c.GrantTablePrivilege(typeOID, "PUBLIC", "USAGE")
		c.RevokeTablePrivilege(typeOID, "postgres", "USAGE")
		c.RevokeTablePrivilege(typeOID, "PUBLIC", "USAGE")
		if want, got := "{}", c.TypeACLText(typeOID); got != want {
			t.Fatalf("typacl after REVOKE USAGE FROM postgres,PUBLIC = %q; want %q", got, want)
		}
	})
}

// TestAttrACLText pins the pg_attribute.attacl projection the column-GRANT
// round-trip needs (M0119-0004-ACLHEAP, attacl half). Unlike relacl/typacl/proacl
// a column has an EMPTY acldefault('c', owner) — the owner and PUBLIC hold no
// implicit column privilege — so attacl carries NO leading owner aclitem and no
// implicit PUBLIC entry: `GRANT SELECT(c) ON t TO grantee` materializes attacl as
// "{grantee=r/postgres}" only, verified against PostgreSQL 18.3. pg_dump's
// getTableAttrs diffs this against the empty default and re-emits the column
// GRANT.
func TestAttrACLText(t *testing.T) {
	c := NewInMemory()
	const relOID = 16600
	const attNum int16 = 2

	// No grants → NULL attacl (matches the empty acldefault, so pg_dump emits no
	// column GRANT).
	if got := c.AttrACLText(relOID, attNum); got != "" {
		t.Fatalf("attacl with no grants = %q; want \"\" (NULL)", got)
	}

	// GRANT SELECT(col) … TO a grantee materializes ONLY the grantee — no owner
	// entry, no implicit PUBLIC entry (the key difference from typacl/proacl).
	c.GrantColumnPrivilege(relOID, attNum, "grantee_col", "SELECT")
	if want, got := "{grantee_col=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after GRANT SELECT(col) TO grantee_col = %q; want %q", got, want)
	}

	// A second column privilege to the same grantee accumulates in canonical bit
	// order (INSERT(a) before UPDATE(w)): "arw" needs a,r,w sorted.
	c.GrantColumnPrivilege(relOID, attNum, "grantee_col", "INSERT")
	c.GrantColumnPrivilege(relOID, attNum, "grantee_col", "UPDATE")
	if want, got := "{grantee_col=arw/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after GRANT INSERT,UPDATE(col) = %q; want %q", got, want)
	}

	// A second grantee appends in grant order; PUBLIC renders as an empty grantee.
	c.GrantColumnPrivilege(relOID, attNum, "PUBLIC", "SELECT")
	if want, got := "{grantee_col=arw/postgres,=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after GRANT SELECT(col) TO PUBLIC = %q; want %q", got, want)
	}

	// A non-column keyword (DELETE is table-only) renders nothing for its grantee.
	c.GrantColumnPrivilege(relOID, attNum, "noise_role", "DELETE")
	if want, got := "{grantee_col=arw/postgres,=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl must ignore a non-column privilege: got %q; want %q", got, want)
	}

	// A different column of the same relation has an independent ACL.
	if got := c.AttrACLText(relOID, 3); got != "" {
		t.Fatalf("attacl for an ungranted sibling column = %q; want \"\" (NULL)", got)
	}
}

// TestAttrACLGrantWithGrantOption pins the grant-option (`*`) projection for a
// column-level GRANT … WITH GRANT OPTION: the grantee's privilege renders with a
// trailing `*` and pg_dump re-emits WITH GRANT OPTION. M0119-0004-ACLHEAP.
func TestAttrACLGrantWithGrantOption(t *testing.T) {
	c := NewInMemory()
	const relOID = 16601
	const attNum int16 = 1

	c.GrantColumnPrivilegeWithGrantOption(relOID, attNum, "grantee_col", "UPDATE", true)
	if want, got := "{grantee_col=w*/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after GRANT UPDATE(col) WITH GRANT OPTION = %q; want %q", got, want)
	}
}

// TestAttrACLRevoke exercises the column REVOKE path: removing the last column
// privilege drops the grantee, and an emptied column returns attacl to NULL (a
// column has no owner default to fall back to, distinct from a table that empties
// to "{}"). M0119-0004-ACLHEAP.
func TestAttrACLRevoke(t *testing.T) {
	c := NewInMemory()
	const relOID = 16602
	const attNum int16 = 4

	c.GrantColumnPrivilege(relOID, attNum, "a_role", "SELECT")
	c.GrantColumnPrivilege(relOID, attNum, "a_role", "UPDATE")
	c.GrantColumnPrivilege(relOID, attNum, "b_role", "SELECT")

	// Revoke one of a_role's two privileges: it stays, with the remaining letter.
	c.RevokeColumnPrivilege(relOID, attNum, "a_role", "UPDATE")
	if want, got := "{a_role=r/postgres,b_role=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after REVOKE UPDATE(col) FROM a_role = %q; want %q", got, want)
	}

	// Revoke a_role's last privilege: it is dropped, b_role preserved in order.
	c.RevokeColumnPrivilege(relOID, attNum, "a_role", "SELECT")
	if want, got := "{b_role=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after REVOKE SELECT(col) FROM a_role = %q; want %q", got, want)
	}

	// Revoke the last grantee: attacl returns to NULL (not "{}").
	c.RevokeColumnPrivilege(relOID, attNum, "b_role", "SELECT")
	if want, got := "", c.AttrACLText(relOID, attNum); got != want {
		t.Fatalf("attacl after revoking every grantee = %q; want %q (NULL)", got, want)
	}

	// A revoke of a privilege never held is a no-op.
	c.RevokeColumnPrivilege(relOID, attNum, "ghost", "SELECT")
	if got := c.AttrACLText(relOID, attNum); got != "" {
		t.Fatalf("no-op revoke perturbed attacl: %q", got)
	}
}

// TestAttrACLGranteeNameRendering pins grantee-name handling in attacl, shared
// with the relacl roleACLDisplay/aclQuoteName paths: a mixed-case but
// all-alphanumeric name keeps its original spelling UNQUOTED (PG's putid only
// quotes names with bytes outside [A-Za-z0-9_]), while a name with an unsafe
// char (a hyphen) is double-quoted exactly as aclitemout renders it.
// M0119-0004-ACLHEAP.
func TestAttrACLGranteeNameRendering(t *testing.T) {
	t.Run("mixed_case_preserved_unquoted", func(t *testing.T) {
		c := NewInMemory()
		const relOID = 16603
		const attNum int16 = 1
		c.GrantColumnPrivilege(relOID, attNum, "MixedCase", "SELECT")
		if want, got := "{MixedCase=r/postgres}", c.AttrACLText(relOID, attNum); got != want {
			t.Fatalf("attacl for a mixed-case grantee = %q; want %q", got, want)
		}
	})

	t.Run("unsafe_char_quoted", func(t *testing.T) {
		c := NewInMemory()
		const relOID = 16604
		const attNum int16 = 1
		c.GrantColumnPrivilege(relOID, attNum, "needs-quote", "SELECT")
		if want, got := `{"needs-quote"=r/postgres}`, c.AttrACLText(relOID, attNum); got != want {
			t.Fatalf("attacl for an unsafe-char grantee = %q; want %q", got, want)
		}
	})
}
