package wal

import (
	"reflect"
	"testing"
)

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

// TestEncodeDecodeDropTSConfigMappingRoundTrip pins the DU-002
// restart-persistence follow-up (M0119-0004, slice 446 RENAME/SET
// SCHEMA/DROP MAPPING follow-up) DROP MAPPING WAL record format.
func TestEncodeDecodeDropTSConfigMappingRoundTrip(t *testing.T) {
	cases := []struct{ name, schema, tokenType string }{
		{"myconfig", "public", "word"},
		{"otherconfig", "myschema", "asciiword"},
	}
	for _, c := range cases {
		raw := EncodeDropTSConfigMapping(c.name, c.schema, c.tokenType)
		if raw[0] != RecordKindDropTSConfigMapping {
			t.Errorf("%q/%q: kind byte = %d, want %d", c.name, c.tokenType, raw[0], RecordKindDropTSConfigMapping)
			continue
		}
		gotName, gotSchema, gotTokenType, err := DecodeDropTSConfigMapping(raw)
		if err != nil {
			t.Errorf("%q/%q: decode err: %v", c.name, c.tokenType, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotTokenType != c.tokenType {
			t.Errorf("%q/%q: decoded (%q, %q, %q)", c.name, c.tokenType, gotName, gotSchema, gotTokenType)
		}
	}
}

// TestEncodeDecodeRenameTSConfigRoundTrip pins the RENAME TO WAL record
// format (DU-002 restart-persistence follow-up, slice 446 follow-up,
// M0119-0004).
func TestEncodeDecodeRenameTSConfigRoundTrip(t *testing.T) {
	cases := []struct{ name, schema, newName string }{
		{"myconfig", "public", "renamedconfig"},
		{"日本語config", "myschema", "新config"}, // multi-byte UTF-8
	}
	for _, c := range cases {
		raw := EncodeRenameTSConfig(c.name, c.schema, c.newName)
		if raw[0] != RecordKindRenameTSConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindRenameTSConfig)
			continue
		}
		gotName, gotSchema, gotNewName, err := DecodeRenameTSConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotNewName != c.newName {
			t.Errorf("%q: decoded (%q, %q, %q)", c.name, gotName, gotSchema, gotNewName)
		}
	}
}

// TestEncodeDecodeSetTSConfigSchemaRoundTrip pins the SET SCHEMA WAL record
// format (DU-002 restart-persistence follow-up, slice 446 follow-up,
// M0119-0004).
func TestEncodeDecodeSetTSConfigSchemaRoundTrip(t *testing.T) {
	cases := []struct{ name, schema, newSchema string }{
		{"myconfig", "public", "otherschema"},
		{"otherconfig", "schema1", "schema2"},
	}
	for _, c := range cases {
		raw := EncodeSetTSConfigSchema(c.name, c.schema, c.newSchema)
		if raw[0] != RecordKindSetTSConfigSchema {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindSetTSConfigSchema)
			continue
		}
		gotName, gotSchema, gotNewSchema, err := DecodeSetTSConfigSchema(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotNewSchema != c.newSchema {
			t.Errorf("%q: decoded (%q, %q, %q)", c.name, gotName, gotSchema, gotNewSchema)
		}
	}
}

// TestDecodeDropTSConfigMappingRejectsWrongKind confirms the decoder
// surfaces a clear error when handed a record of a different kind.
func TestDecodeDropTSConfigMappingRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, err := DecodeDropTSConfigMapping(bogus); err == nil {
		t.Error("expected error decoding non-drop-tsconfig-mapping payload")
	}
}

// TestDecodeRenameTSConfigRejectsWrongKind mirrors the drop-mapping case.
func TestDecodeRenameTSConfigRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, err := DecodeRenameTSConfig(bogus); err == nil {
		t.Error("expected error decoding non-rename-tsconfig payload")
	}
}

// TestDecodeSetTSConfigSchemaRejectsWrongKind mirrors the drop-mapping case.
func TestDecodeSetTSConfigSchemaRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, err := DecodeSetTSConfigSchema(bogus); err == nil {
		t.Error("expected error decoding non-set-tsconfig-schema payload")
	}
}

// TestDecodeDropTSConfigMappingRejectsTruncatedPayload guards against
// silently returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeDropTSConfigMappingRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeDropTSConfigMapping("myconfig", "public", "word")
	truncated := raw[:len(raw)-1]
	if _, _, _, err := DecodeDropTSConfigMapping(truncated); err == nil {
		t.Error("expected error decoding truncated drop-tsconfig-mapping payload")
	}
}

// TestDecodeRenameTSConfigRejectsTruncatedPayload mirrors the drop-mapping
// case for RENAME TO.
func TestDecodeRenameTSConfigRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeRenameTSConfig("myconfig", "public", "renamedconfig")
	truncated := raw[:len(raw)-1]
	if _, _, _, err := DecodeRenameTSConfig(truncated); err == nil {
		t.Error("expected error decoding truncated rename-tsconfig payload")
	}
}

// TestDecodeSetTSConfigSchemaRejectsTruncatedPayload mirrors the
// drop-mapping case for SET SCHEMA.
func TestDecodeSetTSConfigSchemaRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeSetTSConfigSchema("myconfig", "public", "otherschema")
	truncated := raw[:len(raw)-1]
	if _, _, _, err := DecodeSetTSConfigSchema(truncated); err == nil {
		t.Error("expected error decoding truncated set-tsconfig-schema payload")
	}
}

// TestEncodeDecodeReplaceTSConfigMappingDictRoundTrip pins the ALTER MAPPING
// REPLACE WAL record format, covering both the token-type-scoped and bare
// (nil/empty TokenTypes) forms. DU-002 replacedict follow-up (M0119-0004).
func TestEncodeDecodeReplaceTSConfigMappingDictRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema   string
		tokenTypes     []string
		oldOID, newOID uint32
	}{
		{"myconfig", "public", []string{"asciiword"}, 3765, 16384},
		{"myconfig", "public", []string{"asciiword", "word"}, 3765, 16385},
		{"otherconfig", "myschema", nil, 3765, 16384}, // bare REPLACE form
	}
	for _, c := range cases {
		raw := EncodeReplaceTSConfigMappingDict(c.name, c.schema, c.tokenTypes, c.oldOID, c.newOID)
		if raw[0] != RecordKindReplaceTSConfigMappingDict {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindReplaceTSConfigMappingDict)
			continue
		}
		gotName, gotSchema, gotTokenTypes, gotOldOID, gotNewOID, err := DecodeReplaceTSConfigMappingDict(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotOldOID != c.oldOID || gotNewOID != c.newOID {
			t.Errorf("%q: decoded (%q, %q, oldOID=%d, newOID=%d)", c.name, gotName, gotSchema, gotOldOID, gotNewOID)
		}
		if len(gotTokenTypes) != len(c.tokenTypes) {
			t.Errorf("%q: decoded TokenTypes = %v, want %v", c.name, gotTokenTypes, c.tokenTypes)
			continue
		}
		for i := range c.tokenTypes {
			if gotTokenTypes[i] != c.tokenTypes[i] {
				t.Errorf("%q: decoded TokenTypes = %v, want %v", c.name, gotTokenTypes, c.tokenTypes)
				break
			}
		}
	}
}

// TestDecodeReplaceTSConfigMappingDictRejectsWrongKind mirrors the
// drop-mapping case.
func TestDecodeReplaceTSConfigMappingDictRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, _, _, err := DecodeReplaceTSConfigMappingDict(bogus); err == nil {
		t.Error("expected error decoding non-replace-tsconfig-mapping-dict payload")
	}
}

// TestDecodeReplaceTSConfigMappingDictRejectsTruncatedPayload guards against
// silently returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeReplaceTSConfigMappingDictRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeReplaceTSConfigMappingDict("myconfig", "public", []string{"asciiword"}, 3765, 16384)
	truncated := raw[:len(raw)-1]
	if _, _, _, _, _, err := DecodeReplaceTSConfigMappingDict(truncated); err == nil {
		t.Error("expected error decoding truncated replace-tsconfig-mapping-dict payload")
	}
}

// TestEncodeDecodeAlterTSConfigMappingRoundTrip pins the ALTER MAPPING FOR
// tok WITH dict [, ...] override form's wire format (RecordKindAlterTSConfigMapping,
// same shape as RecordKindAddTSConfigMapping). DU-002 slice 446 follow-up
// (M0119-0004).
func TestEncodeDecodeAlterTSConfigMappingRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema, tokenType string
		dictOIDs                []uint32
	}{
		{"myconfig", "public", "asciiword", []uint32{3765}},
		{"myconfig", "public", "word", []uint32{3765, 16384, 16385}},
		{"emptymap", "myschema", "email", []uint32{}},
	}
	for _, c := range cases {
		raw := EncodeAlterTSConfigMapping(c.name, c.schema, c.tokenType, c.dictOIDs)
		if raw[0] != RecordKindAlterTSConfigMapping {
			t.Errorf("%q/%q: kind byte = %d, want %d", c.name, c.tokenType, raw[0], RecordKindAlterTSConfigMapping)
			continue
		}
		gotName, gotSchema, gotTokenType, gotDictOIDs, err := DecodeAlterTSConfigMapping(raw)
		if err != nil {
			t.Errorf("%q/%q: decode err: %v", c.name, c.tokenType, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotTokenType != c.tokenType || !reflect.DeepEqual(gotDictOIDs, c.dictOIDs) {
			t.Errorf("%q/%q: decoded (%q, %q, %q, %v)", c.name, c.tokenType, gotName, gotSchema, gotTokenType, gotDictOIDs)
		}
	}
}

// TestDecodeAlterTSConfigMappingRejectsWrongKind mirrors the add-mapping case.
func TestDecodeAlterTSConfigMappingRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTSConfig("myconfig", "public")
	if _, _, _, _, err := DecodeAlterTSConfigMapping(bogus); err == nil {
		t.Error("expected error decoding non-alter-tsconfig-mapping payload")
	}
}

// TestDecodeAlterTSConfigMappingRejectsTruncatedPayload guards against
// silently returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeAlterTSConfigMappingRejectsTruncatedPayload(t *testing.T) {
	raw := EncodeAlterTSConfigMapping("myconfig", "public", "asciiword", []uint32{3765, 16384})
	truncated := raw[:len(raw)-1]
	if _, _, _, _, err := DecodeAlterTSConfigMapping(truncated); err == nil {
		t.Error("expected error decoding truncated alter-tsconfig-mapping payload")
	}
}
