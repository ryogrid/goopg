package libpq

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

// testFrameLimit is the limit used in oversize-frame unit tests. Using a
// tiny value avoids allocating or sending multi-MiB buffers while still
// exercising the ErrFrameTooLarge path through the same code as production.
const testFrameLimit = 256

// TestFrameReaderRejectsOversizePayload guards the ErrFrameTooLarge sentinel
// and the drain-before-return behaviour on a reader with a small custom limit.
func TestFrameReaderRejectsOversizePayload(t *testing.T) {
	// Build a header claiming payload = testFrameLimit+1 bytes, followed by
	// the actual payload bytes so the drain path succeeds.
	payloadLen := testFrameLimit + 1
	total := uint32(payloadLen + 4)
	hdr := []byte{
		'Q',
		byte(total >> 24), byte(total >> 16), byte(total >> 8), byte(total),
	}
	var buf bytes.Buffer
	buf.Write(hdr)
	buf.Write(make([]byte, payloadLen))
	r := NewFrameReaderWithLimit(&buf, testFrameLimit)
	_, err := r.ReadFrame()
	if err == nil {
		t.Fatal("expected oversize error, got nil")
	}
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

// TestFrameReaderResynchronisesAfterOversizePayload verifies that after
// rejecting an oversized message, the reader is still synchronised and can
// read the next (normal-sized) message successfully. This is the property
// that allows the server to send an error response and continue the session
// rather than dropping the connection.
func TestFrameReaderResynchronisesAfterOversizePayload(t *testing.T) {
	var buf bytes.Buffer

	// First: a MsgQuery exceeding testFrameLimit.
	payloadLen := testFrameLimit + 1
	total := uint32(payloadLen + 4)
	hdr := []byte{
		'Q',
		byte(total >> 24), byte(total >> 16), byte(total >> 8), byte(total),
	}
	buf.Write(hdr)
	buf.Write(make([]byte, payloadLen))

	// Second: a tiny valid MsgSync (payload length 4 = zero bytes after header).
	buf.Write([]byte{'S', 0, 0, 0, 4})

	r := NewFrameReaderWithLimit(&buf, testFrameLimit)

	// First read: expect ErrFrameTooLarge.
	_, err := r.ReadFrame()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("first read: expected ErrFrameTooLarge, got %v", err)
	}

	// Second read: stream should be re-synchronised; expect MsgSync.
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("second read: unexpected error %v", err)
	}
	if f.Type != MsgSync {
		t.Fatalf("second read: expected MsgSync ('S'), got %q", f.Type)
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
