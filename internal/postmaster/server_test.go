package postmaster

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
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
	binary.BigEndian.PutUint32(body, libpq.ProtocolVersion3_0)
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

	r := libpq.NewFrameReader(conn)
	// 1: AuthenticationOk
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read AuthenticationOk: %v", err)
	}
	if f.Type != libpq.MsgAuthentication {
		t.Fatalf("expected 'R', got %c", f.Type)
	}
	if len(f.Payload) != 4 || binary.BigEndian.Uint32(f.Payload) != libpq.AuthenticationOK {
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
		if f.Type != libpq.MsgParameterStatus {
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
	if f.Type != libpq.MsgBackendKeyData {
		t.Fatalf("expected BackendKeyData ('K'), got %c", f.Type)
	}
	if len(f.Payload) != 8 {
		t.Fatalf("BackendKeyData payload len = %d, want 8", len(f.Payload))
	}

	f, err = r.ReadFrame()
	if err != nil {
		t.Fatalf("read ReadyForQuery: %v", err)
	}
	if f.Type != libpq.MsgReadyForQuery || len(f.Payload) != 1 || f.Payload[0] != 'I' {
		t.Fatalf("ReadyForQuery = (%c, %v), want (Z, [I])", f.Type, f.Payload)
	}
}

// TestStartupPacketAppliesGenericGUCs covers the ProcessStartupPacket
// contract (postgres/src/backend/tcop/backend_startup.c ~line 770-790):
// any startup-packet key besides user/database/application_name/replication
// is a generic GUC name=value pair, and the "options" key holds
// PGOPTIONS-style `-c name=value` tokens applied the same way. This is the
// actual wire-level mechanism libpq uses for PGDATESTYLE/PGTZ (folded to
// plain "datestyle"/"timezone" keys by fe-connect.c's EnvironmentOptions
// table) and PGOPTIONS/`options=` connstrings. Before this test's fix,
// goopg's handleStartup only echoed application_name/session_authorization/
// is_superuser and silently dropped every other startup-packet key.
func TestStartupPacketAppliesGenericGUCs(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	writeStartupPacket(t, conn, map[string]string{
		"user":      "postgres",
		"database":  "postgres",
		"datestyle": "Postgres, MDY",
		"timezone":  "America/Los_Angeles",
		"options":   "-c intervalstyle=postgres_verbose",
	})

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil || f.Type != libpq.MsgAuthentication {
		t.Fatalf("read AuthenticationOk: frame=%v err=%v", f, err)
	}

	wantVals := map[string]string{
		"DateStyle":     "Postgres, MDY",
		"TimeZone":      "America/Los_Angeles",
		"IntervalStyle": "postgres_verbose",
	}
	seen := map[string]string{}
	for {
		f, err = r.ReadFrame()
		if err != nil {
			t.Fatalf("read ParameterStatus: %v", err)
		}
		if f.Type != libpq.MsgParameterStatus {
			break
		}
		key, val := splitParamStatus(t, f.Payload)
		if _, ok := wantVals[key]; ok {
			seen[key] = val
		}
	}
	for key, want := range wantVals {
		if got := seen[key]; got != want {
			t.Errorf("ParameterStatus[%q] = %q, want %q", key, got, want)
		}
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
	binary.BigEndian.PutUint32(pkt[4:], libpq.NegotiateSSLCode)
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
	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read after SSL rejection: %v", err)
	}
	if f.Type != libpq.MsgAuthentication {
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

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read ErrorResponse: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
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
	r := libpq.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			break
		}
		if f.Type == libpq.MsgReadyForQuery {
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
	reg := misc.BuildDefaultRegistry()
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

	sess := misc.NewSessionRegistry(reg)
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

// TestBackendFlushAfterOnChangePropagatesToActivityRegistry mirrors
// TestTrackIOTimingOnChangePropagatesToActivityRegistry for M0122-0003's
// writeback follow-up: `SET backend_flush_after` (upstream's GUC is
// PGC_USERSET) must immediately update the calling backend's own
// per-session threshold in the activity registry, which
// storage.Pool.accountBackendWrite consults via BackendFlushAfterOverride,
// without requiring a server restart.
func TestBackendFlushAfterOnChangePropagatesToActivityRegistry(t *testing.T) {
	reg := misc.BuildDefaultRegistry()
	act := activity.NewActivityRegistry(4)
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

	if n, ok := act.BackendFlushAfterOverride(); !ok || n != 0 {
		t.Fatalf("BackendFlushAfterOverride before any SET = (%d, %v), want (0, true)", n, ok)
	}

	sess := misc.NewSessionRegistry(reg)
	if err := sess.Set("backend_flush_after", "16", false); err != nil {
		t.Fatalf("SET backend_flush_after = 16: %v", err)
	}
	if n, ok := act.BackendFlushAfterOverride(); !ok || n != 16 {
		t.Errorf("BackendFlushAfterOverride after SET 16 = (%d, %v), want (16, true)", n, ok)
	}

	if err := sess.Set("backend_flush_after", "0", false); err != nil {
		t.Fatalf("SET backend_flush_after = 0: %v", err)
	}
	if n, ok := act.BackendFlushAfterOverride(); !ok || n != 0 {
		t.Errorf("BackendFlushAfterOverride after SET 0 = (%d, %v), want (0, true)", n, ok)
	}
}

// TestHotStandbyDisabledRefusesConnections pins hot_standby = off: while the
// server is a standby, every connection is refused after its startup packet
// with PG's FATAL 57P03 "the database system is not accepting connections" /
// DETAIL "Hot standby mode is disabled." (canAcceptConnections →
// CAC_NOTHOTSTANDBY, backend_startup.c); once recovery ends (promotion) the
// same server accepts connections, and with hot_standby on a standby accepts
// them. goopg registered hot_standby but never read it.
func TestHotStandbyDisabledRefusesConnections(t *testing.T) {
	var standby atomic.Bool
	start := func(t *testing.T, disabled bool) (string, func()) {
		t.Helper()
		srv := New(Config{
			Address:            "127.0.0.1:0",
			Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			AcceptDeadline:     25 * time.Millisecond,
			HandshakeTimeout:   2 * time.Second,
			IsStandby:          standby.Load,
			HotStandbyDisabled: disabled,
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Run(ctx) }()
		<-srv.Ready()
		return srv.Addr().String(), func() { cancel(); <-done }
	}
	firstFrame := func(t *testing.T, addr string, params map[string]string) libpq.Frame {
		t.Helper()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		writeStartupPacket(t, conn, params)
		f, err := libpq.NewFrameReader(conn).ReadFrame()
		if err != nil {
			t.Fatalf("read first frame: %v", err)
		}
		return f
	}
	user := map[string]string{"user": "alice", "database": "postgres"}
	repl := map[string]string{"user": "alice", "database": "postgres", "replication": "true"}

	addr, stop := start(t, true)
	defer stop()
	standby.Store(true)
	for _, params := range []map[string]string{user, repl} {
		f := firstFrame(t, addr, params)
		if f.Type != libpq.MsgErrorResponse {
			t.Fatalf("standby with hot_standby = off answered %c, want an ErrorResponse (replication=%q)", f.Type, params["replication"])
		}
		got := parseErrorFields(t, f.Payload)
		if got['S'] != "FATAL" || got['C'] != "57P03" || got['M'] != "the database system is not accepting connections" || got['D'] != "Hot standby mode is disabled." {
			t.Errorf("refusal fields = %v, want PG's FATAL 57P03 not-accepting-connections / hot-standby-disabled", got)
		}
	}
	standby.Store(false) // promoted
	if f := firstFrame(t, addr, user); f.Type != libpq.MsgAuthentication {
		t.Errorf("after promotion the server answered %c, want AuthenticationOk", f.Type)
	}

	addr2, stop2 := start(t, false)
	defer stop2()
	standby.Store(true)
	if f := firstFrame(t, addr2, user); f.Type != libpq.MsgAuthentication {
		t.Errorf("standby with hot_standby = on answered %c, want AuthenticationOk", f.Type)
	}
}

// TestHotStandbyNotConsistentRefusesConnections pins the hot_standby = on
// gate: until replay reaches a consistent recovery state the postmaster is
// in PM_RECOVERY, and canAcceptConnections refuses every connection —
// normal or replication — with FATAL 57P03 "the database system is not yet
// accepting connections" / "Consistent recovery state has not been yet
// reached."; once consistent (PM_HOT_STANDBY) they are accepted. goopg's
// standby accepted reads before replay was consistent.
func TestHotStandbyNotConsistentRefusesConnections(t *testing.T) {
	var standby, consistent atomic.Bool
	standby.Store(true)
	srv := New(Config{
		Address:              "127.0.0.1:0",
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:       25 * time.Millisecond,
		HandshakeTimeout:     2 * time.Second,
		IsStandby:            standby.Load,
		HotStandbyConsistent: consistent.Load,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	defer func() { cancel(); <-done }()
	addr := srv.Addr().String()
	firstFrame := func(params map[string]string) libpq.Frame {
		t.Helper()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		writeStartupPacket(t, conn, params)
		f, err := libpq.NewFrameReader(conn).ReadFrame()
		if err != nil {
			t.Fatalf("read first frame: %v", err)
		}
		return f
	}
	user := map[string]string{"user": "alice", "database": "postgres"}
	repl := map[string]string{"user": "alice", "database": "postgres", "replication": "true"}
	for _, params := range []map[string]string{user, repl} {
		f := firstFrame(params)
		if f.Type != libpq.MsgErrorResponse {
			t.Fatalf("inconsistent standby answered %c, want an ErrorResponse (replication=%q)", f.Type, params["replication"])
		}
		got := parseErrorFields(t, f.Payload)
		if got['S'] != "FATAL" || got['C'] != "57P03" || got['M'] != "the database system is not yet accepting connections" ||
			got['D'] != "Consistent recovery state has not been yet reached." {
			t.Errorf("refusal fields = %v, want PG's FATAL 57P03 not-yet-accepting / consistency detail", got)
		}
	}
	consistent.Store(true)
	if f := firstFrame(user); f.Type != libpq.MsgAuthentication {
		t.Errorf("consistent standby answered %c, want AuthenticationOk", f.Type)
	}
	consistent.Store(false)
	standby.Store(false) // promoted: no longer in recovery
	if f := firstFrame(user); f.Type != libpq.MsgAuthentication {
		t.Errorf("promoted server answered %c, want AuthenticationOk", f.Type)
	}
}

// TestStopImmediateSendsQuickdieNotice pins quickdie's PMQUIT_FOR_STOP
// message: on an immediate stop a session gets a WARNING notice — not an
// ErrorResponse — with SQLSTATE 57P01 "terminating connection due to
// immediate shutdown command", and the connection closes. goopg sent the
// fast-shutdown FATAL "terminating connection due to administrator
// command".
// TestFastStopSendsAdminShutdownFatal: a fast stop (the run context's
// cancel, pmdie SIGTERM) wakes an idle session, which reports die()'s
// FATAL 57P01 "terminating connection due to administrator command"
// before closing, as PG's idle backend does.
func TestFastStopSendsAdminShutdownFatal(t *testing.T) {
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice", "database": "postgres"})
	r := libpq.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
		if f.Type == libpq.MsgReadyForQuery {
			break
		}
	}
	cancel()
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read after the fast stop: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("got message %c, want an ErrorResponse", f.Type)
	}
	got := parseErrorFields(t, f.Payload)
	if got['S'] != "FATAL" || got['C'] != "57P01" || got['M'] != "terminating connection due to administrator command" {
		t.Errorf("error fields = %v, want die()'s FATAL 57P01", got)
	}
	if _, err := r.ReadFrame(); err == nil {
		t.Error("connection still open after the FATAL")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the fast stop")
	}
}

// TestShutdownReplacesStatementCancel: a statement a fast stop cancels is
// reported with die()'s FATAL alone — ProcessInterrupts checks
// ProcDiePending before QueryCancelPending, so PG never sends the 57014
// "canceling statement due to user request" first. Both error writers
// (simple query and extended protocol) take the same path; an error that
// is not the cancellation, or one raised while the server runs, is written
// as before.
func TestShutdownReplacesStatementCancel(t *testing.T) {
	writeBoth := func(s *Server, code errcodes.Code) (simpleErr, extErr error, frames []libpq.Frame) {
		var buf bytes.Buffer
		w := libpq.NewFrameWriter(&buf)
		simpleErr = s.writeQueryError(w, code, "canceling statement due to user request")
		extErr = s.writeExtendedMessageError(w, &extendedMessageError{Code: code, Message: "canceling statement due to user request"})
		_ = w.Flush()
		r := libpq.NewFrameReader(&buf)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				break
			}
			// The reader reuses its payload buffer across frames.
			f.Payload = append([]byte(nil), f.Payload...)
			frames = append(frames, f)
		}
		return simpleErr, extErr, frames
	}
	stopping := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopping.runCtx.Store(&ctx)

	simpleErr, extErr, frames := writeBoth(stopping, errcodes.QueryCanceled)
	if !errors.Is(simpleErr, errShutdownSent) || !errors.Is(extErr, errShutdownSent) {
		t.Fatalf("errors = %v, %v; want errShutdownSent from both writers", simpleErr, extErr)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want one FATAL per writer", len(frames))
	}
	for _, f := range frames {
		if f.Type != libpq.MsgErrorResponse {
			t.Fatalf("got message %c, want an ErrorResponse", f.Type)
		}
		got := parseErrorFields(t, f.Payload)
		if got['S'] != "FATAL" || got['C'] != "57P01" || got['M'] != "terminating connection due to administrator command" {
			t.Errorf("error fields = %v, want die()'s FATAL 57P01", got)
		}
	}

	// Any other error during the stop is the statement's own.
	simpleErr, extErr, frames = writeBoth(stopping, errcodes.DivisionByZero)
	if !errors.Is(simpleErr, errQueryErrorSent) || extErr != nil || len(frames) != 3 {
		t.Fatalf("non-cancel error during a stop: errors %v, %v, %d frames; want the plain ERROR path", simpleErr, extErr, len(frames))
	}

	// A cancellation while the server runs (pg_cancel_backend) is the
	// ordinary 57014 ERROR.
	running := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	live := context.Background()
	running.runCtx.Store(&live)
	simpleErr, _, frames = writeBoth(running, errcodes.QueryCanceled)
	if !errors.Is(simpleErr, errQueryErrorSent) || len(frames) != 3 {
		t.Fatalf("cancel while running: error %v, %d frames; want ERROR + ReadyForQuery + ERROR", simpleErr, len(frames))
	}
	if got := parseErrorFields(t, frames[0].Payload); got['S'] != "ERROR" || got['C'] != "57014" {
		t.Errorf("cancel while running fields = %v, want ERROR 57014", got)
	}
}

func TestStopImmediateSendsQuickdieNotice(t *testing.T) {
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "alice", "database": "postgres"})
	r := libpq.NewFrameReader(conn)
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
		if f.Type == libpq.MsgReadyForQuery {
			break
		}
	}
	srv.StopImmediate("test: immediate stop")
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read after the immediate stop: %v", err)
	}
	if f.Type != libpq.MsgNoticeResponse {
		t.Fatalf("got message %c, want a NoticeResponse", f.Type)
	}
	got := parseErrorFields(t, f.Payload)
	if got['S'] != "WARNING" || got['C'] != "57P01" || got['M'] != "terminating connection due to immediate shutdown command" {
		t.Errorf("notice fields = %v, want quickdie's WARNING 57P01", got)
	}
	if _, err := r.ReadFrame(); err == nil {
		t.Error("connection still open after the quickdie notice")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after StopImmediate")
	}
}
