package wal

import "testing"

// TestEncodeDecodeRenameIndexRoundTrip pins RecordKindRenameIndex (DU-002
// slice 443): `ALTER INDEX name RENAME TO newname` on a real (non-TOAST)
// index previously had no catalog mutation at all, so there was nothing to
// WAL-log; now the rename is applied and durable across restart.
func TestEncodeDecodeRenameIndexRoundTrip(t *testing.T) {
	cases := []struct{ schema, oldName, newName string }{
		{"public", "idx_ab", "idx_ab_renamed"},
		{"myschema", "idx1", "idx2"},
		{"public", "日本語索引", "新しい索引"}, // multi-byte UTF-8
	}
	for _, c := range cases {
		raw := EncodeRenameIndex(c.schema, c.oldName, c.newName)
		if raw[0] != RecordKindRenameIndex {
			t.Errorf("%q: kind byte = %d, want %d", c.oldName, raw[0], RecordKindRenameIndex)
			continue
		}
		gotSchema, gotOldName, gotNewName, err := DecodeRenameIndex(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.oldName, err)
			continue
		}
		if gotSchema != c.schema || gotOldName != c.oldName || gotNewName != c.newName {
			t.Errorf("%q: decoded (%q, %q, %q), want (%q, %q, %q)", c.oldName, gotSchema, gotOldName, gotNewName, c.schema, c.oldName, c.newName)
		}
	}
}

func TestDecodeRenameIndexRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropIndex(DropIndexPayload{OID: 0, Schema: "public", Name: "idx_ab"})
	if _, _, _, err := DecodeRenameIndex(bogus); err == nil {
		t.Error("expected error decoding non-rename-index payload")
	}
}

func TestDecodeRenameIndexRejectsTruncatedPayload(t *testing.T) {
	// kind + schemaLen=10, but no bytes follow.
	truncated := []byte{RecordKindRenameIndex, 10, 0}
	if _, _, _, err := DecodeRenameIndex(truncated); err == nil {
		t.Error("expected error decoding truncated rename-index payload")
	}
}
