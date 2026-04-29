package protocol

import (
	"bytes"
	"testing"
	"time"
)

// TestEncodeWALDataRoundTrip pins the on-wire layout for a 'w' WAL-data
// CopyData payload. EndLSN, StartLSN, and the wal byte slice all
// round-trip through Decode. SendTime is rounded to microsecond
// precision (the upstream TimestampTz resolution) before comparison.
func TestEncodeWALDataRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 30, 45, 123456000, time.UTC)
	walBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	encoded := EncodeWALData(0x100, 0x100+uint64(len(walBytes)), now, walBytes)
	if encoded[0] != ReplMsgWALData {
		t.Fatalf("first byte = %q, want 'w'", encoded[0])
	}
	if got, want := len(encoded), 1+24+len(walBytes); got != want {
		t.Fatalf("encoded length = %d, want %d", got, want)
	}

	parsed, kind, err := DecodeReplicationMessage(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if kind != ReplMsgWALData {
		t.Fatalf("kind = %q, want 'w'", kind)
	}
	m, ok := parsed.(*WALDataMessage)
	if !ok {
		t.Fatalf("parsed type = %T, want *WALDataMessage", parsed)
	}
	if m.StartLSN != 0x100 {
		t.Errorf("StartLSN = %x, want 0x100", m.StartLSN)
	}
	if m.EndLSN != 0x100+uint64(len(walBytes)) {
		t.Errorf("EndLSN = %x, want %x", m.EndLSN, 0x100+uint64(len(walBytes)))
	}
	if !bytes.Equal(m.WALBytes, walBytes) {
		t.Errorf("WALBytes = %v, want %v", m.WALBytes, walBytes)
	}
	if !m.SendTime.Equal(now) {
		t.Errorf("SendTime = %v, want %v", m.SendTime, now)
	}
}

// TestEncodeKeepaliveRoundTrip pins the 'k' keepalive layout: 18 bytes
// total, replyRequested round-trips as a single byte boolean.
func TestEncodeKeepaliveRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	for _, reply := range []bool{false, true} {
		encoded := EncodeKeepalive(0xDEADBEEF, now, reply)
		if len(encoded) != 1+8+8+1 {
			t.Fatalf("keepalive(%v) length = %d, want 18", reply, len(encoded))
		}
		parsed, kind, err := DecodeReplicationMessage(encoded)
		if err != nil {
			t.Fatalf("decode(%v): %v", reply, err)
		}
		if kind != ReplMsgKeepalive {
			t.Fatalf("kind = %q, want 'k'", kind)
		}
		m := parsed.(*KeepaliveMessage)
		if m.WALEnd != 0xDEADBEEF {
			t.Errorf("WALEnd = %x, want 0xDEADBEEF", m.WALEnd)
		}
		if m.ReplyRequested != reply {
			t.Errorf("ReplyRequested = %v, want %v", m.ReplyRequested, reply)
		}
		if !m.SendTime.Equal(now) {
			t.Errorf("SendTime = %v, want %v", m.SendTime, now)
		}
	}
}

// TestEncodeStandbyStatusRoundTrip pins the 'r' standby-status layout
// (34 bytes) and validates that all three LSNs round-trip independently.
func TestEncodeStandbyStatusRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 29, 6, 30, 0, 0, time.UTC)
	encoded := EncodeStandbyStatusUpdate(0x100, 0x080, 0x040, now, true)
	if len(encoded) != 1+8+8+8+8+1 {
		t.Fatalf("status length = %d, want 34", len(encoded))
	}
	parsed, kind, err := DecodeReplicationMessage(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if kind != ReplMsgStandbyStatus {
		t.Fatalf("kind = %q, want 'r'", kind)
	}
	m := parsed.(*StandbyStatusUpdate)
	if m.WriteLSN != 0x100 || m.FlushLSN != 0x080 || m.ApplyLSN != 0x040 {
		t.Errorf("LSNs = (%x, %x, %x), want (0x100, 0x80, 0x40)",
			m.WriteLSN, m.FlushLSN, m.ApplyLSN)
	}
	if !m.ReplyRequested {
		t.Errorf("ReplyRequested = false, want true")
	}
}

// TestPgTimestampMicros pins the upstream TimestampTz epoch (2000-01-01
// UTC) so encoders / decoders agree with PostgreSQL's walsender output.
func TestPgTimestampMicros(t *testing.T) {
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := PgTimestampMicros(epoch); got != 0 {
		t.Errorf("PgTimestampMicros(epoch) = %d, want 0", got)
	}
	oneSec := epoch.Add(time.Second)
	if got := PgTimestampMicros(oneSec); got != 1_000_000 {
		t.Errorf("PgTimestampMicros(epoch+1s) = %d, want 1_000_000", got)
	}
	round := PgTimestampToTime(123_456_789)
	if !round.Equal(epoch.Add(123_456_789 * time.Microsecond)) {
		t.Errorf("PgTimestampToTime round-trip mismatch: %v", round)
	}
}

// TestDecodeReplicationMessageUnknownByte rejects unrecognised inner
// types with a clear error rather than silently dropping the frame.
func TestDecodeReplicationMessageUnknownByte(t *testing.T) {
	if _, _, err := DecodeReplicationMessage([]byte{'x'}); err == nil {
		t.Fatalf("expected error for unknown byte, got nil")
	}
	if _, _, err := DecodeReplicationMessage(nil); err == nil {
		t.Fatalf("expected error for empty payload, got nil")
	}
}
