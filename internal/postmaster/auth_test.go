package postmaster

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq/auth"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// startServerWithPolicy spins up a Server with a caller-supplied policy and
// returns the dial address and a cancel function.
func startServerWithPolicy(t *testing.T, p auth.Policy) (string, func()) {
	t.Helper()
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Policy:           p,
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

// TestRejectPolicyClosesWithFATAL covers the v0 reject path: a policy that
// rejects the connection causes the server to emit a FATAL ErrorResponse
// with SQLSTATE 28000 and close, instead of completing the startup.
func TestRejectPolicyClosesWithFATAL(t *testing.T) {
	rejectAll, err := auth.ParseHBAReader(strings.NewReader(""), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServerWithPolicy(t, rejectAll)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice"})

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read FATAL ErrorResponse: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("frame = %c, want E", f.Type)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSeverity] != "FATAL" {
		t.Errorf("severity = %q, want FATAL", got[libpq.FieldSeverity])
	}
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want %q (28000)",
			got[libpq.FieldSQLState], errcodes.InvalidAuthorizationSpecification)
	}
	// Server should now close: another read returns EOF / unexpected EOF.
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected EOF after FATAL, got nil")
	}
}

// TestUnsupportedAuthMethodReportsFeatureNotSupported covers the seam
// where a parsed-but-unimplemented method (e.g. scram-sha-256) lands in
// v0 — the operator sees a clear error rather than a hang.
func TestUnsupportedAuthMethodReportsFeatureNotSupported(t *testing.T) {
	rs, err := auth.ParseHBAReader(
		strings.NewReader("host all all 0.0.0.0/0 scram-sha-256\n"), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	addr, stop := startServerWithPolicy(t, rs)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice"})

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSQLState] != string(errcodes.FeatureNotSupported) {
		t.Errorf("SQLSTATE = %q, want %q (0A000)",
			got[libpq.FieldSQLState], errcodes.FeatureNotSupported)
	}
}

// TestDefaultPolicyTrustsLoopback locks in that the default-policy
// behaviour (no explicit Policy in Config) keeps loopback connections
// working — this is the contract the existing test suite relies on.
func TestDefaultPolicyTrustsLoopback(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice"})
	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if f.Type != libpq.MsgAuthentication {
		t.Fatalf("frame = %c, want R (AuthenticationOk)", f.Type)
	}
	if len(f.Payload) != 4 || binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationOK {
		t.Errorf("AuthenticationOk payload = %v", f.Payload)
	}
}

func decodeFields(t *testing.T, payload []byte) map[byte]string {
	t.Helper()
	out := map[byte]string{}
	for len(payload) > 0 {
		if payload[0] == 0 {
			return out
		}
		code := payload[0]
		payload = payload[1:]
		end := -1
		for i, b := range payload {
			if b == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			t.Fatalf("ErrorResponse field %c missing terminator", code)
		}
		out[code] = string(payload[:end])
		payload = payload[end+1:]
	}
	return out
}
