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
