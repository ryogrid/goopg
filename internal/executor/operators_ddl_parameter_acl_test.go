package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/parser"
)

// sessionGetSetting builds a Context.GetSetting closure backed by a real
// config.SessionRegistry (the default GUC set), so checkParameterACLName's
// "does this compiled-in GUC exist" branch exercises actual registered names
// rather than a hand-picked stand-in list.
func sessionGetSetting() func(name string) (string, bool) {
	sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	return func(name string) (string, bool) {
		_, val, ok := sess.Get(name)
		return val, ok
	}
}

// TestCheckParameterACLName ports check_GUC_name_for_parameter_acl's three
// branches: a real compiled-in GUC is accepted; a syntactically valid
// custom/extension name (two-or-more dot-separated identifiers) is accepted
// even though goopg has never seen it; a single-part unrecognized name is
// rejected 42704; a dotted name that fails the custom-name syntax check is
// rejected 42602. M0119-0004-ACLHEAP (parameter ACL half).
func TestCheckParameterACLName(t *testing.T) {
	op := &ddlOp{ctx: &Context{GetSetting: sessionGetSetting()}}

	if err := op.checkParameterACLName("work_mem"); err != nil {
		t.Fatalf("known compiled-in GUC \"work_mem\" rejected: %v", err)
	}
	if err := op.checkParameterACLName("plpgsql.check_asserts"); err != nil {
		t.Fatalf("syntactically valid custom GUC name rejected: %v", err)
	}

	err := op.checkParameterACLName("totally_bogus_param")
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42704" {
		t.Fatalf("unrecognized single-part name: got %#v, want 42704 ExecError", err)
	}

	err = op.checkParameterACLName("bad..name")
	execErr, ok = err.(*ExecError)
	if !ok || execErr.Code != "42602" {
		t.Fatalf("malformed dotted name: got %#v, want 42602 ExecError", err)
	}
}

// TestExecParameterACLChangeRejectsUnknownName confirms the executor wiring:
// GRANT ... ON PARAMETER on a name that is neither a known GUC nor a valid
// custom name fails (42704) and never materializes a pg_parameter_acl row —
// mirroring ParameterAclCreate's check_GUC_name_for_parameter_acl guard.
// M0119-0004-ACLHEAP (parameter ACL half).
func TestExecParameterACLChangeRejectsUnknownName(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat, GetSetting: sessionGetSetting()}}

	pc := &parser.ParameterACLChange{
		Privileges: []string{"SET"},
		ParamNames: []string{"totally_bogus_param"},
		Grantees:   []string{"grantee_p"},
	}
	err := op.execParameterACLChange(pc)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42704" {
		t.Fatalf("expected 42704 for unrecognized parameter name, got %#v", err)
	}
	if got := cat.ParameterACLEntries(); len(got) != 0 {
		t.Fatalf("a rejected GRANT must not materialize a pg_parameter_acl row, got %+v", got)
	}
}

// TestExecParameterACLChangeAcceptsKnownAndCustomNames confirms a known
// compiled-in GUC and a syntactically valid custom name both succeed and
// materialize exactly one row each.
func TestExecParameterACLChangeAcceptsKnownAndCustomNames(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat, GetSetting: sessionGetSetting()}}

	pc := &parser.ParameterACLChange{
		Privileges: []string{"SET"},
		ParamNames: []string{"work_mem", "myext.enabled"},
		Grantees:   []string{"grantee_p"},
	}
	if err := op.execParameterACLChange(pc); err != nil {
		t.Fatalf("expected GRANT to succeed, got %v", err)
	}
	got := cat.ParameterACLEntries()
	if len(got) != 2 {
		t.Fatalf("ParameterACLEntries len = %d, want 2: %+v", len(got), got)
	}
}

// TestExecParameterACLChangeRevokeOnUnknownNameIsNoop mirrors
// objectNamesToOids' REVOKE branch: naming a GUC with no existing
// pg_parameter_acl entry has nothing to remove, so it must not materialize a
// row (goopg previously called ParameterACLOID unconditionally on the REVOKE
// path too, minting a spurious owner-only row for a name that was never
// granted — fixed alongside the GRANT-side name validation).
func TestExecParameterACLChangeRevokeOnUnknownNameIsNoop(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat, GetSetting: sessionGetSetting()}}

	pc := &parser.ParameterACLChange{
		Revoke:     true,
		Privileges: []string{"SET"},
		ParamNames: []string{"never_granted_param"},
		Grantees:   []string{"grantee_p"},
	}
	if err := op.execParameterACLChange(pc); err != nil {
		t.Fatalf("REVOKE on a never-granted parameter should be a no-op, got error %v", err)
	}
	if got := cat.ParameterACLEntries(); len(got) != 0 {
		t.Fatalf("REVOKE on a never-granted parameter must not materialize a row, got %+v", got)
	}
}

// TestExecParameterACLChangeSecondGrantSkipsRevalidation confirms real PG's
// timing: name validation only runs the first time a GRANT would
// materialize a brand-new entry. A second GRANT on an already-materialized
// name must succeed even via a Context whose GetSetting always reports
// "unknown" — the entry already exists, so ParameterAclLookup's hit path
// (not ParameterAclCreate) is taken and check_GUC_name_for_parameter_acl
// never runs again.
func TestExecParameterACLChangeSecondGrantSkipsRevalidation(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.ParameterACLOID("already_materialized") // simulate a prior successful GRANT

	unknownGetSetting := func(name string) (string, bool) { return "", false }
	op := &ddlOp{ctx: &Context{Catalog: cat, GetSetting: unknownGetSetting}}

	pc := &parser.ParameterACLChange{
		Privileges: []string{"SET"},
		ParamNames: []string{"already_materialized"},
		Grantees:   []string{"grantee_p"},
	}
	if err := op.execParameterACLChange(pc); err != nil {
		t.Fatalf("GRANT on an already-materialized parameter must skip name validation, got %v", err)
	}
}
