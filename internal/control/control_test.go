package control

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestPIDFileRoundTrip pins the marshal/parse contract — a written
// pidfile reads back identical to the original.
func TestPIDFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := PIDFile{
		PID:        12345,
		DataDir:    dir,
		StartedAt:  time.UnixMilli(1700000000000),
		ListenAddr: "127.0.0.1:5432",
		SocketPath: filepath.Join(dir, SocketName),
	}
	if err := WritePIDFile(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ParsePIDFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != want.PID || got.DataDir != want.DataDir ||
		got.ListenAddr != want.ListenAddr || got.SocketPath != want.SocketPath ||
		!got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("round trip lost data: got %+v want %+v", got, want)
	}
}

// TestParseMissingPIDFile surfaces os.ErrNotExist so the CLI can
// distinguish "no running server" from "pidfile is corrupt".
func TestParseMissingPIDFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ParsePIDFile(dir)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want ErrNotExist", err)
	}
}

// TestListenerStopAndPing drives the full operator flow: bind a
// listener, send PING (verifies routing), send STOP (verifies the
// OnStop callback fires), and ensure the socket file is gone after
// Close.
func TestListenerStopAndPing(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	ln, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	var stopped int32
	ln.OnStop = func() error {
		atomic.StoreInt32(&stopped, 1)
		return nil
	}
	served := make(chan error, 1)
	go func() { served <- ln.Serve() }()

	reply, err := Send(path, "PING", time.Second)
	if err != nil {
		t.Fatalf("PING: %v", err)
	}
	if reply != "OK" {
		t.Errorf("PING reply=%q want OK", reply)
	}

	reply, err = Send(path, "STOP", time.Second)
	if err != nil {
		t.Fatalf("STOP: %v", err)
	}
	if reply != "OK" {
		t.Errorf("STOP reply=%q want OK", reply)
	}
	// OnStop runs in the listener goroutine after replying — give
	// it a brief window to land.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&stopped) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&stopped) != 1 {
		t.Error("OnStop never fired")
	}

	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	<-served
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket file should be removed after Close, got %v", err)
	}
}

// TestPromoteDispatch confirms the PROMOTE command routes to the
// OnPromote callback and the reply only flows after the handler
// returns. We seed the handler with a 50ms sleep — if the listener
// replied before the callback finished, the test would race and
// occasionally observe stopped == 0 below.
func TestPromoteDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	ln, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var promoted int32
	ln.OnPromote = func() error {
		time.Sleep(50 * time.Millisecond)
		atomic.StoreInt32(&promoted, 1)
		return nil
	}
	go ln.Serve()
	reply, err := Send(path, "PROMOTE", 5*time.Second)
	if err != nil {
		t.Fatalf("PROMOTE: %v", err)
	}
	if reply != "OK" {
		t.Errorf("PROMOTE reply=%q want OK", reply)
	}
	if atomic.LoadInt32(&promoted) != 1 {
		t.Error("OnPromote did not run before reply")
	}
}

// TestPromoteUnconfigured pins the "I'm a primary, not a standby"
// guard: a server with no OnPromote set must reject PROMOTE rather
// than no-op silently.
func TestPromoteUnconfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	ln, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go ln.Serve()
	reply, err := Send(path, "PROMOTE", time.Second)
	if err != nil {
		t.Fatalf("PROMOTE: %v", err)
	}
	if reply != "ERR promote not configured" {
		t.Errorf("PROMOTE reply=%q want ERR promote not configured", reply)
	}
}

// TestPromoteHandlerError surfaces handler-side failures verbatim so
// the operator sees what went wrong (drain timeout, signal-removal
// failure, etc.) without having to scrape server logs.
func TestPromoteHandlerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	ln, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ln.OnPromote = func() error { return errors.New("drain timed out") }
	go ln.Serve()
	reply, err := Send(path, "PROMOTE", time.Second)
	if err != nil {
		t.Fatalf("PROMOTE: %v", err)
	}
	if reply != "ERR drain timed out" {
		t.Errorf("PROMOTE reply=%q want ERR drain timed out", reply)
	}
}

// TestProcessAliveSelf sanity-checks ProcessAlive: this process is
// always alive, pid 0 is not.
func TestProcessAliveSelf(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("our own pid should be alive")
	}
	if ProcessAlive(0) {
		t.Error("pid 0 should not be alive")
	}
}
