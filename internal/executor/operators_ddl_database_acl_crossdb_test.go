package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestExecDatabaseACLChangeReachesNonConnectedDatabase pins the M0122-0008
// fix: `GRANT … ON DATABASE otherdb` issued from a session connected to
// `postgres` must change otherdb's datacl.
//
// Before the fix execDatabaseACLChange compared every named database against
// ctx.CurrentDatabase and returned nil when none matched, so the statement
// reported GRANT and changed nothing. Upstream's ExecGrant_Database
// (postgres/src/backend/catalog/aclchk.c) resolves each name against the
// SHARED pg_database catalog and never consults MyDatabaseId; measured on a
// live PG 18.3, the same statement leaves
// `{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}` on otherdb.
//
// The live-database assertion is the paired control, and it is the half that
// makes this test able to fail for the right reason: an implementation that
// simply applied the grant to whatever database it had in hand would satisfy
// the otherdb assertion while corrupting the connected one.
func TestExecDatabaseACLChangeReachesNonConnectedDatabase(t *testing.T) {
	cat := catalog.NewInMemory()
	otherOid, err := cat.CreateDatabase("otherdb", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres"}}

	dc := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"otherdb"},
		Grantees:      []string{"r1"},
	}
	if err := op.execDatabaseACLChange(dc); err != nil {
		t.Fatalf("execDatabaseACLChange: %v", err)
	}

	want := "{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}"
	if got := cat.DatabaseACLText(otherOid); got != want {
		t.Errorf("otherdb datacl = %q; want %q", got, want)
	}
	// The connected database must be untouched: the grant named otherdb.
	if got := cat.DatabaseACLText(cat.DBOID()); got != "" {
		t.Errorf("connected database's datacl = %q; want it untouched (empty)", got)
	}
}

// TestExecDatabaseACLChangeAppliesToEveryNamedDatabase covers the list form.
// PG applies `GRANT … ON DATABASE a, b` to both; a loop that stopped at the
// first resolvable name would pass the single-database test above.
func TestExecDatabaseACLChangeAppliesToEveryNamedDatabase(t *testing.T) {
	cat := catalog.NewInMemory()
	aOid, err := cat.CreateDatabase("dba", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase dba: %v", err)
	}
	bOid, err := cat.CreateDatabase("dbb", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase dbb: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres"}}

	dc := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"dba", "dbb"},
		Grantees:      []string{"r1"},
	}
	if err := op.execDatabaseACLChange(dc); err != nil {
		t.Fatalf("execDatabaseACLChange: %v", err)
	}
	want := "{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}"
	if got := cat.DatabaseACLText(aOid); got != want {
		t.Errorf("dba datacl = %q; want %q", got, want)
	}
	if got := cat.DatabaseACLText(bOid); got != want {
		t.Errorf("dbb datacl = %q; want %q", got, want)
	}
}

// TestExecDatabaseACLChangeUnknownDatabaseErrors pins upstream's refusal.
// get_database_oid(..., missing_ok = false) raises 3D000; goopg used to treat
// an unresolvable name as "not the live database" and return success, which is
// the worst of the two — a typo in a database name silently granted nothing.
//
// The second assertion is what makes the all-or-nothing resolution order a
// tested property rather than an implementation detail: a statement naming one
// good and one bad database must leave the good one alone.
func TestExecDatabaseACLChangeUnknownDatabaseErrors(t *testing.T) {
	cat := catalog.NewInMemory()
	goodOid, err := cat.CreateDatabase("dbgood", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres"}}

	dc := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"dbgood", "nosuchdb"},
		Grantees:      []string{"r1"},
	}
	err = op.execDatabaseACLChange(dc)
	if err == nil {
		t.Fatalf("execDatabaseACLChange on an unknown database returned nil; want 3D000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T (%v); want *ExecError", err, err)
	}
	if ee.Code != "3D000" {
		t.Errorf("SQLSTATE = %q; want 3D000", ee.Code)
	}
	if want := `database "nosuchdb" does not exist`; ee.Message != want {
		t.Errorf("message = %q; want %q", ee.Message, want)
	}
	if got := cat.DatabaseACLText(goodOid); got != "" {
		t.Errorf("dbgood datacl = %q; want it untouched — the statement errored", got)
	}
}

// TestExecDatabaseACLChangeRevokeReachesNonConnectedDatabase is the REVOKE
// half. The two arms seed the implicit acldefault('d', …) differently, so a
// fix applied only to the GRANT branch would leave REVOKE silently inert.
// PG 18.3 measured: after REVOKE CONNECT the row reads
// `{=Tc/postgres,postgres=CTc/postgres}`.
func TestExecDatabaseACLChangeRevokeReachesNonConnectedDatabase(t *testing.T) {
	cat := catalog.NewInMemory()
	otherOid, err := cat.CreateDatabase("otherdb", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	op := &ddlOp{ctx: &Context{Catalog: cat, CurrentDatabase: "postgres"}}

	grant := &parser.DatabaseACLChange{
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"otherdb"},
		Grantees:      []string{"r1"},
	}
	if err := op.execDatabaseACLChange(grant); err != nil {
		t.Fatalf("GRANT: %v", err)
	}
	revoke := &parser.DatabaseACLChange{
		Revoke:        true,
		Privileges:    []string{"CONNECT"},
		DatabaseNames: []string{"otherdb"},
		Grantees:      []string{"r1"},
	}
	if err := op.execDatabaseACLChange(revoke); err != nil {
		t.Fatalf("REVOKE: %v", err)
	}
	want := "{=Tc/postgres,postgres=CTc/postgres}"
	if got := cat.DatabaseACLText(otherOid); got != want {
		t.Errorf("otherdb datacl after REVOKE = %q; want %q", got, want)
	}
}
