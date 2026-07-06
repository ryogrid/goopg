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
