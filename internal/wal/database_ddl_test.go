package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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
		raw := EncodeCreateDatabase(name, 16401, 16500)
		if raw[0] != RecordKindCreateDatabase {
			t.Errorf("name %q: kind byte = %d, want %d", name, raw[0], RecordKindCreateDatabase)
			continue
		}
		got, owner, oid, err := DecodeCreateDatabase(raw)
		if err != nil {
			t.Errorf("name %q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("name %q: decoded %q", name, got)
		}
		if owner != 16401 {
			t.Errorf("name %q: decoded owner = %d, want 16401", name, owner)
		}
		if oid != 16500 {
			t.Errorf("name %q: decoded oid = %d, want 16500", name, oid)
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
	if _, _, _, err := DecodeCreateDatabase(bogus); err == nil {
		t.Error("expected error decoding non-create-database payload")
	}
}

// TestDecodeCreateDatabaseRejectsTruncatedPayload guards against
// silently returning empty names when the on-disk record is corrupt.
func TestDecodeCreateDatabaseRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10 but only 4 name bytes follow.
	truncated := []byte{RecordKindCreateDatabase, 10, 0, 't', 'p', 'c', 'h'}
	if _, _, _, err := DecodeCreateDatabase(truncated); err == nil {
		t.Error("expected error decoding truncated create-database payload")
	}
}

// TestDecodeCreateDatabaseDefaultsOwnerForPreM01220007Payload confirms a WAL
// record written before the M0122-0007 owner suffix was added (bare
// kind|nameLen|name, no trailing 4 owner bytes) still decodes — with owner
// defaulting to catalog.BootstrapSuperuserOID — so replaying an
// already-on-disk WAL stream from before this change doesn't error.
func TestDecodeCreateDatabaseDefaultsOwnerForPreM01220007Payload(t *testing.T) {
	legacy := []byte{RecordKindCreateDatabase, 4, 0, 't', 'p', 'c', 'h'}
	name, owner, oid, err := DecodeCreateDatabase(legacy)
	if err != nil {
		t.Fatalf("decode legacy payload: %v", err)
	}
	if name != "tpch" {
		t.Errorf("name = %q, want tpch", name)
	}
	if owner != catalog.BootstrapSuperuserOID {
		t.Errorf("owner = %d, want BootstrapSuperuserOID (%d)", owner, catalog.BootstrapSuperuserOID)
	}
	if oid != 0 {
		t.Errorf("oid = %d, want 0 (no override — pre-slice-1 payload)", oid)
	}
}

// TestDecodeCreateDatabaseDefaultsOidForPreSlice1Payload confirms a WAL
// record written after the M0122-0007 owner suffix landed but before the
// physical-storage-isolation slice-1 oid suffix (kind|nameLen|name|owner,
// no trailing 4 oid bytes) still decodes, with oid defaulting to 0 (the
// catalog's DatabaseOid "no override" sentinel).
func TestDecodeCreateDatabaseDefaultsOidForPreSlice1Payload(t *testing.T) {
	midway := []byte{RecordKindCreateDatabase, 4, 0, 't', 'p', 'c', 'h', 0, 0, 0, 0}
	name, owner, oid, err := DecodeCreateDatabase(midway)
	if err != nil {
		t.Fatalf("decode pre-slice-1 payload: %v", err)
	}
	if name != "tpch" {
		t.Errorf("name = %q, want tpch", name)
	}
	if owner != 0 {
		t.Errorf("owner = %d, want 0 (the payload's actual owner suffix)", owner)
	}
	if oid != 0 {
		t.Errorf("oid = %d, want 0 (no override — pre-slice-1 payload)", oid)
	}
}
