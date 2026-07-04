package wal

import (
	"testing"
)

// TestEncodeDecodeCreateCollationRoundTrip pins the DU-002
// restart-persistence (M0119-0004 follow-up) CREATE COLLATION WAL record
// format. Encode → Decode must return the original name/schema/collate/
// ctype/locale/rules/OIDs/encoding/provider/deterministic flag; an Encode of
// a string longer than 64 KiB gets defensively truncated rather than
// panicking.
func TestEncodeDecodeCreateCollationRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema, collate, ctype, locale, rules string
		oid, ownerOID                               uint32
		encoding                                     int32
		provider                                     byte
		deterministic                                bool
	}{
		{"mycoll", "public", "C", "C", "", "", 16384, 10, -1, 'c', true},
		{"icucoll", "public", "", "", "en-US", "&a < b", 16385, 10, 6, 'i', true},
		{"nondet", "myschema", "", "", "und", "", 16386, 16400, -1, 'i', false},
		{"日本語照合", "myschema", "ja_JP", "ja_JP", "", "", 4294967295, 16400, 6, 'c', true}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateCollation(c.name, c.schema, c.collate, c.ctype, c.locale, c.rules, c.oid, c.ownerOID, c.encoding, c.provider, c.deterministic)
		if raw[0] != RecordKindCreateCollation {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateCollation)
			continue
		}
		gotName, gotSchema, gotCollate, gotCtype, gotLocale, gotRules, gotOID, gotOwnerOID, gotEncoding, gotProvider, gotDeterministic, err := DecodeCreateCollation(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotCollate != c.collate || gotCtype != c.ctype ||
			gotLocale != c.locale || gotRules != c.rules || gotOID != c.oid || gotOwnerOID != c.ownerOID ||
			gotEncoding != c.encoding || gotProvider != c.provider || gotDeterministic != c.deterministic {
			t.Errorf("%q: decoded (%q, %q, %q, %q, %q, %q, %d, %d, %d, %c, %v)",
				c.name, gotName, gotSchema, gotCollate, gotCtype, gotLocale, gotRules, gotOID, gotOwnerOID, gotEncoding, gotProvider, gotDeterministic)
		}
	}
}

// TestEncodeDecodeDropCollationRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropCollationRoundTrip(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"mycoll", "public"},
		{"icucoll", "myschema"},
	}
	for _, c := range cases {
		raw := EncodeDropCollation(c.name, c.schema)
		if raw[0] != RecordKindDropCollation {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindDropCollation)
			continue
		}
		gotName, gotSchema, err := DecodeDropCollation(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema {
			t.Errorf("%q: decoded (%q, %q)", c.name, gotName, gotSchema)
		}
	}
}

// TestDecodeCreateCollationRejectsWrongKind confirms the decoder surfaces a
// clear error when handed a record of a different kind.
func TestDecodeCreateCollationRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropCollation("mycoll", "public")
	if _, _, _, _, _, _, _, _, _, _, _, err := DecodeCreateCollation(bogus); err == nil {
		t.Error("expected error decoding non-create-collation payload")
	}
}

// TestDecodeCreateCollationRejectsTruncatedPayload guards against silently
// returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeCreateCollationRejectsTruncatedPayload(t *testing.T) {
	// kind + oid(4) + ownerOID(4) + encoding(4) + provider(1) + deterministicFlag(1) = 15 bytes,
	// but the payload is one byte short of that fixed header.
	truncated := make([]byte, 14)
	truncated[0] = RecordKindCreateCollation
	if _, _, _, _, _, _, _, _, _, _, _, err := DecodeCreateCollation(truncated); err == nil {
		t.Error("expected error decoding truncated create-collation payload")
	}
}

// TestDecodeDropCollationRejectsTruncatedPayload mirrors the create case for
// DROP COLLATION.
func TestDecodeDropCollationRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10, but no bytes follow.
	truncated := []byte{RecordKindDropCollation, 10, 0}
	if _, _, err := DecodeDropCollation(truncated); err == nil {
		t.Error("expected error decoding truncated drop-collation payload")
	}
}

// TestEncodeDecodeAlterCollationRenameRoundTrip pins the DU-002
// restart-persistence follow-up (M0119-0004, loop #50 ledger resume point
// (a)) ALTER COLLATION RENAME TO WAL record format.
func TestEncodeDecodeAlterCollationRenameRoundTrip(t *testing.T) {
	cases := []struct{ name, schema, newName string }{
		{"mycoll", "public", "renamedcoll"},
		{"icucoll", "myschema", "icucoll2"},
		{"日本語照合", "myschema", "新名前"}, // multi-byte UTF-8
	}
	for _, c := range cases {
		raw := EncodeAlterCollationRename(c.name, c.schema, c.newName)
		if raw[0] != RecordKindAlterCollationRename {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterCollationRename)
			continue
		}
		gotName, gotSchema, gotNewName, err := DecodeAlterCollationRename(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotNewName != c.newName {
			t.Errorf("%q: decoded (%q, %q, %q)", c.name, gotName, gotSchema, gotNewName)
		}
	}
}

// TestEncodeDecodeAlterCollationOwnerRoundTrip is the OWNER TO counterpart.
func TestEncodeDecodeAlterCollationOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema string
		ownerOID     uint32
	}{
		{"mycoll", "public", 16400},
		{"icucoll", "myschema", 4294967295}, // max OID
	}
	for _, c := range cases {
		raw := EncodeAlterCollationOwner(c.name, c.schema, c.ownerOID)
		if raw[0] != RecordKindAlterCollationOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterCollationOwner)
			continue
		}
		gotName, gotSchema, gotOwnerOID, err := DecodeAlterCollationOwner(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: decoded (%q, %q, %d)", c.name, gotName, gotSchema, gotOwnerOID)
		}
	}
}

// TestDecodeAlterCollationRenameRejectsWrongKind / TruncatedPayload and the
// owner counterparts mirror the CREATE/DROP guards above.
func TestDecodeAlterCollationRenameRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropCollation("mycoll", "public")
	if _, _, _, err := DecodeAlterCollationRename(bogus); err == nil {
		t.Error("expected error decoding non-alter-collation-rename payload")
	}
}

func TestDecodeAlterCollationRenameRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10, but no bytes follow.
	truncated := []byte{RecordKindAlterCollationRename, 10, 0}
	if _, _, _, err := DecodeAlterCollationRename(truncated); err == nil {
		t.Error("expected error decoding truncated alter-collation-rename payload")
	}
}

func TestDecodeAlterCollationOwnerRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropCollation("mycoll", "public")
	if _, _, _, err := DecodeAlterCollationOwner(bogus); err == nil {
		t.Error("expected error decoding non-alter-collation-owner payload")
	}
}

func TestDecodeAlterCollationOwnerRejectsTruncatedPayload(t *testing.T) {
	// kind + ownerOID(4) = 5 bytes, but the payload is one byte short of the fixed 9-byte header.
	truncated := make([]byte, 8)
	truncated[0] = RecordKindAlterCollationOwner
	if _, _, _, err := DecodeAlterCollationOwner(truncated); err == nil {
		t.Error("expected error decoding truncated alter-collation-owner payload")
	}
}

// TestEncodeDecodeAlterCollationSetSchemaRoundTrip is the SET SCHEMA
// counterpart (DU-002 slice 442, closing the last unmodelled ALTER COLLATION
// form).
func TestEncodeDecodeAlterCollationSetSchemaRoundTrip(t *testing.T) {
	cases := []struct{ name, schema, newSchema string }{
		{"mycoll", "public", "otherschema"},
		{"icucoll", "myschema", "myschema2"},
		{"日本語照合", "myschema", "新しいスキーマ"}, // multi-byte UTF-8
	}
	for _, c := range cases {
		raw := EncodeAlterCollationSetSchema(c.name, c.schema, c.newSchema)
		if raw[0] != RecordKindAlterCollationSetSchema {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterCollationSetSchema)
			continue
		}
		gotName, gotSchema, gotNewSchema, err := DecodeAlterCollationSetSchema(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotNewSchema != c.newSchema {
			t.Errorf("%q: decoded (%q, %q, %q)", c.name, gotName, gotSchema, gotNewSchema)
		}
	}
}

func TestDecodeAlterCollationSetSchemaRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropCollation("mycoll", "public")
	if _, _, _, err := DecodeAlterCollationSetSchema(bogus); err == nil {
		t.Error("expected error decoding non-alter-collation-set-schema payload")
	}
}

func TestDecodeAlterCollationSetSchemaRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10, but no bytes follow.
	truncated := []byte{RecordKindAlterCollationSetSchema, 10, 0}
	if _, _, _, err := DecodeAlterCollationSetSchema(truncated); err == nil {
		t.Error("expected error decoding truncated alter-collation-set-schema payload")
	}
}
