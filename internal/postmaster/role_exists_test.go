package postmaster

import (
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// TestConnectNonexistentRoleRejected pins the M0110-0003 (AC-002 gap #7b)
// contract: a connection whose role is absent from goopg's runtime role
// authority is rejected post-authentication with a FATAL ErrorResponse carrying
// SQLSTATE 28000 and the PG-compatible `role "no_such_user" does not exist`
// message, after which the server closes the connection. This mirrors PG's
// InitializeSessionUserId (utils/init/miscinit.c) and is what pg_amcheck's
// 002_nonesuch `--username no_such_user` case depends on. The trust policy
// admits loopback, so the role check is the only gate the handshake hits here
// (the database "postgres" is seeded, and the role check runs before it).
func TestConnectNonexistentRoleRejected(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "no_such_user", "database": "postgres"})

	// checkAuth sends AuthenticationOk first (PG authenticates before the role
	// is established); the FATAL 28000 follows. Read until the ErrorResponse.
	r := libpq.NewFrameReader(conn)
	var f libpq.Frame
	for {
		fr, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read FATAL ErrorResponse: %v", err)
		}
		if fr.Type == libpq.MsgErrorResponse {
			f = fr
			break
		}
		if fr.Type != libpq.MsgAuthentication {
			t.Fatalf("unexpected frame %c before ErrorResponse", fr.Type)
		}
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSeverity] != "FATAL" {
		t.Errorf("severity = %q, want FATAL", got[libpq.FieldSeverity])
	}
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want %q (28000)",
			got[libpq.FieldSQLState], errcodes.InvalidAuthorizationSpecification)
	}
	if want := `role "no_such_user" does not exist`; got[libpq.FieldMessage] != want {
		t.Errorf("message = %q, want %q", got[libpq.FieldMessage], want)
	}
	// Server should close after FATAL: a subsequent read returns EOF.
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected EOF after FATAL, got nil")
	}
}

// TestConnectSeededRoleAccepted is the positive twin: the always-seeded
// `postgres` role passes the existence check, so the handshake proceeds to
// AuthenticationOk rather than a 28000 rejection. A regression that rejected
// every role (e.g. a mis-seeded role set) would surface here.
func TestConnectSeededRoleAccepted(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "postgres"})

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if f.Type == libpq.MsgErrorResponse {
		got := decodeFields(t, f.Payload)
		t.Fatalf("role postgres rejected: SQLSTATE=%q msg=%q",
			got[libpq.FieldSQLState], got[libpq.FieldMessage])
	}
	if f.Type != libpq.MsgAuthentication {
		t.Errorf("first frame = %c, want R (AuthenticationOk)", f.Type)
	}
}
