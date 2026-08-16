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
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO bob`, ""); err != nil {
		t.Fatalf("owner GRANT: %v", err)
	}
	want := `{postgres=arwdDxtm/postgres,bob=r/postgres}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl after owner GRANT = %q; want %q", got, want)
	}

	// bob (SET ROLE'd, i.e. actingRole = "bob") grants onward to charlie: the
	// new aclitem's grantor must be "bob", not "postgres".
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie`, "bob"); err != nil {
		t.Fatalf("delegated GRANT: %v", err)
	}
	want = `{postgres=arwdDxtm/postgres,bob=r/postgres,charlie=r/bob}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl after delegated GRANT = %q; want %q", got, want)
	}

	// A later re-GRANT of the same privilege by the owner restamps the
	// grantor back to the owner (PostgreSQL's aclupdate updates an existing
	// aclitem's grantor to whoever issued the latest GRANT).
	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie`, ""); err != nil {
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

	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO bob GRANTED BY postgres`, ""); err != nil {
		t.Fatalf("GRANTED BY postgres (as postgres): %v", err)
	}
	want := `{postgres=arwdDxtm/postgres,bob=r/postgres}`
	if got := im.RelaclText(oid); got != want {
		t.Fatalf("relacl = %q; want %q", got, want)
	}

	if err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie GRANTED BY bob`, "bob"); err != nil {
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

	err := s.tryRecordTableGrant(`GRANT SELECT ON TABLE t TO charlie GRANTED BY bob`, "")
	if err != errGrantorMustBeCurrentUser {
		t.Fatalf("err = %v; want errGrantorMustBeCurrentUser", err)
	}
	// The rejected statement must not have recorded anything.
	if got := im.RelaclText(oid); got != "" {
		t.Fatalf("relacl after rejected GRANT = %q; want \"\" (NULL)", got)
	}
}
