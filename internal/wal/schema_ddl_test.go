package wal

import (
	"testing"
)

// TestEncodeDecodeCreateSchemaRoundTrip pins the M0110-0003 CREATE SCHEMA WAL
// record format. Encode → Decode must return the original name and OID; an
// Encode of a name longer than 64 KiB gets defensively truncated rather than
// panicking.
func TestEncodeDecodeCreateSchemaRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		oid  uint32
	}{
		{"", 16384},             // edge case — caller rejects empty earlier
		{"s1", 16384},           // common shape (pg_amcheck --schema s1)
		{"My Schema", 1},        // identifier with whitespace
		{"日本語スキーマ", 4294967295}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateSchema(c.name, c.oid)
		if raw[0] != RecordKindCreateSchema {
			t.Errorf("name %q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateSchema)
			continue
		}
		gotName, gotOID, err := DecodeCreateSchema(raw)
		if err != nil {
			t.Errorf("name %q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOID != c.oid {
			t.Errorf("name %q oid %d: decoded (%q, %d)", c.name, c.oid, gotName, gotOID)
		}
	}
}

// TestEncodeDecodeDropSchemaRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropSchemaRoundTrip(t *testing.T) {
	for _, name := range []string{"s1", "public_clone", "x"} {
		raw := EncodeDropSchema(name)
		if raw[0] != RecordKindDropSchema {
			t.Errorf("name %q: kind byte = %d, want %d", name, raw[0], RecordKindDropSchema)
			continue
		}
		got, err := DecodeDropSchema(raw)
		if err != nil {
			t.Errorf("name %q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("name %q: decoded %q", name, got)
		}
	}
}

// TestDecodeCreateSchemaRejectsWrongKind confirms the decoder surfaces a clear
// error when handed a record of a different kind.
func TestDecodeCreateSchemaRejectsWrongKind(t *testing.T) {
	bogus := []byte{RecordKindDropSchema, 0, 0, 0, 0, 0, 0}
	if _, _, err := DecodeCreateSchema(bogus); err == nil {
		t.Error("expected error decoding non-create-schema payload")
	}
}

// TestDecodeCreateSchemaRejectsTruncatedPayload guards against silently
// returning empty names when the on-disk record is corrupt.
func TestDecodeCreateSchemaRejectsTruncatedPayload(t *testing.T) {
	// kind + oid(4) + nameLen=10 but only 2 name bytes follow.
	truncated := []byte{RecordKindCreateSchema, 0, 0x40, 0, 0, 10, 0, 's', '1'}
	if _, _, err := DecodeCreateSchema(truncated); err == nil {
		t.Error("expected error decoding truncated create-schema payload")
	}
}
