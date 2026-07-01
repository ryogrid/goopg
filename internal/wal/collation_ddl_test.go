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
