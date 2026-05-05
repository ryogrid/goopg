package wal

import (
	"testing"
)

// TestEncodeDecodeCreateDatabaseRoundTrip pins the M0054-0001
// CREATE DATABASE WAL record format. Encode → Decode must return the
// original name byte-for-byte; an Encode of a name longer than 64 KiB
// gets defensively truncated rather than panicking.
func TestEncodeDecodeCreateDatabaseRoundTrip(t *testing.T) {
	cases := []string{
		"",       // edge case — caller is expected to reject this earlier
		"tpch",   // HammerDB shape
		"My DB",  // identifier with whitespace
		"日本語DB", // multi-byte UTF-8
	}
	for _, name := range cases {
		raw := EncodeCreateDatabase(name)
		if raw[0] != RecordKindCreateDatabase {
			t.Errorf("name %q: kind byte = %d, want %d", name, raw[0], RecordKindCreateDatabase)
			continue
		}
		got, err := DecodeCreateDatabase(raw)
		if err != nil {
			t.Errorf("name %q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("name %q: decoded %q", name, got)
		}
	}
}

// TestEncodeDecodeDropDatabaseRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropDatabaseRoundTrip(t *testing.T) {
	for _, name := range []string{"tpch", "postgres", "x"} {
		raw := EncodeDropDatabase(name)
		if raw[0] != RecordKindDropDatabase {
			t.Errorf("name %q: kind byte = %d, want %d", name, raw[0], RecordKindDropDatabase)
			continue
		}
		got, err := DecodeDropDatabase(raw)
		if err != nil {
			t.Errorf("name %q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("name %q: decoded %q", name, got)
		}
	}
}

// TestDecodeCreateDatabaseRejectsWrongKind confirms the decoder
// surfaces a clear error when handed a record of a different kind
// (e.g. a SmgrCreate record). Sanity guard against silently
// misinterpreting bytes.
func TestDecodeCreateDatabaseRejectsWrongKind(t *testing.T) {
	bogus := []byte{RecordKindSmgrCreate, 0, 0}
	if _, err := DecodeCreateDatabase(bogus); err == nil {
		t.Error("expected error decoding non-create-database payload")
	}
}

// TestDecodeCreateDatabaseRejectsTruncatedPayload guards against
// silently returning empty names when the on-disk record is corrupt.
func TestDecodeCreateDatabaseRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10 but only 4 name bytes follow.
	truncated := []byte{RecordKindCreateDatabase, 10, 0, 't', 'p', 'c', 'h'}
	if _, err := DecodeCreateDatabase(truncated); err == nil {
		t.Error("expected error decoding truncated create-database payload")
	}
}
