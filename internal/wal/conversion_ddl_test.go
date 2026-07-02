package wal

import (
	"testing"
)

// TestEncodeDecodeCreateConversionRoundTrip pins the DU-002
// restart-persistence (M0119-0004 follow-up) CREATE CONVERSION WAL record
// format. Encode → Decode must return the original name/schema/proc
// name/OIDs/encodings/default flag; an Encode of a name longer than 64 KiB
// gets defensively truncated rather than panicking.
func TestEncodeDecodeCreateConversionRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema, procSchema, procName string
		oid, ownerOID, funcOID             uint32
		forEncoding, toEncoding            int32
		isDefault                          bool
	}{
		{"myconv", "public", "pg_catalog", "utf8_to_ascii", 16384, 10, 1811, 6, 0, false},
		{"defconv", "public", "pg_catalog", "latin1_to_utf8", 16385, 10, 1230, 8, 6, true},
		{"日本語変換", "myschema", "myschema", "custom_conv", 4294967295, 16400, 1, 7, 6, false}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateConversion(c.name, c.schema, c.procSchema, c.procName, c.oid, c.ownerOID, c.funcOID, c.forEncoding, c.toEncoding, c.isDefault)
		if raw[0] != RecordKindCreateConversion {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateConversion)
			continue
		}
		gotName, gotSchema, gotProcSchema, gotProcName, gotOID, gotOwnerOID, gotFuncOID, gotForEnc, gotToEnc, gotDefault, err := DecodeCreateConversion(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema || gotProcSchema != c.procSchema || gotProcName != c.procName ||
			gotOID != c.oid || gotOwnerOID != c.ownerOID || gotFuncOID != c.funcOID ||
			gotForEnc != c.forEncoding || gotToEnc != c.toEncoding || gotDefault != c.isDefault {
			t.Errorf("%q: decoded (%q, %q, %q, %q, %d, %d, %d, %d, %d, %v)",
				c.name, gotName, gotSchema, gotProcSchema, gotProcName, gotOID, gotOwnerOID, gotFuncOID, gotForEnc, gotToEnc, gotDefault)
		}
	}
}

// TestEncodeDecodeDropConversionRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropConversionRoundTrip(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"myconv", "public"},
		{"defconv", "myschema"},
	}
	for _, c := range cases {
		raw := EncodeDropConversion(c.name, c.schema)
		if raw[0] != RecordKindDropConversion {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindDropConversion)
			continue
		}
		gotName, gotSchema, err := DecodeDropConversion(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSchema != c.schema {
			t.Errorf("%q: decoded (%q, %q)", c.name, gotName, gotSchema)
		}
	}
}

// TestDecodeCreateConversionRejectsWrongKind confirms the decoder surfaces a
// clear error when handed a record of a different kind.
func TestDecodeCreateConversionRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropConversion("myconv", "public")
	if _, _, _, _, _, _, _, _, _, _, err := DecodeCreateConversion(bogus); err == nil {
		t.Error("expected error decoding non-create-conversion payload")
	}
}

// TestDecodeCreateConversionRejectsTruncatedPayload guards against silently
// returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeCreateConversionRejectsTruncatedPayload(t *testing.T) {
	// kind + oid(4) + ownerOID(4) + funcOID(4) + forEnc(4) + toEnc(4) + defaultFlag(1) = 22 bytes,
	// but the payload is one byte short of that fixed header.
	truncated := make([]byte, 21)
	truncated[0] = RecordKindCreateConversion
	if _, _, _, _, _, _, _, _, _, _, err := DecodeCreateConversion(truncated); err == nil {
		t.Error("expected error decoding truncated create-conversion payload")
	}
}

// TestDecodeDropConversionRejectsTruncatedPayload mirrors the create case for
// DROP CONVERSION.
func TestDecodeDropConversionRejectsTruncatedPayload(t *testing.T) {
	// kind + nameLen=10, but no bytes follow.
	truncated := []byte{RecordKindDropConversion, 10, 0}
	if _, _, err := DecodeDropConversion(truncated); err == nil {
		t.Error("expected error decoding truncated drop-conversion payload")
	}
}
