package wal

import (
	"testing"
)

// TestEncodeDecodeCreateTransformRoundTrip pins the M0119-0004 restart-
// persistence CREATE TRANSFORM WAL record format. Encode → Decode must
// return the original type/lang/OIDs; an Encode of a name longer than 64 KiB
// gets defensively truncated rather than panicking.
func TestEncodeDecodeCreateTransformRoundTrip(t *testing.T) {
	cases := []struct {
		typeName    string
		lang        string
		oid         uint32
		fromFuncOID uint32
		toFuncOID   uint32
	}{
		{"integer", "sql", 16384, 3721, 2406},       // the CREATE TRANSFORM FOR int fixture shape
		{"integer", "sql", 16384, 0, 2406},          // FROM SQL absent
		{"integer", "sql", 16384, 3721, 0},          // TO SQL absent
		{"日本語型", "plpgsql", 4294967295, 1, 2}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateTransform(c.typeName, c.lang, c.oid, c.fromFuncOID, c.toFuncOID)
		if raw[0] != RecordKindCreateTransform {
			t.Errorf("type %q lang %q: kind byte = %d, want %d", c.typeName, c.lang, raw[0], RecordKindCreateTransform)
			continue
		}
		gotType, gotLang, gotOID, gotFrom, gotTo, err := DecodeCreateTransform(raw)
		if err != nil {
			t.Errorf("type %q lang %q: decode err: %v", c.typeName, c.lang, err)
			continue
		}
		if gotType != c.typeName || gotLang != c.lang || gotOID != c.oid || gotFrom != c.fromFuncOID || gotTo != c.toFuncOID {
			t.Errorf("type %q lang %q: decoded (%q, %q, %d, %d, %d)", c.typeName, c.lang, gotType, gotLang, gotOID, gotFrom, gotTo)
		}
	}
}

// TestEncodeDecodeDropTransformRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropTransformRoundTrip(t *testing.T) {
	cases := []struct{ typeName, lang string }{
		{"integer", "sql"},
		{"text", "plpgsql"},
	}
	for _, c := range cases {
		raw := EncodeDropTransform(c.typeName, c.lang)
		if raw[0] != RecordKindDropTransform {
			t.Errorf("type %q lang %q: kind byte = %d, want %d", c.typeName, c.lang, raw[0], RecordKindDropTransform)
			continue
		}
		gotType, gotLang, err := DecodeDropTransform(raw)
		if err != nil {
			t.Errorf("type %q lang %q: decode err: %v", c.typeName, c.lang, err)
			continue
		}
		if gotType != c.typeName || gotLang != c.lang {
			t.Errorf("type %q lang %q: decoded (%q, %q)", c.typeName, c.lang, gotType, gotLang)
		}
	}
}

// TestDecodeCreateTransformRejectsWrongKind confirms the decoder surfaces a
// clear error when handed a record of a different kind.
func TestDecodeCreateTransformRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropTransform("integer", "sql")
	if _, _, _, _, _, err := DecodeCreateTransform(bogus); err == nil {
		t.Error("expected error decoding non-create-transform payload")
	}
}

// TestDecodeCreateTransformRejectsTruncatedPayload guards against silently
// returning empty/zero fields when the on-disk record is corrupt.
func TestDecodeCreateTransformRejectsTruncatedPayload(t *testing.T) {
	// kind + oid(4) + fromFuncOID(4) + toFuncOID(4) + typeLen=10, but no bytes follow.
	truncated := []byte{RecordKindCreateTransform, 0, 0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 0}
	if _, _, _, _, _, err := DecodeCreateTransform(truncated); err == nil {
		t.Error("expected error decoding truncated create-transform payload")
	}
}

// TestDecodeDropTransformRejectsTruncatedPayload mirrors the create case for
// DROP TRANSFORM.
func TestDecodeDropTransformRejectsTruncatedPayload(t *testing.T) {
	// kind + typeLen=10, but no bytes follow.
	truncated := []byte{RecordKindDropTransform, 10, 0}
	if _, _, err := DecodeDropTransform(truncated); err == nil {
		t.Error("expected error decoding truncated drop-transform payload")
	}
}
