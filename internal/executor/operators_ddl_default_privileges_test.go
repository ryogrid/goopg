package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestExecAlterDefaultPrivilegesGrantMaterializesRow confirms the basic GRANT
// path: no FOR ROLE/IN SCHEMA defaults to the bootstrap superuser (OID 10)
// and a global (schemaOID 0) entry, and materializes exactly one
// pg_default_acl row. M0110-0001 (DU-002 slice 438 follow-up).
func TestExecAlterDefaultPrivilegesGrantMaterializesRow(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(s); err != nil {
		t.Fatalf("expected GRANT to succeed, got %v", err)
	}
	entries := cat.DefaultACLEntries()
	if len(entries) != 1 {
		t.Fatalf("DefaultACLEntries len = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].RoleOID != 10 || entries[0].SchemaOID != 0 || entries[0].ObjType != catalog.DefaultACLObjTypeRelation {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

// TestExecAlterDefaultPrivilegesForRoleUnregisteredRejected mirrors
// get_rolespec_oid: a FOR ROLE naming an unregistered role raises 42704 and
// must not materialize any row.
func TestExecAlterDefaultPrivilegesForRoleUnregisteredRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Roles:      []string{"nosuchrole"},
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	err := op.execAlterDefaultPrivileges(s)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "42704" {
		t.Fatalf("expected 42704 for unregistered FOR ROLE target, got %#v", err)
	}
	if got := cat.DefaultACLEntries(); len(got) != 0 {
		t.Fatalf("a rejected GRANT must not materialize a row, got %+v", got)
	}
}

// TestExecAlterDefaultPrivilegesInSchemaUnknownRejected mirrors every other
// schema-name lookup in this codebase: an unrecognized IN SCHEMA target
// raises 3F000.
func TestExecAlterDefaultPrivilegesInSchemaUnknownRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Schemas:    []string{"nosuchschema"},
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	err := op.execAlterDefaultPrivileges(s)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "3F000" {
		t.Fatalf("expected 3F000 for unrecognized IN SCHEMA target, got %#v", err)
	}
}

// TestExecAlterDefaultPrivilegesInSchemaWithSchemasRejected and its LARGE
// OBJECTS sibling mirror SetDefaultACL's (aclchk.c) outright rejection of IN
// SCHEMA combined with either namespace-independent object class.
func TestExecAlterDefaultPrivilegesInSchemaWithSchemasRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Schemas:    []string{"public"},
		ObjType:    "schema",
		Privileges: []string{"USAGE"},
		Grantees:   []string{"alice"},
	}
	err := op.execAlterDefaultPrivileges(s)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "0LP01" {
		t.Fatalf("expected 0LP01 for IN SCHEMA + ON SCHEMAS, got %#v", err)
	}
}

func TestExecAlterDefaultPrivilegesInSchemaWithLargeObjectsRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Schemas:    []string{"public"},
		ObjType:    "largeobject",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	err := op.execAlterDefaultPrivileges(s)
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "0LP01" {
		t.Fatalf("expected 0LP01 for IN SCHEMA + ON LARGE OBJECTS, got %#v", err)
	}
}

// TestExecAlterDefaultPrivilegesLargeObjectsGloballyAcceptedNoop confirms
// goopg's documented scope boundary: LARGE OBJECTS with no IN SCHEMA is
// syntactically accepted (goopg has no pg_largeobject subsystem to describe
// a resulting row) and never materializes a pg_default_acl entry.
func TestExecAlterDefaultPrivilegesLargeObjectsGloballyAcceptedNoop(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		ObjType:    "largeobject",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(s); err != nil {
		t.Fatalf("bare LARGE OBJECTS default should be accepted as a no-op, got %v", err)
	}
	if got := cat.DefaultACLEntries(); len(got) != 0 {
		t.Fatalf("LARGE OBJECTS has no backing storage, must not materialize a row, got %+v", got)
	}
}

// TestExecAlterDefaultPrivilegesRevokeNeverGrantedIsNoop mirrors
// execParameterACLChange's identical REVOKE-side gate: revoking a triple
// that was never granted must not mint a hollow pg_default_acl row.
func TestExecAlterDefaultPrivilegesRevokeNeverGrantedIsNoop(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Revoke:     true,
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(s); err != nil {
		t.Fatalf("REVOKE on a never-granted default should be a no-op, got %v", err)
	}
	if got := cat.DefaultACLEntries(); len(got) != 0 {
		t.Fatalf("REVOKE on a never-granted default must not materialize a row, got %+v", got)
	}
}

// TestExecAlterDefaultPrivilegesGrantThenFullRevokeDropsRow confirms
// SetDefaultACL's row-deletion behavior: once every grant on an entry is
// revoked back to its implicit default, the pg_default_acl row disappears
// from DefaultACLEntries entirely (not merely emptied).
func TestExecAlterDefaultPrivilegesGrantThenFullRevokeDropsRow(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	grant := &parser.AlterDefaultPrivilegesStmt{
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(grant); err != nil {
		t.Fatalf("GRANT failed: %v", err)
	}
	if got := cat.DefaultACLEntries(); len(got) != 1 {
		t.Fatalf("expected 1 entry after GRANT, got %d: %+v", len(got), got)
	}

	revoke := &parser.AlterDefaultPrivilegesStmt{
		Revoke:     true,
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(revoke); err != nil {
		t.Fatalf("REVOKE failed: %v", err)
	}
	if got := cat.DefaultACLEntries(); len(got) != 0 {
		t.Fatalf("expected 0 entries after full REVOKE, got %d: %+v", len(got), got)
	}
}

// TestExecAlterDefaultPrivilegesInSchemaScopedEntry confirms a schema-scoped
// (IN SCHEMA given) GRANT materializes against the resolved namespace OID,
// distinct from a global entry.
func TestExecAlterDefaultPrivilegesInSchemaScopedEntry(t *testing.T) {
	cat := catalog.NewInMemory()
	op := &ddlOp{ctx: &Context{Catalog: cat}}

	s := &parser.AlterDefaultPrivilegesStmt{
		Schemas:    []string{"public"},
		ObjType:    "table",
		Privileges: []string{"SELECT"},
		Grantees:   []string{"alice"},
	}
	if err := op.execAlterDefaultPrivileges(s); err != nil {
		t.Fatalf("expected GRANT to succeed, got %v", err)
	}
	entries := cat.DefaultACLEntries()
	if len(entries) != 1 {
		t.Fatalf("DefaultACLEntries len = %d, want 1: %+v", len(entries), entries)
	}
	wantSchemaOID := cat.SchemaOID("public")
	if entries[0].SchemaOID != wantSchemaOID || entries[0].SchemaOID == 0 {
		t.Fatalf("expected schema-scoped entry with SchemaOID %d, got %+v", wantSchemaOID, entries[0])
	}
}
