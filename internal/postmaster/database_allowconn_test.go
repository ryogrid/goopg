package postmaster

import (
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// TestConnectTemplate0Rejected pins the M0119-0006 (bs residual) contract: a
// connection naming a database whose pg_database.datallowconn is false is
// rejected post-authentication with FATAL SQLSTATE 55000 and PostgreSQL's
// verbatim message, mirroring InitPostgres
// (postgres/src/backend/utils/init/postinit.c:361-365).
//
// goopg's catalog already reported datallowconn = false for template0 — so
// pg_amcheck's `--all` filter skipped it correctly — but nothing enforced it at
// connect time and `psql -d template0` succeeded. That is what allowed a
// CREATE EXTENSION to land in template0, from where it would be copied into
// every future CREATE DATABASE using it as a template, breaking the
// pristine-template invariant the flag exists to protect.
func TestConnectTemplate0Rejected(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "template0"})

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
	if want := `database "template0" is not currently accepting connections`; got[libpq.FieldMessage] != want {
		t.Errorf("message = %q, want %q (PG 18.3 verbatim)", got[libpq.FieldMessage], want)
	}
}

// TestConnectTemplate1Accepted is the paired control, and it is not optional:
// the cheap way to make the test above pass is to refuse every template
// database, and PostgreSQL allows template1 — it is the template users are
// meant to customise. Without this arm, a blanket refusal would look correct.
func TestConnectTemplate1Accepted(t *testing.T) {
	addr, stop := startServerWithCatalog(t, catalog.NewInMemory())
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "database": "template1"})

	r := libpq.NewFrameReader(conn)
	for {
		fr, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frames after startup: %v", err)
		}
		switch fr.Type {
		case libpq.MsgErrorResponse:
			got := decodeFields(t, fr.Payload)
			t.Fatalf("template1 connection rejected (%s): %s",
				got[libpq.FieldSQLState], got[libpq.FieldMessage])
		case libpq.MsgReadyForQuery:
			return // connected, as PostgreSQL allows
		}
	}
}
