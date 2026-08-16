package postmaster

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/storage"
)

// TestE2EOversizedMessageDoD verifies that when a client sends a SQL message
// whose payload exceeds the server's per-connection limit, the server:
//   (a) sends a proper ErrorResponse (not a silent TCP close), and
//   (b) continues serving subsequent queries on the same connection.
//
// This is the DoD for M0052-0001 / M0052-0002: the HammerDB TPC-H loader
// sends INSERT statements that can approach or exceed the message limit.
// Before this fix the server silently dropped the connection. The test uses
// Config.MaxQueryPayloadBytes=1024 so the test message stays tiny (avoids
// sending multi-MiB data over TCP in a unit test while still exercising the
// same code path).
func TestE2EOversizedMessageDoD(t *testing.T) {
	// Start a minimal server backed by real storage.
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	const testLimit = 1024 // tiny per-connection limit so the test doesn't need to send MiBs
	srv := New(Config{
		Address:              "127.0.0.1:0",
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:       25 * time.Millisecond,
		HandshakeTimeout:     2 * time.Second,
		Catalog:              catalog.NewInMemory(),
		Pool:                 pool,
		TxnMgr:               transam.NewManager(),
		MaxQueryPayloadBytes: testLimit,
	})
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	t.Cleanup(func() {
		<-done
	})

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Startup message: protocol version 3.0, user=postgres, database=postgres.
	startup := buildStartupMessage(map[string]string{
		"user":     "postgres",
		"database": "postgres",
	})
	if _, err := conn.Write(startup); err != nil {
		t.Fatalf("write startup: %v", err)
	}

	// Drain startup reply messages until ReadyForQuery.
	if err := drainUntilReadyForQuery(conn); err != nil {
		t.Fatalf("drain startup: %v", err)
	}

	// Send an oversized MsgQuery (> testLimit = 1024 bytes).
	oversizedSQL := bytes.Repeat([]byte("SELECT 1;\n"), testLimit/9+10)
	if err := sendSimpleQuery(conn, oversizedSQL); err != nil {
		t.Fatalf("send oversized query: %v", err)
	}

	// Expect an ErrorResponse (not a connection close).
	f, err := readOneFrame(conn)
	if err != nil {
		t.Fatalf("read after oversized query: connection dropped (got %v), wanted ErrorResponse", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		// A connection-level error would have surfaced above. Any non-Error
		// response means the server is in an unexpected state.
		t.Fatalf("expected ErrorResponse after oversized message, got %q", f.Type)
	}

	// Drain remaining frames (there may be a ReadyForQuery following the error).
	drainFrames(conn)

	// Send a valid query to confirm the session is still alive.
	if err := sendSimpleQuery(conn, []byte("SELECT 1;")); err != nil {
		t.Fatalf("send SELECT 1 after recovery: %v", err)
	}
	f, err = readOneFrame(conn)
	if err != nil {
		t.Fatalf("read after SELECT 1: %v", err)
	}
	// The first non-ErrorResponse frame after a valid query is the data
	// (RowDescription, DataRow, CommandComplete, or similar).
	if f.Type == libpq.MsgErrorResponse {
		t.Fatalf("SELECT 1 failed after oversized-message recovery: ErrorResponse payload %q", f.Payload)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func buildStartupMessage(params map[string]string) []byte {
	var body bytes.Buffer
	// Protocol version 3.0 (0x00030000).
	binary.Write(&body, binary.BigEndian, uint32(196608))
	for k, v := range params {
		body.WriteString(k)
		body.WriteByte(0)
		body.WriteString(v)
		body.WriteByte(0)
	}
	body.WriteByte(0) // terminator
	var out bytes.Buffer
	binary.Write(&out, binary.BigEndian, uint32(4+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func sendSimpleQuery(conn net.Conn, sql []byte) error {
	payload := append(sql, 0) // null-terminate
	total := uint32(4 + len(payload))
	hdr := []byte{
		libpq.MsgQuery,
		byte(total >> 24), byte(total >> 16), byte(total >> 8), byte(total),
	}
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func readOneFrame(conn net.Conn) (libpq.Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return libpq.Frame{}, err
	}
	total := binary.BigEndian.Uint32(hdr[1:])
	payloadLen := int(total) - 4
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return libpq.Frame{}, err
		}
	}
	return libpq.Frame{Type: hdr[0], Payload: payload}, nil
}

func drainUntilReadyForQuery(conn net.Conn) error {
	for {
		f, err := readOneFrame(conn)
		if err != nil {
			return err
		}
		if f.Type == libpq.MsgReadyForQuery {
			return nil
		}
	}
}

func drainFrames(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		f, err := readOneFrame(conn)
		if err != nil {
			break
		}
		if f.Type == libpq.MsgReadyForQuery {
			break
		}
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
}
