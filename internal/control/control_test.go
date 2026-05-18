package control

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
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


// TestUpdateControlFileRoundTrip verifies that UpdateControlFile preserves
// immutable fields, applies mutations, and produces a correct CRC32C.
func TestUpdateControlFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Build a minimal but valid pg_control buffer: 8192 bytes, CRC over [0:292].
	buf := make([]byte, pgControlFileSize)
	le := binary.LittleEndian
	// Write a sentinel in the immutable system_identifier field (offset 0).
	le.PutUint64(buf[0:], 0xDEADBEEFCAFEBABE)
	// Write pg_control_version (offset 8) so it is non-zero.
	le.PutUint32(buf[8:], 1800)
	// Set initial state to DB_SHUTDOWNED.
	le.PutUint32(buf[16:], DBStateShutdowned)
	// Compute initial CRC.
	crc := crc32.Checksum(buf[:pgControlCRCOffset], pgCRCTable)
	le.PutUint32(buf[pgControlCRCOffset:], crc)
	path := filepath.Join(dir, pgControlFilePath)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	// Mutate: flip state to DB_IN_PRODUCTION and set a checkpoint LSN.
	if err := UpdateControlFile(dir, func(cd *ControlFileData) {
		cd.State = DBStateInProduction
		cd.Time = 1234567890
		cd.CheckPoint = 0x01000028
		cd.CheckPointCopyRedo = 0x01000028
		cd.CheckPointCopyThisTLI = 1
		cd.CheckPointCopyPrevTLI = 1
		cd.CheckPointCopyFullPageWrites = true
	}); err != nil {
		t.Fatalf("UpdateControlFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != pgControlFileSize {
		t.Fatalf("pg_control size: got %d want %d", len(got), pgControlFileSize)
	}

	// Immutable field must be preserved.
	if sysID := le.Uint64(got[0:]); sysID != 0xDEADBEEFCAFEBABE {
		t.Errorf("system_identifier changed: got %#x", sysID)
	}

	// Mutations must be reflected.
	if state := le.Uint32(got[16:]); state != DBStateInProduction {
		t.Errorf("state: got %d want %d (DBStateInProduction)", state, DBStateInProduction)
	}
	if ts := int64(le.Uint64(got[24:])); ts != 1234567890 {
		t.Errorf("time: got %d want 1234567890", ts)
	}
	if cp := le.Uint64(got[32:]); cp != 0x01000028 {
		t.Errorf("checkPoint: got %#x want 0x01000028", cp)
	}
	if tli := le.Uint32(got[48:]); tli != 1 {
		t.Errorf("checkPointCopy.ThisTimeLineID: got %d want 1", tli)
	}
	if fpw := got[56]; fpw != 1 {
		t.Errorf("checkPointCopy.fullPageWrites: got %d want 1", fpw)
	}

	// CRC must be valid over the new buffer.
	wantCRC := crc32.Checksum(got[:pgControlCRCOffset], pgCRCTable)
	gotCRC := le.Uint32(got[pgControlCRCOffset:])
	if gotCRC != wantCRC {
		t.Errorf("CRC mismatch: got %#x want %#x", gotCRC, wantCRC)
	}
}

// TestUpdateControlFileMissingDir verifies that UpdateControlFile returns
// an error (wrapping os.ErrNotExist) when the data directory does not
// contain a pg_control file.
func TestUpdateControlFileMissingDir(t *testing.T) {
	err := UpdateControlFile(t.TempDir(), func(cd *ControlFileData) {})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want error wrapping os.ErrNotExist", err)
	}
}
