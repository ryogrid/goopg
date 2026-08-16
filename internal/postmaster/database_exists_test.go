package postmaster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// startServerWithCatalog spins up a Server wired with the given catalog so the
// connection-startup database-existence check (M0110-0003 AC-002 gap #3) is
// active. The default policy trusts loopback, so the only gate the handshake
// hits here is the database check itself.
func startServerWithCatalog(t *testing.T, cat catalog.Catalog) (string, func()) {
	t.Helper()
	return startServerWithCatalogAndActivity(t, cat, nil)
}

// startServerWithCatalogAndActivity is startServerWithCatalog plus an
// optional activity registry, needed by the M0119-0006 (AC-002 residual #2)
// positive-datconnlimit tests: that check is gated on Config.Activity being
// non-nil (mirroring every other Activity-registry-driven check in
// handleStartup), which startServerWithCatalog's callers don't need.
func startServerWithCatalogAndActivity(t *testing.T, cat catalog.Catalog, act *activity.Registry) (string, func()) {
	t.Helper()
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          cat,
		Activity:         act,
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
	if got[libpq.FieldSQLState] != string(errcodes.InvalidCatalogName) {
		t.Errorf("SQLSTATE = %q, want %q (3D000)",
			got[libpq.FieldSQLState], errcodes.InvalidCatalogName)
	}
	if want := `database "qqq" does not exist`; got[libpq.FieldMessage] != want {
		t.Errorf("message = %q, want %q", got[libpq.FieldMessage], want)
	}
	// Server should close after FATAL: a subsequent read returns EOF.
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected EOF after FATAL, got nil")
	}
}

// TestConnectInvalidDatconnlimitDatabaseRejected pins the M0119-0006 (AC-002
// residual #1) contract: a connection naming a database whose `datconnlimit`
// is the `-2` "invalid database" sentinel (left partway through DROP
// DATABASE, per PG's DATCONNLIMIT_INVALID_DB) is rejected post-authentication
// with a FATAL ErrorResponse carrying SQLSTATE 55000 and the PG-compatible
// `cannot connect to invalid database "..."` message, mirroring
// postinit.c's InitPostgres check.
func TestConnectInvalidDatconnlimitDatabaseRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateDatabase("brokendb", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatal(err)
	}
	if !cat.SetDatabaseConnLimit("brokendb", catalog.DatconnlimitInvalidDB) {
		t.Fatal("SetDatabaseConnLimit: database not found")
	}
	addr, stop := startServerWithCatalog(t, cat)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "brokendb"})

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
	if got[libpq.FieldSQLState] != string(errcodes.ObjectNotInPrerequisiteState) {
		t.Errorf("SQLSTATE = %q, want %q (55000)",
			got[libpq.FieldSQLState], errcodes.ObjectNotInPrerequisiteState)
	}
	if want := `cannot connect to invalid database "brokendb"`; got[libpq.FieldMessage] != want {
		t.Errorf("message = %q, want %q", got[libpq.FieldMessage], want)
	}
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

			r := libpq.NewFrameReader(conn)
			f, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("read first frame: %v", err)
			}
			if f.Type == libpq.MsgErrorResponse {
				got := decodeFields(t, f.Payload)
				t.Fatalf("database %q rejected: SQLSTATE=%q msg=%q",
					db, got[libpq.FieldSQLState], got[libpq.FieldMessage])
			}
			if f.Type != libpq.MsgAuthentication {
				t.Errorf("first frame = %c, want R (AuthenticationOk)", f.Type)
			}
		})
	}
}

// TestConnectExceedsPositiveDatconnlimitRejected pins the M0119-0006 (AC-002
// residual #2) contract: once a database's live connection count exceeds its
// positive `datconnlimit`, a further non-superuser connection is rejected
// post-authentication with a FATAL ErrorResponse carrying SQLSTATE 53300 and
// the PG-compatible `too many connections for database "%s"` message,
// mirroring postinit.c's CheckMyDatabase check. Requires a real
// activity.Registry (see startServerWithCatalogAndActivity), since the check
// is gated on Config.Activity being wired, exactly like every other
// activity-registry-driven check in handleStartup.
func TestConnectExceedsPositiveDatconnlimitRejected(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateDatabase("limiteddb", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatal(err)
	}
	if !cat.SetDatabaseConnLimit("limiteddb", 1) {
		t.Fatal("SetDatabaseConnLimit: database not found")
	}
	cat.RegisterRole("alice")
	act := activity.NewActivityRegistry(transam.DefaultProcArraySize)
	addr, stop := startServerWithCatalogAndActivity(t, cat, act)
	defer stop()

	dial := func(t *testing.T) net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		writeStartupPacket(t, conn, map[string]string{"user": "alice", "database": "limiteddb"})
		return conn
	}

	// First connection: count becomes 1, limit is 1 — must succeed.
	first := dial(t)
	defer first.Close()
	r1 := libpq.NewFrameReader(first)
	f1, err := r1.ReadFrame()
	if err != nil {
		t.Fatalf("first connection: read first frame: %v", err)
	}
	if f1.Type != libpq.MsgAuthentication {
		got := decodeFields(t, f1.Payload)
		t.Fatalf("first connection rejected: SQLSTATE=%q msg=%q",
			got[libpq.FieldSQLState], got[libpq.FieldMessage])
	}

	// Second connection: count becomes 2 > limit 1 — must be rejected.
	second := dial(t)
	defer second.Close()
	r2 := libpq.NewFrameReader(second)
	var f2 libpq.Frame
	for {
		fr, err := r2.ReadFrame()
		if err != nil {
			t.Fatalf("second connection: read FATAL ErrorResponse: %v", err)
		}
		if fr.Type == libpq.MsgErrorResponse {
			f2 = fr
			break
		}
		if fr.Type != libpq.MsgAuthentication {
			t.Fatalf("unexpected frame %c before ErrorResponse", fr.Type)
		}
	}
	got := decodeFields(t, f2.Payload)
	if got[libpq.FieldSeverity] != "FATAL" {
		t.Errorf("severity = %q, want FATAL", got[libpq.FieldSeverity])
	}
	if got[libpq.FieldSQLState] != string(errcodes.TooManyConnections) {
		t.Errorf("SQLSTATE = %q, want %q (53300)",
			got[libpq.FieldSQLState], errcodes.TooManyConnections)
	}
	if want := `too many connections for database "limiteddb"`; got[libpq.FieldMessage] != want {
		t.Errorf("message = %q, want %q", got[libpq.FieldMessage], want)
	}
	if _, err := r2.ReadFrame(); err == nil {
		t.Error("expected EOF after FATAL, got nil")
	}
}

// TestConnectNonSuperuserFirstConnectionUnlimitedDatabaseAccepted pins the
// M0119-0006-DATCONNLIMIT-DEFAULT fix: a database that never had
// SetDatabaseConnLimit called on it (i.e. every database in a fresh cluster)
// must report an effectively unlimited datconnlimit, matching PG's `-1`
// pg_database.h default — not the Go zero-value `0`, which would reject a
// non-superuser role's very first connection (`limit >= 0 && count(1) >
// limit(0)`). Uses a real activity.Registry so the positive-datconnlimit
// check in handleStartup actually runs.
func TestConnectNonSuperuserFirstConnectionUnlimitedDatabaseAccepted(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateDatabase("freshdb", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatal(err)
	}
	cat.RegisterRole("alice")
	act := activity.NewActivityRegistry(transam.DefaultProcArraySize)
	addr, stop := startServerWithCatalogAndActivity(t, cat, act)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice", "database": "freshdb"})

	// AuthenticationOk is always sent before the datconnlimit check runs, so
	// a single first-frame read can't distinguish accept from reject here —
	// read on until ReadyForQuery (full acceptance) or an ErrorResponse
	// (the datconnlimit-default bug would produce one) arrives.
	r := libpq.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Type == libpq.MsgErrorResponse {
			got := decodeFields(t, f.Payload)
			t.Fatalf("connection rejected: SQLSTATE=%q msg=%q",
				got[libpq.FieldSQLState], got[libpq.FieldMessage])
		}
		if f.Type == libpq.MsgReadyForQuery {
			break
		}
	}
}

// TestConnectPositiveDatconnlimitSuperuserBypasses confirms the bootstrap
// superuser ("postgres") connects past a positive datconnlimit that would
// reject any other role, mirroring CheckMyDatabase's `!am_superuser` gate:
// the superuser's own connections are still counted (CountDBConnections has
// no role filter) but the reject check itself never applies to them.
func TestConnectPositiveDatconnlimitSuperuserBypasses(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateDatabase("limiteddb2", catalog.BootstrapSuperuserOID); err != nil {
		t.Fatal(err)
	}
	if !cat.SetDatabaseConnLimit("limiteddb2", 1) {
		t.Fatal("SetDatabaseConnLimit: database not found")
	}
	act := activity.NewActivityRegistry(transam.DefaultProcArraySize)
	addr, stop := startServerWithCatalogAndActivity(t, cat, act)
	defer stop()

	for i := range 3 {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "limiteddb2"})

		r := libpq.NewFrameReader(conn)
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("connection %d: read first frame: %v", i, err)
		}
		if f.Type != libpq.MsgAuthentication {
			got := decodeFields(t, f.Payload)
			t.Fatalf("connection %d rejected: SQLSTATE=%q msg=%q",
				i, got[libpq.FieldSQLState], got[libpq.FieldMessage])
		}
	}
}
