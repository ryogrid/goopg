package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// startServerWithCatalog spins up a Server wired with the given catalog so the
// connection-startup database-existence check (M0110-0003 AC-002 gap #3) is
// active. The default policy trusts loopback, so the only gate the handshake
// hits here is the database check itself.
func startServerWithCatalog(t *testing.T, cat catalog.Catalog) (string, func()) {
	t.Helper()
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          cat,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	addr := srv.Addr().String()
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Server.Run returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return")
		}
	}
	return addr, stop
}

// TestConnectNonexistentDatabaseRejected pins the M0110-0003 (AC-002 gap #3)
// contract: a connection that names a database absent from the registry is
// rejected post-authentication with a FATAL ErrorResponse carrying SQLSTATE
// 3D000 and the PG-compatible `database "qqq" does not exist` message, after
// which the server closes the connection. pg_amcheck's 002_nonesuch test
// depends on this exact wire behaviour.
func TestConnectNonexistentDatabaseRejected(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "qqq"})

	// checkAuth sends AuthenticationOk first (mirroring PG, which authenticates
	// before InitPostgres selects the database); the FATAL 3D000 follows. Read
	// frames until the ErrorResponse arrives.
	r := protocol.NewFrameReader(conn)
	var f protocol.Frame
	for {
		fr, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read FATAL ErrorResponse: %v", err)
		}
		if fr.Type == protocol.MsgErrorResponse {
			f = fr
			break
		}
		if fr.Type != protocol.MsgAuthentication {
			t.Fatalf("unexpected frame %c before ErrorResponse", fr.Type)
		}
	}
	got := decodeFields(t, f.Payload)
	if got[protocol.FieldSeverity] != "FATAL" {
		t.Errorf("severity = %q, want FATAL", got[protocol.FieldSeverity])
	}
	if got[protocol.FieldSQLState] != string(sqlstate.InvalidCatalogName) {
		t.Errorf("SQLSTATE = %q, want %q (3D000)",
			got[protocol.FieldSQLState], sqlstate.InvalidCatalogName)
	}
	if want := `database "qqq" does not exist`; got[protocol.FieldMessage] != want {
		t.Errorf("message = %q, want %q", got[protocol.FieldMessage], want)
	}
	// Server should close after FATAL: a subsequent read returns EOF.
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected EOF after FATAL, got nil")
	}
}

// TestConnectBootstrapDatabasesAccepted confirms the three seeded bootstrap
// databases (postgres, template1, template0) pass the existence check — the
// handshake proceeds to AuthenticationOk rather than a 3D000 rejection. This is
// the positive twin of the rejection test: template1/template0 must be present
// in the registry so pg_amcheck's --all / `template1` cases reach the relation
// query instead of failing at connect.
func TestConnectBootstrapDatabasesAccepted(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	for _, db := range []string{"postgres", "template1", "template0"} {
		t.Run(db, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": db})

			r := protocol.NewFrameReader(conn)
			f, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("read first frame: %v", err)
			}
			if f.Type == protocol.MsgErrorResponse {
				got := decodeFields(t, f.Payload)
				t.Fatalf("database %q rejected: SQLSTATE=%q msg=%q",
					db, got[protocol.FieldSQLState], got[protocol.FieldMessage])
			}
			if f.Type != protocol.MsgAuthentication {
				t.Errorf("first frame = %c, want R (AuthenticationOk)", f.Type)
			}
		})
	}
}
