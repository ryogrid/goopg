package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestExecTypeACLChangeStampsActingRoleAsGrantor confirms execTypeACLChange's
// grantor-wiring follow-up (M0119-0004-ACLHEAP): a GRANT ON TYPE issued while
// impersonating a non-superuser role (o.ctx.NonSuperuserRole, the SET
// ROLE/SET SESSION AUTHORIZATION-tracked effective role) stamps that role as
// the aclitem's grantor instead of the hardcoded owner, mirroring
// TestRelaclTextGrantor/TestAttrACLTextGrantor for the shared tableACLs/
// tableACLGrantor store typacl also renders through.
func TestExecTypeACLChangeStampsActingRoleAsGrantor(t *testing.T) {
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("acl_grantor_enum", []string{"a", "b"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, NonSuperuserRole: "bob"}}

	tc := &parser.TypeACLChange{
		Privileges: []string{"USAGE"},
		TypeNames:  []parser.ObjectName{{Name: "acl_grantor_enum"}},
		Grantees:   []string{"charlie"},
	}
	if err := op.execTypeACLChange(tc); err != nil {
		t.Fatalf("execTypeACLChange: %v", err)
	}

	want := "{postgres=U/postgres,=U/postgres,charlie=U/bob}"
	if got := cat.TypeACLText(et.OID); got != want {
		t.Fatalf("typacl after impersonated GRANT = %q; want %q", got, want)
	}
}

// TestExecDatabaseACLChangeStampsActingRoleAsGrantor is the DATABASE-ACL
// analogue of TestExecTypeACLChangeStampsActingRoleAsGrantor — datacl shares
// the same grantor-aware GrantTablePrivilegeAs primitive.
func TestExecDatabaseACLChangeStampsActingRoleAsGrantor(t *testing.T) {
	cat := catalog.NewInMemory()
	dbOid := cat.DBOID()
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres", NonSuperuserRole: "bob"}}

	dc := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"postgres"},
		Grantees:      []string{"charlie"},
	}
	if err := op.execDatabaseACLChange(dc); err != nil {
		t.Fatalf("execDatabaseACLChange: %v", err)
	}

	want := "{postgres=CTc/postgres,=Tc/postgres,charlie=c/bob}"
	if got := cat.DatabaseACLText(dbOid); got != want {
		t.Fatalf("datacl after impersonated GRANT = %q; want %q", got, want)
	}
}

// TestExecParameterACLChangeStampsActingRoleAsGrantor is the PARAMETER-ACL
// analogue of TestExecTypeACLChangeStampsActingRoleAsGrantor — paracl shares
// the same grantor-aware GrantTablePrivilegeAs primitive.
func TestExecParameterACLChangeStampsActingRoleAsGrantor(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat, GetSetting: sessionGetSetting(), NonSuperuserRole: "bob"}}

	pc := &parser.ParameterACLChange{
		Privileges: []string{"SET"},
		ParamNames: []string{"work_mem"},
		Grantees:   []string{"charlie"},
	}
	if err := op.execParameterACLChange(pc); err != nil {
		t.Fatalf("execParameterACLChange: %v", err)
	}

	oid := cat.ParameterACLOID("work_mem")
	want := "{postgres=sA/postgres,charlie=s/bob}"
	if got := cat.ParameterACLText(oid); got != want {
		t.Fatalf("paracl after impersonated GRANT = %q; want %q", got, want)
	}
}

// TestExecACLChangeGrantedByCurrentUserIsNoop confirms an explicit `GRANTED
// BY <acting role>` clause on a TYPE/DATABASE/PARAMETER GRANT — real, valid
// PostgreSQL grammar restricted to "SQL compatibility" (aclchk.c
// ExecuteGrantStmt) — is accepted as a no-op confirmation exactly like
// omitting the clause, mirroring
// TestTryRecordTableGrantGrantedByCurrentUserIsNoop for the table-ACL path.
func TestExecACLChangeGrantedByCurrentUserIsNoop(t *testing.T) {
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("acl_grantedby_enum", []string{"a", "b"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, NonSuperuserRole: "bob"}}

	tc := &parser.TypeACLChange{
		Privileges: []string{"USAGE"},
		TypeNames:  []parser.ObjectName{{Name: "acl_grantedby_enum"}},
		Grantees:   []string{"charlie"},
		GrantedBy:  "bob",
	}
	if err := op.execTypeACLChange(tc); err != nil {
		t.Fatalf("execTypeACLChange with GRANTED BY bob (as bob): %v", err)
	}
	want := "{postgres=U/postgres,=U/postgres,charlie=U/bob}"
	if got := cat.TypeACLText(et.OID); got != want {
		t.Fatalf("typacl = %q; want %q", got, want)
	}
}

// TestExecACLChangeGrantedByOtherRoleErrors pins the flip side across all
// three call sites (TYPE, DATABASE, PARAMETER): a GRANTED BY clause naming
// any role other than the acting one is rejected 0A000 ("grantor must be
// current user"), never silently substituted as the recorded grantor.
// Mirrors TestTryRecordTableGrantGrantedByOtherRoleErrors for the table-ACL
// path. Verified against real PG 18.3 (postgres/src/backend/catalog/
// aclchk.c:394-412, the shared InternalGrant check every object-privilege
// GRANT/REVOKE variant runs through).
func TestExecACLChangeGrantedByOtherRoleErrors(t *testing.T) {
	cat := catalog.NewInMemory()
	et, err := cat.RegisterEnum("acl_grantedby_err_enum", []string{"a", "b"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres", GetSetting: sessionGetSetting(), NonSuperuserRole: "bob"}}

	tc := &parser.TypeACLChange{
		Privileges: []string{"USAGE"},
		TypeNames:  []parser.ObjectName{{Name: "acl_grantedby_err_enum"}},
		Grantees:   []string{"charlie"},
		GrantedBy:  "alice",
	}
	if err := op.execTypeACLChange(tc); err == nil {
		t.Fatal("execTypeACLChange with GRANTED BY alice (as bob): want error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "0A000" {
		t.Fatalf("execTypeACLChange error = %v; want 0A000 ExecError", err)
	}
	if got := cat.TypeACLText(et.OID); got != "" {
		t.Fatalf("typacl after rejected GRANT = %q; want \"\" (NULL)", got)
	}

	dc := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"postgres"},
		Grantees:      []string{"charlie"},
		GrantedBy:     "alice",
	}
	dbOid := cat.DBOID()
	if err := op.execDatabaseACLChange(dc); err == nil {
		t.Fatal("execDatabaseACLChange with GRANTED BY alice (as bob): want error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "0A000" {
		t.Fatalf("execDatabaseACLChange error = %v; want 0A000 ExecError", err)
	}
	if got := cat.DatabaseACLText(dbOid); got != "" {
		t.Fatalf("datacl after rejected GRANT = %q; want \"\" (NULL)", got)
	}

	pc := &parser.ParameterACLChange{
		Privileges: []string{"SET"},
		ParamNames: []string{"work_mem"},
		Grantees:   []string{"charlie"},
		GrantedBy:  "alice",
	}
	if err := op.execParameterACLChange(pc); err == nil {
		t.Fatal("execParameterACLChange with GRANTED BY alice (as bob): want error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "0A000" {
		t.Fatalf("execParameterACLChange error = %v; want 0A000 ExecError", err)
	}
}
