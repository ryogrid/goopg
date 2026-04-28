package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestFrameRoundTrip writes a few backend messages and reads them back as
// raw frames. This pins the framing contract relied on by every subsequent
// milestone; if it breaks, the wire-level guarantee in 0002-wire-protocol.md
// is broken.
func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	if err := w.WriteAuthenticationOk(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteParameterStatus("server_version", "18.3"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteBackendKeyData(7, 0xDEADBEEF); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteReadyForQuery(TxStatusIdle); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	r := NewFrameReader(&buf)
	want := []struct {
		typ  byte
		size int
	}{
		{MsgAuthentication, 4},   // int32(0)
		{MsgParameterStatus, 20}, // "server_version\0" (15) + "18.3\0" (5)
		{MsgBackendKeyData, 8},   // pid + secret
		{MsgReadyForQuery, 1},    // 'I'
	}
	for i, exp := range want {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Type != exp.typ {
			t.Errorf("frame %d: type=%c want %c", i, f.Type, exp.typ)
		}
		if len(f.Payload) != exp.size {
			t.Errorf("frame %d: payload len=%d want %d", i, len(f.Payload), exp.size)
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF after last frame, got %v", err)
	}
}

func TestParseStartupParameters(t *testing.T) {
	// Build a NUL-terminated key/value sequence ending with empty key.
	body := []byte("user\x00alice\x00database\x00postgres\x00application_name\x00psql\x00\x00")
	got, err := ParseStartupParameters(body)
	if err != nil {
		t.Fatalf("ParseStartupParameters: %v", err)
	}
	want := map[string]string{
		"user":             "alice",
		"database":         "postgres",
		"application_name": "psql",
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("params[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseStartupParameters_MissingTerminator(t *testing.T) {
	// No final empty key — should error rather than wander off the end.
	body := []byte("user\x00alice\x00")
	if _, err := ParseStartupParameters(body); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestFrameReaderRejectsOversizePayload guards against memory-exhaustion DoS
// from a malicious client claiming a multi-GB message length.
func TestFrameReaderRejectsOversizePayload(t *testing.T) {
	// Build a header claiming length = MaxRegularMessageLength + 5 (i.e.
	// payload = MaxRegularMessageLength + 1).
	hdr := []byte{'Q', 0, 0, 0, 0}
	tooBig := uint32(MaxRegularMessageLength + 5)
	hdr[1] = byte(tooBig >> 24)
	hdr[2] = byte(tooBig >> 16)
	hdr[3] = byte(tooBig >> 8)
	hdr[4] = byte(tooBig)
	r := NewFrameReader(bytes.NewReader(hdr))
	if _, err := r.ReadFrame(); err == nil {
		t.Fatal("expected oversize error, got nil")
	}
}

func TestReadStartupPacketEOF(t *testing.T) {
	// Empty input: caller must distinguish "client closed without sending
	// anything" (returned as io.EOF) from "partial packet" (a different
	// error). This contract underpins the v0 server's connection-lifecycle
	// handling: silent close on probe, error log on partial.
	r := NewFrameReader(bytes.NewReader(nil))
	_, _, err := r.ReadStartupPacket()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}
