package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreateTSDictRoundTrip pins the DU-002 restart-persistence
// (M0119-0004 follow-up to slice 437) CREATE TEXT SEARCH DICTIONARY WAL
// record format.
func TestEncodeDecodeCreateTSDictRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema, initOption string
		oid, ownerOID, template  uint32
	}{
		{"simple_dict", "public", `"STOPWORDS" = 'english'`, 16384, 10, 3727},
		{"日本語辞書", "myschema", "", 4294967295, 16400, 3742}, // multi-byte UTF-8, max OID, no options
	}
	for _, c := range cases {
		raw := EncodeCreateTSDict(c.name, c.schema, c.initOption, c.oid, c.ownerOID, c.template)
		if raw[0] != RecordKindCreateTSDict {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateTSDict)
			continue
		}
		gotName, gotSchema, gotInitOption, gotOID, gotOwnerOID, gotTemplate, err := DecodeCreateTSDict(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotInitOption != c.initOption ||
			gotOID != c.oid || gotOwnerOID != c.ownerOID || gotTemplate != c.template {
			t.Errorf("%q: decoded (%q, %q, %q, %d, %d, %d)",
				c.name, gotName, gotSchema, gotInitOption, gotOID, gotOwnerOID, gotTemplate)
		}
	}
}

// TestEncodeDecodeDropTSDictRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropTSDictRoundTrip(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"simple_dict", "public"},
		{"other_dict", "myschema"},
	}
	for _, c := range cases {
		raw := EncodeDropTSDict(c.name, c.schema)
		if raw[0] != RecordKindDropTSDict {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindDropTSDict)
			continue
		}
		gotName, gotSchema, err := DecodeDropTSDict(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema {
			t.Errorf("%q: decoded (%q, %q)", c.name, gotName, gotSchema)
		}
	}
}

// TestDecodeCreateTSDictRejectsWrongKind confirms the decoder surfaces a
// clear error when handed a record of a different kind.
func TestDecodeCreateTSDictRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSDict("simple_dict", "public")
	if _, _, _, _, _, _, err := DecodeCreateTSDict(bogus); err == nil {
		t.Error("expected error decoding non-create-tsdict payload")
	}
}

// TestDecodeCreateTSDictRejectsTruncatedPayload guards against silently
// returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeCreateTSDictRejectsTruncatedPayload(t *testing.T) {
	truncated := make([]byte, 12)
	truncated[0] = RecordKindCreateTSDict
	if _, _, _, _, _, _, err := DecodeCreateTSDict(truncated); err == nil {
		t.Error("expected error decoding truncated create-tsdict payload")
	}
}

// TestDecodeDropTSDictRejectsTruncatedPayload mirrors the create case for
// DROP TEXT SEARCH DICTIONARY.
func TestDecodeDropTSDictRejectsTruncatedPayload(t *testing.T) {
	truncated := []byte{RecordKindDropTSDict, 10, 0}
	if _, _, err := DecodeDropTSDict(truncated); err == nil {
		t.Error("expected error decoding truncated drop-tsdict payload")
	}
}

// TestEncodeDecodeCreateTSConfigRoundTrip pins the DU-002
// restart-persistence (M0119-0004 follow-up to slice 446) CREATE TEXT
// SEARCH CONFIGURATION WAL record format.
func TestEncodeDecodeCreateTSConfigRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema          string
		oid, ownerOID, parser uint32
	}{
		{"myconfig", "public", 16384, 10, 3722},
		{"日本語設定", "myschema", 4294967295, 16400, 3722},
	}
	for _, c := range cases {
		raw := EncodeCreateTSConfig(c.name, c.schema, c.oid, c.ownerOID, c.parser)
		if raw[0] != RecordKindCreateTSConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateTSConfig)
			continue
		}
		gotName, gotSchema, gotOID, gotOwnerOID, gotParser, err := DecodeCreateTSConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotOID != c.oid || gotOwnerOID != c.ownerOID || gotParser != c.parser {
			t.Errorf("%q: decoded (%q, %q, %d, %d, %d)", c.name, gotName, gotSchema, gotOID, gotOwnerOID, gotParser)
		}
	}
}

// TestEncodeDecodeAddTSConfigMappingRoundTrip pins the ALTER TEXT SEARCH
// CONFIGURATION ... ADD MAPPING WAL record format, including the
// zero-dictionaries edge case.
func TestEncodeDecodeAddTSConfigMappingRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema, tokenType string
		dictOIDs                []uint32
	}{
		{"myconfig", "public", "asciiword", []uint32{3765}},
		{"myconfig", "public", "word", []uint32{3765, 16384, 16385}},
		{"emptymap", "myschema", "email", []uint32{}},
	}
	for _, c := range cases {
		raw := EncodeAddTSConfigMapping(c.name, c.schema, c.tokenType, c.dictOIDs)
		if raw[0] != RecordKindAddTSConfigMapping {
			t.Errorf("%q/%q: kind byte = %d, want %d", c.name, c.tokenType, raw[0], RecordKindAddTSConfigMapping)
			continue
		}
		gotName, gotSchema, gotTokenType, gotDictOIDs, err := DecodeAddTSConfigMapping(raw)
		if err != nil {
			t.Errorf("%q/%q: decode err: %v", c.name, c.tokenType, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotTokenType != c.tokenType || !reflect.DeepEqual(gotDictOIDs, c.dictOIDs) {
			t.Errorf("%q/%q: decoded (%q, %q, %q, %v)", c.name, c.tokenType, gotName, gotSchema, gotTokenType, gotDictOIDs)
		}
	}
}

// TestEncodeDecodeDropTSConfigRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropTSConfigRoundTrip(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"myconfig", "public"},
		{"otherconfig", "myschema"},
	}
	for _, c := range cases {
		raw := EncodeDropTSConfig(c.name, c.schema)
		if raw[0] != RecordKindDropTSConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindDropTSConfig)
			continue
		}
		gotName, gotSchema, err := DecodeDropTSConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema {
			t.Errorf("%q: decoded (%q, %q)", c.name, gotName, gotSchema)
		}
	}
}

// TestDecodeCreateTSConfigRejectsWrongKind confirms the decoder surfaces a
// clear error when handed a record of a different kind.
func TestDecodeCreateTSConfigRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, _, _, err := DecodeCreateTSConfig(bogus); err == nil {
		t.Error("expected error decoding non-create-tsconfig payload")
	}
}

// TestDecodeAddTSConfigMappingRejectsWrongKind mirrors the create case.
func TestDecodeAddTSConfigMappingRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, _, err := DecodeAddTSConfigMapping(bogus); err == nil {
		t.Error("expected error decoding non-add-tsconfig-mapping payload")
	}
}

// TestDecodeAddTSConfigMappingRejectsTruncatedPayload guards against
// silently returning empty/zero fields when the on-disk record is corrupt
// (truncated mid dictOID list).
func TestDecodeAddTSConfigMappingRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeAddTSConfigMapping("myconfig", "public", "word", []uint32{3765, 16384})
	truncated := raw[:len(raw)-1]
	if _, _, _, _, err := DecodeAddTSConfigMapping(truncated); err == nil {
		t.Error("expected error decoding truncated add-tsconfig-mapping payload")
	}
}

// TestDecodeDropTSConfigRejectsTruncatedPayload mirrors the create case for
// DROP TEXT SEARCH CONFIGURATION.
func TestDecodeDropTSConfigRejectsTruncatedPayload(t *testing.T) {
	truncated := []byte{RecordKindDropTSConfig, 10, 0}
	if _, _, err := DecodeDropTSConfig(truncated); err == nil {
		t.Error("expected error decoding truncated drop-tsconfig payload")
	}
}
