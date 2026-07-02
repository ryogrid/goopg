package wal

import (
	"testing"
)

// TestEncodeDecodeCreateCastRoundTrip pins the DU-002 restart-persistence
// (M0119-0004 follow-up) CREATE CAST WAL record format. Encode → Decode must
// return the original source/target/context/method/OIDs; an Encode of a name
// longer than 64 KiB gets defensively truncated rather than panicking.
func TestEncodeDecodeCreateCastRoundTrip(t *testing.T) {
	cases := []struct {
		source, target, context, method string
		oid, funcOID                    uint32
	}{
		{"integer", "text", "e", "f", 16384, 2406}, // WITH FUNCTION explicit cast
		{"integer", "bigint", "i", "b", 16385, 0},  // WITHOUT FUNCTION implicit
		{"box", "circle", "a", "i", 16386, 0},      // WITH INOUT assignment
		{"日本語型", "text", "e", "f", 4294967295, 1}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateCast(c.source, c.target, c.context, c.method, c.oid, c.funcOID)
		if raw[0] != RecordKindCreateCast {
			t.Errorf("%q->%q: kind byte = %d, want %d", c.source, c.target, raw[0], RecordKindCreateCast)
			continue
		}
		gotSource, gotTarget, gotContext, gotMethod, gotOID, gotFuncOID, err := DecodeCreateCast(raw)
		if err != nil {
			t.Errorf("%q->%q: decode err: %v", c.source, c.target, err)
			continue
		}
		if gotSource != c.source || gotTarget != c.target || gotContext != c.context || gotMethod != c.method || gotOID != c.oid || gotFuncOID != c.funcOID {
			t.Errorf("%q->%q: decoded (%q, %q, %q, %q, %d, %d)", c.source, c.target, gotSource, gotTarget, gotContext, gotMethod, gotOID, gotFuncOID)
		}
	}
}

// TestEncodeDecodeDropCastRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropCastRoundTrip(t *testing.T) {
	cases := []struct{ source, target string }{
		{"integer", "text"},
		{"box", "circle"},
	}
	for _, c := range cases {
		raw := EncodeDropCast(c.source, c.target)
		if raw[0] != RecordKindDropCast {
			t.Errorf("%q->%q: kind byte = %d, want %d", c.source, c.target, raw[0], RecordKindDropCast)
			continue
		}
		gotSource, gotTarget, err := DecodeDropCast(raw)
		if err != nil {
			t.Errorf("%q->%q: decode err: %v", c.source, c.target, err)
			continue
		}
		if gotSource != c.source || gotTarget != c.target {
			t.Errorf("%q->%q: decoded (%q, %q)", c.source, c.target, gotSource, gotTarget)
		}
	}
}

// TestDecodeCreateCastRejectsWrongKind confirms the decoder surfaces a clear
// error when handed a record of a different kind.
func TestDecodeCreateCastRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropCast("integer", "text")
	if _, _, _, _, _, _, err := DecodeCreateCast(bogus); err == nil {
		t.Error("expected error decoding non-create-cast payload")
	}
}

// TestDecodeCreateCastRejectsTruncatedPayload guards against silently
// returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeCreateCastRejectsTruncatedPayload(t *testing.T) {
	// kind + oid(4) + funcOID(4) + context(1) + method(1) + sourceLen=10, but no bytes follow.
	truncated := []byte{RecordKindCreateCast, 0, 0x40, 0, 0, 0, 0, 0, 0, 'e', 'f', 10, 0}
	if _, _, _, _, _, _, err := DecodeCreateCast(truncated); err == nil {
		t.Error("expected error decoding truncated create-cast payload")
	}
}

// TestDecodeDropCastRejectsTruncatedPayload mirrors the create case for DROP
// CAST.
func TestDecodeDropCastRejectsTruncatedPayload(t *testing.T) {
	// kind + sourceLen=10, but no bytes follow.
	truncated := []byte{RecordKindDropCast, 10, 0}
	if _, _, err := DecodeDropCast(truncated); err == nil {
		t.Error("expected error decoding truncated drop-cast payload")
	}
}
