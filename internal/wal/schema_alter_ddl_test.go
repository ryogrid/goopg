package wal

import "testing"

// TestEncodeDecodeAlterSchemaRenameRoundTrip guards DU-002 slice 440 resume
// point (3) (M0110-0001).
func TestEncodeDecodeAlterSchemaRenameRoundTrip(t *testing.T) {
	cases := []struct{ name, newName string }{
		{"s1", "s1_renamed"},
		{"s2", "s2_new"},
		{"", ""},
	}
	for _, c := range cases {
		raw := EncodeAlterSchemaRename(c.name, c.newName)
		if raw[0] != RecordKindAlterSchemaRename {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterSchemaRename)
		}
		gotName, gotNewName, err := DecodeAlterSchemaRename(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotNewName != c.newName {
			t.Errorf("%q: got (name=%q newName=%q)", c.name, gotName, gotNewName)
		}
	}
}

// TestEncodeDecodeAlterSchemaOwnerRoundTrip is the OWNER TO counterpart.
func TestEncodeDecodeAlterSchemaOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"s1", 16384},
		{"s2", 10},
		{"", 0},
	}
	for _, c := range cases {
		raw := EncodeAlterSchemaOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterSchemaOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterSchemaOwner)
		}
		gotName, gotOwnerOID, err := DecodeAlterSchemaOwner(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: got (name=%q ownerOID=%d)", c.name, gotName, gotOwnerOID)
		}
	}
}

// TestDecodeAlterSchemaRejectsWrongKind confirms each new decoder surfaces an
// error rather than silently misinterpreting a differently-kinded payload.
func TestDecodeAlterSchemaRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropSchema("s1")
	if _, _, err := DecodeAlterSchemaRename(bogus); err == nil {
		t.Error("DecodeAlterSchemaRename: expected error decoding drop-schema payload, got nil")
	}
	if _, _, err := DecodeAlterSchemaOwner(bogus); err == nil {
		t.Error("DecodeAlterSchemaOwner: expected error decoding drop-schema payload, got nil")
	}
}

// TestDecodeAlterSchemaRejectsTruncatedPayload guards against silently
// reading past the end of a truncated payload for each new decoder.
func TestDecodeAlterSchemaRejectsTruncatedPayload(t *testing.T) {
	renameFull := EncodeAlterSchemaRename("s1", "s1_new")
	for _, n := range []int{0, 1, 4, len(renameFull) - 1} {
		if _, _, err := DecodeAlterSchemaRename(renameFull[:n]); err == nil {
			t.Errorf("DecodeAlterSchemaRename: truncated to %d bytes, expected error, got nil", n)
		}
	}

	ownerFull := EncodeAlterSchemaOwner("s1", 16384)
	for _, n := range []int{0, 4, 6, len(ownerFull) - 1} {
		if _, _, err := DecodeAlterSchemaOwner(ownerFull[:n]); err == nil {
			t.Errorf("DecodeAlterSchemaOwner: truncated to %d bytes, expected error, got nil", n)
		}
	}
}
