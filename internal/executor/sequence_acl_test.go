package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Sequence ACL enforcement — M0134-0069 bucket 5 (sequence.sql regress
// diff). PostgreSQL requires nextval/currval/setval/lastval and ALTER
// SEQUENCE to check privileges/ownership before acting; goopg previously
// skipped this entirely. PG oracle: postgres/src/backend/commands/sequence.c
// nextval_internal (~649-655, ACL_USAGE|ACL_UPDATE), currval_oid (~876-881,
// ACL_SELECT|ACL_USAGE), lastval (~918-923, ACL_SELECT|ACL_USAGE),
// do_setval (~960-964, ACL_UPDATE); AlterSequence (~437) for ownership.
// Fixture: postgres/src/test/regress/sql/sequence.sql lines 294-396.

func mustExecErr(t *testing.T, err error) *ExecError {
	t.Helper()
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	return execErr
}

// TestNextvalRequiresUsageOrUpdatePrivilege pins nextval()'s ACL check
// (sequence.c nextval_internal ~649-655): a non-owner role with neither
// USAGE nor UPDATE gets 42501; either privilege alone is sufficient; the
// sequence owner and the bootstrap superuser always pass.
func TestNextvalRequiresUsageOrUpdatePrivilege(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_nextval_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_nextval_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}
	if tbl.Owner != "bob" {
		t.Fatalf("expected sequence owner %q, got %q", "bob", tbl.Owner)
	}

	// Non-owner, no grants: denied.
	ctx.NonSuperuserRole = "alice"
	_, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')")
	if err == nil {
		t.Fatal("expected 42501, got nil")
	}
	if ee := mustExecErr(t, err); ee.Code != "42501" {
		t.Fatalf("expected 42501, got %s (%v)", ee.Code, ee)
	}

	// USAGE alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "USAGE")
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')"); err != nil {
		t.Fatalf("nextval with USAGE: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "USAGE")

	// UPDATE alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')"); err != nil {
		t.Fatalf("nextval with UPDATE: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "UPDATE")

	// SELECT alone is NOT sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")
	_, err = runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("nextval with only SELECT: expected 42501, got %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "SELECT")

	// The sequence owner always passes without any GRANT.
	ctx.NonSuperuserRole = "bob"
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')"); err != nil {
		t.Fatalf("nextval as owner: unexpected error: %v", err)
	}

	// The bootstrap superuser (no SET ROLE) always passes.
	ctx.NonSuperuserRole = ""
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_nextval_acl')"); err != nil {
		t.Fatalf("nextval as superuser: unexpected error: %v", err)
	}
}

// TestCurrvalRequiresSelectOrUsagePrivilege pins currval()'s ACL check
// (sequence.c currval_oid ~876-881): denied without SELECT/USAGE, allowed
// with either.
func TestCurrvalRequiresSelectOrUsagePrivilege(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_currval_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_currval_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}
	// Owner primes currval's session state via nextval.
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_currval_acl')"); err != nil {
		t.Fatalf("nextval (owner priming): unexpected error: %v", err)
	}

	// Non-owner: currval() alone (without a prior nextval by this role) would
	// hit 55000 first — grant UPDATE so nextval succeeds, then test currval's
	// own ACL independently by using a role that already has a CurrSeqVals
	// entry. Simplest: keep using ctx (same session, CurrSeqVals already has
	// an entry from the owner's nextval above), and switch role.
	ctx.NonSuperuserRole = "alice"
	_, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("currval with no grant: expected 42501, got %v", err)
	}

	// SELECT alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')"); err != nil {
		t.Fatalf("currval with SELECT: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "SELECT")

	// USAGE alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "USAGE")
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')"); err != nil {
		t.Fatalf("currval with USAGE: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "USAGE")

	// UPDATE alone is NOT sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	_, err = runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("currval with only UPDATE: expected 42501, got %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "UPDATE")

	// The owner always passes.
	ctx.NonSuperuserRole = "bob"
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')"); err != nil {
		t.Fatalf("currval as owner: unexpected error: %v", err)
	}

	// The bootstrap superuser always passes.
	ctx.NonSuperuserRole = ""
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_currval_acl')"); err != nil {
		t.Fatalf("currval as superuser: unexpected error: %v", err)
	}
}

// TestSetvalRequiresUpdatePrivilege pins setval()'s ACL check (sequence.c
// do_setval ~960-964): UPDATE only — SELECT/USAGE alone is not sufficient.
func TestSetvalRequiresUpdatePrivilege(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_setval_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_setval_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}

	ctx.NonSuperuserRole = "alice"
	_, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_setval_acl', 5)")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("setval with no grant: expected 42501, got %v", err)
	}

	// SELECT/USAGE alone is NOT sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")
	im.GrantTablePrivilege(tbl.OID, "alice", "USAGE")
	_, err = runSQLCtxErr(t, ctx, "SELECT setval('seq_setval_acl', 5)")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("setval with SELECT/USAGE only: expected 42501, got %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "SELECT")
	im.RevokeTablePrivilege(tbl.OID, "alice", "USAGE")

	// UPDATE is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	if _, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_setval_acl', 5)"); err != nil {
		t.Fatalf("setval with UPDATE: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "UPDATE")

	// The owner always passes.
	ctx.NonSuperuserRole = "bob"
	if _, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_setval_acl', 5)"); err != nil {
		t.Fatalf("setval as owner: unexpected error: %v", err)
	}

	// The bootstrap superuser always passes.
	ctx.NonSuperuserRole = ""
	if _, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_setval_acl', 5)"); err != nil {
		t.Fatalf("setval as superuser: unexpected error: %v", err)
	}
}

// TestLastvalRequiresSelectOrUsagePrivilege pins lastval()'s ACL check
// (sequence.c lastval ~918-923): the check applies to the last sequence
// used by nextval() in this session, once resolved.
func TestLastvalRequiresSelectOrUsagePrivilege(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_lastval_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_lastval_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}
	// Owner primes lastval's session state via nextval.
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_lastval_acl')"); err != nil {
		t.Fatalf("nextval (owner priming): unexpected error: %v", err)
	}

	ctx.NonSuperuserRole = "alice"
	_, err := runSQLCtxErr(t, ctx, "SELECT lastval()")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("lastval with no grant: expected 42501, got %v", err)
	}

	// SELECT alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err != nil {
		t.Fatalf("lastval with SELECT: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "SELECT")

	// USAGE alone is sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "USAGE")
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err != nil {
		t.Fatalf("lastval with USAGE: unexpected error: %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "USAGE")

	// UPDATE alone is NOT sufficient.
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	_, err = runSQLCtxErr(t, ctx, "SELECT lastval()")
	if err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("lastval with only UPDATE: expected 42501, got %v", err)
	}
	im.RevokeTablePrivilege(tbl.OID, "alice", "UPDATE")

	// The owner always passes.
	ctx.NonSuperuserRole = "bob"
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err != nil {
		t.Fatalf("lastval as owner: unexpected error: %v", err)
	}

	// The bootstrap superuser always passes.
	ctx.NonSuperuserRole = ""
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err != nil {
		t.Fatalf("lastval as superuser: unexpected error: %v", err)
	}
}

// TestSequenceOwnerACLRevokedDeniesOwner pins M0134-0069 bucket 7: PostgreSQL's
// object-owner privilege is a *revocable implicit aclitem* (acl.c
// pg_class_aclcheck never special-cases "is this role the owner" — only the
// DDL-facing *_ownercheck functions do, and those are untouched here), so
// once `REVOKE ALL ON <seq> FROM <owner>` has emptied the owner's implicit
// default ACL, the (former) owner denies nextval/currval/setval/lastval just
// like any other role without an explicit grant. Re-granting a single
// privilege back to the owner (a materialized owner ACL entry, not the
// implicit-default bypass) restores only that specific right — matching the
// REVOKE-then-selective-GRANT sub-block in
// postgres/src/test/regress/sql/sequence.sql lines ~645-786 (role
// regress_seq_user). The catalog-level plumbing (MaterializeOwnerACL under
// the "postgres" owner-ACL sentinel + RevokeTablePrivilege) mirrors exactly
// what grant_ddl.go's tryRecordTableRevoke now does when the REVOKE target is
// the object's actual owner.
func TestSequenceOwnerACLRevokedDeniesOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_owner_revoked_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_owner_revoked_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}
	if tbl.Owner != "bob" {
		t.Fatalf("expected sequence owner %q, got %q", "bob", tbl.Owner)
	}

	// Before any revoke, the owner passes unconditionally (also primes
	// CurrSeqVals for the currval/lastval checks below).
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_owner_revoked_acl')"); err != nil {
		t.Fatalf("nextval as owner before revoke: unexpected error: %v", err)
	}

	// REVOKE ALL ON SEQUENCE seq_owner_revoked_acl FROM bob: materialize the
	// owner's full implicit default (USAGE, SELECT, UPDATE) under the
	// "postgres" owner-ACL sentinel key, then strip every privilege — the
	// same sequence tryRecordTableRevoke now performs for a REVOKE naming the
	// actual owner (rather than the literal role "postgres").
	allSeqPrivs := []string{"USAGE", "SELECT", "UPDATE"}
	im.MaterializeOwnerACL(tbl.OID, "postgres", allSeqPrivs)
	for _, p := range allSeqPrivs {
		im.RevokeTablePrivilege(tbl.OID, "postgres", p)
	}

	// The owner is now denied every operation, exactly like a stranger role.
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_owner_revoked_acl')"); err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("nextval as owner after REVOKE ALL: expected 42501, got %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_owner_revoked_acl')"); err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("currval as owner after REVOKE ALL: expected 42501, got %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_owner_revoked_acl', 5)"); err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("setval as owner after REVOKE ALL: expected 42501, got %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err == nil || mustExecErr(t, err).Code != "42501" {
		t.Fatalf("lastval as owner after REVOKE ALL: expected 42501, got %v", err)
	}

	// A selective GRANT back to the owner (GRANT SELECT ON seq TO bob) is
	// observed here to restore the FULL implicit owner bypass, not just
	// SELECT-gated operations — a real PG-fidelity gap in
	// GrantTablePrivilegeAs's pre-existing "role == aclOwnerRole clears
	// relACLEmptied/relACLOwnerRevoked" behavior (catalog.go ~16270-16273,
	// written for relacl *display* purposes, not privilege re-gating), out
	// of scope for this brief (M0134-0069 bucket 7 only wires
	// IsOwnerACLRevoked/HasOwnerPrivilege into the read path). Verified
	// against real PG 18.3's sequence.sql fixture (REVOKE ALL ... FROM
	// regress_seq_user; GRANT UPDATE ... TO regress_seq_user; SELECT
	// currval(...) still ERRORs — UPDATE alone must NOT restore SELECT/
	// USAGE-gated currval), which goopg does not yet match end-to-end; see
	// the coordinator's M0134-0069 bucket 7 report for the full finding.
	// This assertion pins the CURRENT (divergent) behavior as a regression
	// guard, not as the PG-correct target.
	im.GrantTablePrivilege(tbl.OID, "postgres", "SELECT")
	if _, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_owner_revoked_acl')"); err != nil {
		t.Fatalf("currval as owner after selective SELECT re-grant: unexpected error: %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, "SELECT lastval()"); err != nil {
		t.Fatalf("lastval as owner after selective SELECT re-grant: unexpected error: %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_owner_revoked_acl', 5)"); err != nil {
		t.Fatalf("setval as owner after selective SELECT re-grant: unexpected error (current, non-PG-faithful behavior: any owner re-grant restores full bypass): %v", err)
	}

	// The bootstrap superuser (no SET ROLE) always passes regardless of any
	// owner-ACL revoke.
	ctx.NonSuperuserRole = ""
	if _, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_owner_revoked_acl')"); err != nil {
		t.Fatalf("nextval as superuser after owner REVOKE ALL: unexpected error: %v", err)
	}
}

// TestAlterSequenceRequiresOwnership pins ALTER SEQUENCE's ownership check
// (sequence.c AlterSequence ~437, via RangeVarGetRelidExtended +
// RangeVarCallbackOwnsRelation — shared with ALTER TABLE's owner check,
// tablecmds.c:19554): a non-owner role gets 42501 "must be owner of
// sequence <name>" regardless of any GRANTed ACL privilege; the owner and
// the bootstrap superuser succeed.
func TestAlterSequenceRequiresOwnership(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	// currentDDLOwnerOID/checkCommentObjectOwner resolve unmapped role names
	// to the bootstrap-superuser OID (10) as a fallback, which would make an
	// ownership check between two never-CREATE-ROLE'd names vacuously equal.
	// Register both roles with distinct OIDs so the check is meaningful.
	im.RegisterRoleWithOID("bob", 100001)
	im.RegisterRoleWithOID("alice", 100002)

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_alter_acl"); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_alter_acl"})
	if !ok {
		t.Fatal("sequence catalog table not found")
	}
	// Even a full ACL grant does not substitute for ownership.
	im.GrantTablePrivilege(tbl.OID, "alice", "USAGE")
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")

	ctx.NonSuperuserRole = "alice"
	err := runDDL(t, ctx, "ALTER SEQUENCE seq_alter_acl START WITH 1")
	if err == nil {
		t.Fatal("expected 42501 must-be-owner error, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42501" {
		t.Fatalf("expected 42501, got %s (%v)", ee.Code, ee)
	}
	if want := "must be owner of sequence seq_alter_acl"; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}

	// The owner succeeds.
	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "ALTER SEQUENCE seq_alter_acl START WITH 1"); err != nil {
		t.Fatalf("ALTER SEQUENCE as owner: unexpected error: %v", err)
	}

	// The bootstrap superuser (no SET ROLE) succeeds regardless of owner.
	ctx.NonSuperuserRole = ""
	if err := runDDL(t, ctx, "ALTER SEQUENCE seq_alter_acl START WITH 1"); err != nil {
		t.Fatalf("ALTER SEQUENCE as superuser: unexpected error: %v", err)
	}
}

// TestValidateSeqOwnedByIndexTarget pins the index-relkind branch of
// validateSeqOwnedBy: naming an index (rather than a table/view) in OWNED BY
// must return 42809 (ERRCODE_WRONG_OBJECT_TYPE) with the PG-faithful DETAIL,
// not the generic 42P01 "does not exist" that a plain LookupTable miss
// produces. PG oracle: postgres/src/backend/commands/sequence.c:1629-1638
// (process_owned_by), errdetail_relkind_not_supported (pg_class.c:24-52).
// M0134-0069 bucket 6 item 4.
func TestValidateSeqOwnedByIndexTarget(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_owned_idx_tbl (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX seq_owned_idx ON seq_owned_idx_tbl (id)"); err != nil {
		t.Fatalf("create index: %v", err)
	}

	err := runDDL(t, ctx, "CREATE SEQUENCE seq_owned_idx_seq OWNED BY seq_owned_idx.id")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `sequence cannot be owned by relation "seq_owned_idx"`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if want := "This operation is not supported for indexes."; ee.Detail != want {
		t.Fatalf("expected detail %q, got %q", want, ee.Detail)
	}
}
