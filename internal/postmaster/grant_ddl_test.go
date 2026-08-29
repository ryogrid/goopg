package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// newGrantTestServer builds a bare *Server wired to a fresh in-memory catalog
// holding one table ("t"), sufficient to exercise tryRecordTableGrant/
// tryRecordTableRevoke without a real network connection.
func newGrantTestServer(t *testing.T) (*Server, *catalog.InMemory, uint32) {
	t.Helper()
	im := catalog.NewInMemory()
	tbl, err := im.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	return &Server{cfg: Config{Catalog: im}}, im, tbl.OID
}

// TestTryRecordTableGrantStampsActingRoleAsGrantor pins the M0119-0004-ACLHEAP
// grantor-tracking fix: a GRANT issued by a role reached via SET ROLE / SET
// SESSION AUTHORIZATION (not the bootstrap superuser) must be attributed to
// that role in the materialized relacl, not hardcoded to the object owner —
// mirroring the real, reachable PostgreSQL path of a WITH GRANT OPTION
// delegation chain (verified against real PG 18.3: a grantee who received
// WITH GRANT OPTION and then grants onward produces an aclitem whose grantor
// is that intermediate role, e.g. "charlie=r/bob").
func TestTryRecordTableGrantStampsActingRoleAsGrantor(t *testing.T) {
	s, im, oid := newGrantTestServer(t)

	// The bootstrap superuser's own GRANT is still attributed to the owner
	// ("postgres") — the pre-existing, unchanged common case.
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO bob`, "", nil); err != nil {
		t.Fatalf("owner GRANT: %v", err)
	}
	want := `{postgres=arwdDxtm/postgres,bob=r/postgres}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl after owner GRANT = %q; want %q", got, want)
	}

	// bob (SET ROLE'd, i.e. actingRole = "bob") grants onward to charlie: the
	// new aclitem's grantor must be "bob", not "postgres".
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie`, "bob", nil); err != nil {
		t.Fatalf("delegated GRANT: %v", err)
	}
	want = `{postgres=arwdDxtm/postgres,bob=r/postgres,charlie=r/bob}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl after delegated GRANT = %q; want %q", got, want)
	}

	// A later re-GRANT of the same privilege by the owner restamps the
	// grantor back to the owner (PostgreSQL's aclupdate updates an existing
	// aclitem's grantor to whoever issued the latest GRANT).
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie`, "", nil); err != nil {
		t.Fatalf("owner re-GRANT: %v", err)
	}
	want = `{postgres=arwdDxtm/postgres,bob=r/postgres,charlie=r/postgres}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl after owner re-GRANT = %q; want %q", got, want)
	}
}

// TestTryRecordTableGrantGrantedByCurrentUserIsNoop pins that an explicit
// `GRANTED BY <acting role>` clause — real, valid PostgreSQL grammar restricted
// to "SQL compatibility" (aclchk.c ExecuteGrantStmt) — is accepted as a no-op
// confirmation exactly like omitting the clause, whether the acting role is
// the bootstrap superuser or a SET ROLE-impersonated role.
func TestTryRecordTableGrantGrantedByCurrentUserIsNoop(t *testing.T) {
	s, im, oid := newGrantTestServer(t)

	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO bob GRANTED BY postgres`, "", nil); err != nil {
		t.Fatalf("GRANTED BY postgres (as postgres): %v", err)
	}
	want := `{postgres=arwdDxtm/postgres,bob=r/postgres}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl = %q; want %q", got, want)
	}

	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie GRANTED BY bob`, "bob", nil); err != nil {
		t.Fatalf("GRANTED BY bob (as bob): %v", err)
	}
	want = `{postgres=arwdDxtm/postgres,bob=r/postgres,charlie=r/bob}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl = %q; want %q", got, want)
	}
}

// TestTryRecordTableGrantGrantedByOtherRoleErrors pins the flip side: real
// PostgreSQL rejects a GRANTED BY clause naming any role other than the
// current user with ERRCODE_FEATURE_NOT_SUPPORTED ("grantor must be current
// user"), never silently substituting it as the recorded grantor. Verified
// against real PG 18.3 (postgres/src/backend/catalog/aclchk.c:394-412).
func TestTryRecordTableGrantGrantedByOtherRoleErrors(t *testing.T) {
	s, im, oid := newGrantTestServer(t)

	err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie GRANTED BY bob`, "", nil)
	if err != errGrantorMustBeCurrentUser {
		t.Fatalf("err = %v; want errGrantorMustBeCurrentUser", err)
	}
	// The rejected statement must not have recorded anything.
	if got := im.RelaclText(oid); got != "" {
		t.Fatalf("relacl after rejected GRANT = %q; want \"\" (NULL)", got)
	}
}

// TestTryRecordTableRevokeMaterializesActualOwner pins M0134-0069 bucket 7:
// a `REVOKE ... FROM <role>` whose <role> is the object's *actual* Owner
// field (not the literal string "postgres", goopg's internal owner-ACL
// bookkeeping sentinel) must route through the owner-materialize-then-revoke
// path, exactly like a REVOKE naming "postgres" always did. Before this fix
// tryRecordTableRevoke only detected the owner branch by comparing the
// REVOKE target against the literal "postgres" sentinel, so
// `REVOKE ALL ... FROM regress_seq_user` (a non-"postgres" owner) fell
// through to a plain RevokeTablePrivilege(oid, "regress_seq_user", priv) —
// a no-op, since the owner's implicit privileges are stored under the
// "postgres" sentinel key, not under the owner's real role name. PG oracle:
// postgres/src/backend/utils/adt/acl.c pg_class_aclcheck (the owner's
// privilege is just as revocable as any other aclitem); fixture:
// postgres/src/test/regress/sql/sequence.sql lines ~645-786.
func TestTryRecordTableRevokeMaterializesActualOwner(t *testing.T) {
	s, im, oid := newGrantTestServer(t)

	tbl, ok := im.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t not found")
	}
	// newGrantTestServer's CreateTable does not stamp an owner; set one
	// explicitly so the REVOKE target below is a genuine non-"postgres" owner
	// name, matching sequence.sql's regress_seq_user fixture role.
	tbl.Owner = "regress_seq_user"

	// Before any revoke the relacl is still NULL (implicit default,
	// unmaterialized) — the owner holds every privilege without an explicit
	// aclitem.
	if got := im.RelaclText(oid); got != "" {
		t.Fatalf("relacl before revoke = %q; want \"\" (NULL)", got)
	}

	s.tryRecordTableRevoke(`REVOKE ALL ON TABLE t FROM regress_seq_user`, nil)

	// The owner's implicit default ACL must now be materialized (under the
	// "postgres" sentinel) with every privilege stripped — a non-NULL empty
	// array, not a no-op leaving relacl NULL.
	if got := im.RelaclText(oid); got != "{}" {
		t.Fatalf("relacl after REVOKE ALL FROM actual owner = %q; want %q (empty array, not NULL/no-op)", got, "{}")
	}
	if !im.IsOwnerACLRevoked(oid) {
		t.Fatal("IsOwnerACLRevoked = false after REVOKE ALL FROM actual owner; want true")
	}
	// Nothing was ever recorded under the literal role name
	// "regress_seq_user" — the revoke must have operated on the "postgres"
	// owner-ACL sentinel key, not a dead entry under the real role name.
	if im.HasTablePrivilege(oid, "regress_seq_user", "SELECT") {
		t.Fatal("HasTablePrivilege(oid, \"regress_seq_user\", SELECT) = true; the owner's ACL entry must live under the sentinel key, not the literal owner role name")
	}

	// The owner's privileges (or absence thereof) must be queryable through
	// the sentinel key: none survive a REVOKE ALL.
	if im.HasOwnerPrivilege(oid, "SELECT") || im.HasOwnerPrivilege(oid, "INSERT") {
		t.Fatal("HasOwnerPrivilege = true for some privilege after REVOKE ALL FROM actual owner; want none")
	}

	// A subsequent REVOKE (of a privilege the owner no longer holds) is a
	// harmless no-op, not a panic or a resurrection of the owner default.
	s.tryRecordTableRevoke(`REVOKE SELECT ON TABLE t FROM regress_seq_user`, nil)
	if got := im.RelaclText(oid); got != "{}" {
		t.Fatalf("relacl after second REVOKE FROM actual owner = %q; want %q (unchanged empty array)", got, "{}")
	}
}

// TestTryRecordTableGrantResolvesSearchPath pins the M0134-0163 fix: the
// GRANT/REVOKE recorders run before the executor and so have no
// executor.Context, but they must still resolve an unqualified relation name
// through the session's search_path. Catalog.LookupTable alone only tries the
// bare key, "public.<name>", and "pg_catalog.<name>", so a GRANT naming a table
// in any other schema used to resolve to nothing and record no aclitem while
// still reporting "GRANT" — leaving every `SET search_path = <schema>` regress
// case (rowsecurity.sql:35-36) running against an empty relacl.
func TestTryRecordTableGrantResolvesSearchPath(t *testing.T) {
	im := catalog.NewInMemory()
	tbl, err := im.CreateTable(parser.ObjectName{Schema: "sch", Name: "doc"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	s := &Server{cfg: Config{Catalog: im}}

	// Without "sch" on the search_path the name is unresolvable, so the
	// recorder stays a silent no-op (the pre-fix behaviour, still correct here).
	if err := s.tryRecordTableGrant(`GRANT SELECT ON doc TO bob`, "", []string{"public"}); err != nil {
		t.Fatalf("off-path GRANT: %v", err)
	}
	if got := im.RelaclText(tbl.OID); got != "" {
		t.Fatalf("relacl after off-path GRANT = %q; want empty (name not resolvable)", got)
	}

	// With "sch" on the search_path the unqualified name resolves and the
	// aclitem lands, exactly as the schema-qualified form would.
	if err := s.tryRecordTableGrant(`GRANT SELECT ON doc TO bob`, "", []string{"sch", "public"}); err != nil {
		t.Fatalf("on-path GRANT: %v", err)
	}
	want := `{postgres=arwdDxtm/postgres,bob=r/postgres}`
	if got := im.RelaclText(tbl.OID); got != want {
		t.Fatalf("relacl after on-path GRANT = %q; want %q", got, want)
	}

	// REVOKE must resolve the same relation, or the pair desynchronises.
	s.tryRecordTableRevoke(`REVOKE SELECT ON doc FROM bob`, []string{"sch", "public"})
	if got := im.RelaclText(tbl.OID); got != "" {
		t.Fatalf("relacl after on-path REVOKE = %q; want empty", got)
	}
}
