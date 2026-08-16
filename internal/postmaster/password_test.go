package postmaster

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
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

// startAuthTestServer wires both a Policy and a UserStore into a Server
// bound to an ephemeral port.
func startAuthTestServer(t *testing.T, hba string, store auth.UserStore) (string, func()) {
	t.Helper()
	rs, err := auth.ParseHBAReader(strings.NewReader(hba), "test.conf")
	if err != nil {
		t.Fatalf("parse hba: %v", err)
	}
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Policy:           rs,
		UserStore:        store,
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

// writePasswordMessage encodes a 'p' message carrying a NUL-terminated
// password string.
func writePasswordMessage(t *testing.T, conn net.Conn, password string) {
	t.Helper()
	body := append([]byte(password), 0)
	hdr := make([]byte, 5)
	hdr[0] = libpq.MsgPasswordMessage
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := conn.Write(append(hdr, body...)); err != nil {
		t.Fatalf("write PasswordMessage: %v", err)
	}
}

// TestPasswordAuthSuccess covers the cleartext "password" method end-to-end.
func TestPasswordAuthSuccess(t *testing.T) {
	store := auth.NewMapUserStore()
	store.Set("alice", auth.NewPlaintextCredential("hunter2"))

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 password\n", store)
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
		t.Fatalf("read AuthenticationCleartextPassword: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationCleartextPasswd {
		t.Fatalf("first frame = (%c, %v), want R/3", f.Type, f.Payload)
	}

	writePasswordMessage(t, conn, "hunter2")

	// Expect AuthenticationOk next.
	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationOk: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationOK {
		t.Fatalf("second frame = (%c, %v), want R/0", f.Type, f.Payload)
	}
	// And the parameter-status block follows; we don't drain the rest.
}

// TestPasswordAuthWrongPasswordClosesWithFATAL pins the contract:
// wrong cleartext password yields FATAL ErrorResponse with SQLSTATE
// 28000 and the connection closes.
func TestPasswordAuthWrongPasswordClosesWithFATAL(t *testing.T) {
	store := auth.NewMapUserStore()
	store.Set("alice", auth.NewPlaintextCredential("hunter2"))

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 password\n", store)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice"})

	r := libpq.NewFrameReader(conn)
	if _, err := r.ReadFrame(); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	writePasswordMessage(t, conn, "WRONG")

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read FATAL: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("frame = %c, want E", f.Type)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSeverity] != "FATAL" {
		t.Errorf("severity = %q, want FATAL", got[libpq.FieldSeverity])
	}
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want 28000", got[libpq.FieldSQLState])
	}
}

// TestMD5AuthSuccess covers the md5 method end-to-end. We perform the
// same client-side computation libpq does, so the test pins both the
// salt extraction and the response computation to the upstream recipe.
func TestMD5AuthSuccess(t *testing.T) {
	store := auth.NewMapUserStore()
	store.Set("alice", auth.NewMD5Credential("alice", "hunter2"))

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 md5\n", store)
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
		t.Fatalf("read AuthenticationMD5Password: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload[:4]) != libpq.AuthenticationMD5Passwd {
		t.Fatalf("first frame = (%c, %v), want R/5", f.Type, f.Payload)
	}
	if len(f.Payload) != 8 {
		t.Fatalf("md5 challenge payload len = %d, want 8 (subcode + 4-byte salt)", len(f.Payload))
	}
	var salt [4]byte
	copy(salt[:], f.Payload[4:])

	// Client-side response (mirrors libpq):
	innerSum := md5.Sum([]byte("hunter2alice"))
	inner := hex.EncodeToString(innerSum[:])
	outerSum := md5.Sum([]byte(inner + string(salt[:])))
	response := "md5" + hex.EncodeToString(outerSum[:])

	writePasswordMessage(t, conn, response)

	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationOk: %v", err)
	}
	if f.Type != libpq.MsgAuthentication ||
		binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationOK {
		t.Fatalf("post-md5 frame = (%c, %v), want R/0", f.Type, f.Payload)
	}
}

// TestMD5AuthWrongPasswordRejected: wrong response → FATAL/28000.
func TestMD5AuthWrongPasswordRejected(t *testing.T) {
	store := auth.NewMapUserStore()
	store.Set("alice", auth.NewMD5Credential("alice", "hunter2"))

	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 md5\n", store)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice"})

	r := libpq.NewFrameReader(conn)
	if _, err := r.ReadFrame(); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	// Send a structurally-valid but wrong response.
	writePasswordMessage(t, conn, "md5"+hex.EncodeToString(make([]byte, 16)))

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read FATAL: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("frame = %c, want E", f.Type)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want 28000", got[libpq.FieldSQLState])
	}
}

// TestPasswordAuthUnknownUser: lookup miss is treated identically to
// wrong password on the wire (same SQLSTATE, no information leak).
func TestPasswordAuthUnknownUser(t *testing.T) {
	store := auth.NewMapUserStore() // empty
	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 password\n", store)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "nobody"})

	r := libpq.NewFrameReader(conn)
	if _, err := r.ReadFrame(); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	writePasswordMessage(t, conn, "anything")

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read FATAL: %v", err)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSQLState] != string(errcodes.InvalidAuthorizationSpecification) {
		t.Errorf("SQLSTATE = %q, want 28000 (no leak between unknown-user and wrong-password)", got[libpq.FieldSQLState])
	}
}

// TestPasswordMethodWithNilUserStoreFailsCleanly pins the safety net:
// a misconfigured server (password method but no UserStore) should
// emit FATAL with SQLSTATE 0A000 (feature_not_supported) rather than
// silently accepting or panicking.
func TestPasswordMethodWithNilUserStoreFailsCleanly(t *testing.T) {
	addr, stop := startAuthTestServer(t, "host all all 127.0.0.1/32 password\n", nil)
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
		t.Fatalf("read FATAL: %v", err)
	}
	got := decodeFields(t, f.Payload)
	if got[libpq.FieldSQLState] != string(errcodes.FeatureNotSupported) {
		t.Errorf("SQLSTATE = %q, want 0A000", got[libpq.FieldSQLState])
	}
}
