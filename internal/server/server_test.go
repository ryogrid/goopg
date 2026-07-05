package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/protocol"
)

// startTestServer launches a Server bound to an ephemeral port and returns
// the dial address plus a cancel function that triggers graceful shutdown.
func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
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
				t.Errorf("Server.Run returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
	}
	return addr, stop
}

// writeStartupPacket encodes a regular protocol-3.0 StartupMessage to w.
func writeStartupPacket(t *testing.T, w io.Writer, params map[string]string) {
	t.Helper()
	body := make([]byte, 4) // protocol version
	binary.BigEndian.PutUint32(body, protocol.ProtocolVersion3_0)
	for k, v := range params {
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
		body = append(body, 0)
	}
	body = append(body, 0) // empty key terminator
	pkt := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(pkt[:4], uint32(4+len(body)))
	copy(pkt[4:], body)
	if _, err := w.Write(pkt); err != nil {
		t.Fatalf("write startup packet: %v", err)
	}
}

// TestStartupHandshake covers the contract from
// docs/design/0002-wire-protocol.md: a client that sends a regular v3.0
// StartupMessage receives AuthenticationOk, a ParameterStatus block
// containing the documented keys, BackendKeyData, and ReadyForQuery('I').
func TestStartupHandshake(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	writeStartupPacket(t, conn, map[string]string{
		"user":             "alice",
		"database":         "postgres",
		"application_name": "test",
	})

	r := protocol.NewFrameReader(conn)
	// 1: AuthenticationOk
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationOk: %v", err)
	}
	if f.Type != protocol.MsgAuthentication {
		t.Fatalf("expected 'R', got %c", f.Type)
	}
	if len(f.Payload) != 4 || binary.BigEndian.Uint32(f.Payload) != protocol.AuthenticationOK {
		t.Fatalf("AuthenticationOk payload = %v, want 0", f.Payload)
	}

	// 2..N: ParameterStatus messages until something that isn't 'S'.
	wantKeys := map[string]bool{
		"server_version":              false,
		"server_encoding":             false,
		"client_encoding":             false,
		"application_name":            false,
		"DateStyle":                   false,
		"TimeZone":                    false,
		"integer_datetimes":           false,
		"standard_conforming_strings": false,
	}
	for {
		f, err = r.ReadFrame()
		if err != nil {
			t.Fatalf("read ParameterStatus: %v", err)
		}
		if f.Type != protocol.MsgParameterStatus {
			break
		}
		key, val := splitParamStatus(t, f.Payload)
		if _, ok := wantKeys[key]; ok {
			wantKeys[key] = true
		}
		if key == "application_name" && val != "test" {
			t.Errorf("application_name=%q, want %q (echoed from startup)", val, "test")
		}
	}
	for k, seen := range wantKeys {
		if !seen {
			t.Errorf("ParameterStatus block missing required key %q", k)
		}
	}

	// f is now the message after the last ParameterStatus.
	if f.Type != protocol.MsgBackendKeyData {
		t.Fatalf("expected BackendKeyData ('K'), got %c", f.Type)
	}
	if len(f.Payload) != 8 {
		t.Fatalf("BackendKeyData payload len = %d, want 8", len(f.Payload))
	}

	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read ReadyForQuery: %v", err)
	}
	if f.Type != protocol.MsgReadyForQuery || len(f.Payload) != 1 || f.Payload[0] != 'I' {
		t.Fatalf("ReadyForQuery = (%c, %v), want (Z, [I])", f.Type, f.Payload)
	}
}

// TestSSLRequestRejectedThenStartup verifies the SSLRequest -> 'N' -> resend
// startup flow. libpq does this by default unless sslmode=disable.
func TestSSLRequestRejectedThenStartup(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// SSLRequest packet: 8-byte length=8, body = NegotiateSSLCode.
	pkt := make([]byte, 8)
	binary.BigEndian.PutUint32(pkt[:4], 8)
	binary.BigEndian.PutUint32(pkt[4:], protocol.NegotiateSSLCode)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatal(err)
	}

	resp := make([]byte, 1)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read SSL response: %v", err)
	}
	if resp[0] != 'N' {
		t.Fatalf("SSL response = %c, want N", resp[0])
	}

	// Now send the actual startup packet.
	writeStartupPacket(t, conn, map[string]string{"user": "u"})
	r := protocol.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read after SSL rejection: %v", err)
	}
	if f.Type != protocol.MsgAuthentication {
		t.Fatalf("expected AuthenticationOk after SSL rejection, got %c", f.Type)
	}
}

// TestUnsupportedProtocolVersion verifies the FATAL path when a client
// requests something other than 3.0.
func TestUnsupportedProtocolVersion(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// 8-byte startup with version = 4.0 (unsupported).
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, 4<<16)
	body = append(body, 0) // empty key terminator
	pkt := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(pkt[:4], uint32(4+len(body)))
	copy(pkt[4:], body)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatal(err)
	}

	r := protocol.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read ErrorResponse: %v", err)
	}
	if f.Type != protocol.MsgErrorResponse {
		t.Fatalf("expected ErrorResponse, got %c", f.Type)
	}
}

// TestGracefulShutdown ensures cancelling the server's context causes Run
// to return promptly even with an idle connection open.
func TestGracefulShutdown(t *testing.T) {
	srv := New(Config{
		Address:        "127.0.0.1:0",
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline: 25 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeStartupPacket(t, conn, map[string]string{"user": "u"})

	// Drain the startup reply so the connection is idle.
	r := protocol.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			break
		}
		if f.Type == protocol.MsgReadyForQuery {
			break
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel")
	}
}

// splitParamStatus splits a ParameterStatus payload "key\0value\0".
func splitParamStatus(t *testing.T, p []byte) (string, string) {
	t.Helper()
	for i, b := range p {
		if b == 0 {
			if len(p) < 2 || p[len(p)-1] != 0 {
				t.Fatalf("ParameterStatus payload not NUL-terminated: %v", p)
			}
			return string(p[:i]), string(p[i+1 : len(p)-1])
		}
	}
	t.Fatalf("ParameterStatus payload missing key terminator: %v", p)
	return "", ""
}

// TestTrackIOTimingOnChangePropagatesToActivityRegistry verifies M0122-0003's
// runtime-SET follow-up: New() wires a GUC OnChange hook so `SET
// track_io_timing` immediately updates the calling backend's flag in the
// activity registry (which the always-installed I/O wait-event hooks in
// internal/initdb/open.go consult via LookupTrackedGoroutine), without
// requiring a server restart or hook reinstallation.
func TestTrackIOTimingOnChangePropagatesToActivityRegistry(t *testing.T) {
	reg := config.BuildDefaultRegistry()
	act := activity.NewActivityRegistry(4)
	// New() registers the OnChange hook against cfg.Registry as a side
	// effect; it doesn't need to be Run() for that wiring to take effect.
	New(Config{
		Address:  "127.0.0.1:0",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: reg,
		Activity: act,
	})

	act.Register(&activity.Backend{PID: "1", State: "active"})
	procNum := int32(0)
	activity.SetCurrentGoroutine(act, procNum)
	defer activity.ClearCurrentGoroutine()

	if act.TrackIOTiming(procNum) {
		t.Fatalf("TrackIOTiming before any SET = true, want false")
	}
	if _, _, ok := act.LookupTrackedGoroutine(); ok {
		t.Fatalf("LookupTrackedGoroutine before any SET = ok, want false")
	}

	sess := config.NewSessionRegistry(reg)
	if err := sess.Set("track_io_timing", "on", false); err != nil {
		t.Fatalf("SET track_io_timing = on: %v", err)
	}
	if !act.TrackIOTiming(procNum) {
		t.Errorf("TrackIOTiming after SET on = false, want true")
	}
	if _, pn, ok := act.LookupTrackedGoroutine(); !ok || pn != procNum {
		t.Errorf("LookupTrackedGoroutine after SET on = (_, %d, %v), want (_, %d, true)", pn, ok, procNum)
	}

	if err := sess.Set("track_io_timing", "off", false); err != nil {
		t.Fatalf("SET track_io_timing = off: %v", err)
	}
	if act.TrackIOTiming(procNum) {
		t.Errorf("TrackIOTiming after SET off = true, want false")
	}
}
