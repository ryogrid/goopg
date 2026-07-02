package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreateEventTriggerRoundTrip pins the DU-002
// restart-persistence (M0119-0004, loop #70 ledger resume point) CREATE
// EVENT TRIGGER WAL record format. Encode → Decode must return the
// original name/event/tags/OIDs.
func TestEncodeDecodeCreateEventTriggerRoundTrip(t *testing.T) {
	cases := []struct {
		name, event            string
		tags                   []string
		oid, ownerOID, funcOID uint32
	}{
		{"mytrig", "ddl_command_start", nil, 16384, 10, 16390},
		{"tagtrig", "ddl_command_end", []string{"CREATE TABLE", "ALTER TABLE"}, 16385, 16400, 16391},
		{"emptytags", "sql_drop", []string{}, 16386, 10, 16392},
		{"日本語トリガ", "login", []string{"ログイン"}, 4294967295, 16400, 16393}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateEventTrigger(c.name, c.event, c.tags, c.oid, c.ownerOID, c.funcOID)
		if raw[0] != RecordKindCreateEventTrigger {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateEventTrigger)
			continue
		}
		gotName, gotEvent, gotTags, gotOID, gotOwnerOID, gotFuncOID, err := DecodeCreateEventTrigger(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotEvent != c.event || gotOID != c.oid ||
			gotOwnerOID != c.ownerOID || gotFuncOID != c.funcOID {
			t.Errorf("%q: decoded (%q, %q, %d, %d, %d)", c.name, gotName, gotEvent, gotOID, gotOwnerOID, gotFuncOID)
		}
		if len(c.tags) == 0 && len(gotTags) != 0 {
			t.Errorf("%q: tags = %v, want empty", c.name, gotTags)
		} else if len(c.tags) > 0 && !reflect.DeepEqual(gotTags, c.tags) {
			t.Errorf("%q: tags = %v, want %v", c.name, gotTags, c.tags)
		}
	}
}

// TestEncodeDecodeDropEventTriggerRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropEventTriggerRoundTrip(t *testing.T) {
	for _, name := range []string{"mytrig", "日本語トリガ"} {
		raw := EncodeDropEventTrigger(name)
		if raw[0] != RecordKindDropEventTrigger {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropEventTrigger)
			continue
		}
		got, err := DecodeDropEventTrigger(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestEncodeDecodeAlterEventTriggerEnabledRoundTrip is the
// DISABLE/ENABLE[REPLICA|ALWAYS] counterpart.
func TestEncodeDecodeAlterEventTriggerEnabledRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		code byte
	}{
		{"mytrig", 'D'},
		{"mytrig", 'O'},
		{"mytrig", 'R'},
		{"mytrig", 'A'},
		{"日本語トリガ", 'D'},
	}
	for _, c := range cases {
		raw := EncodeAlterEventTriggerEnabled(c.name, c.code)
		if raw[0] != RecordKindAlterEventTriggerEnabled {
			t.Errorf("%q/%c: kind byte = %d, want %d", c.name, c.code, raw[0], RecordKindAlterEventTriggerEnabled)
			continue
		}
		gotName, gotCode, err := DecodeAlterEventTriggerEnabled(raw)
		if err != nil {
			t.Errorf("%q/%c: decode err: %v", c.name, c.code, err)
			continue
		}
		if gotName != c.name || gotCode != c.code {
			t.Errorf("%q/%c: decoded (%q, %c)", c.name, c.code, gotName, gotCode)
		}
	}
}

// TestEncodeDecodeAlterEventTriggerRenameRoundTrip is the RENAME TO
// counterpart.
func TestEncodeDecodeAlterEventTriggerRenameRoundTrip(t *testing.T) {
	cases := []struct {
		name, newName string
	}{
		{"mytrig", "renamed"},
		{"日本語トリガ", "新しい名前"},
	}
	for _, c := range cases {
		raw := EncodeAlterEventTriggerRename(c.name, c.newName)
		if raw[0] != RecordKindAlterEventTriggerRename {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterEventTriggerRename)
			continue
		}
		gotName, gotNewName, err := DecodeAlterEventTriggerRename(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotNewName != c.newName {
			t.Errorf("%q: decoded (%q, %q)", c.name, gotName, gotNewName)
		}
	}
}

// TestEncodeDecodeAlterEventTriggerOwnerRoundTrip is the OWNER TO
// counterpart.
func TestEncodeDecodeAlterEventTriggerOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"mytrig", 16400},
		{"日本語トリガ", 4294967295},
	}
	for _, c := range cases {
		raw := EncodeAlterEventTriggerOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterEventTriggerOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterEventTriggerOwner)
			continue
		}
		gotName, gotOwnerOID, err := DecodeAlterEventTriggerOwner(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: decoded (%q, %d)", c.name, gotName, gotOwnerOID)
		}
	}
}

// TestDecodeEventTriggerRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record, for every event trigger record kind.
func TestDecodeEventTriggerRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropEventTrigger("x")

	if _, _, _, _, _, _, err := DecodeCreateEventTrigger(bogus); err == nil {
		t.Error("DecodeCreateEventTrigger: expected error on wrong kind")
	}
	if _, err := DecodeDropEventTrigger(EncodeCreateEventTrigger("x", "ddl_command_start", nil, 1, 10, 20)); err == nil {
		t.Error("DecodeDropEventTrigger: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterEventTriggerEnabled(bogus); err == nil {
		t.Error("DecodeAlterEventTriggerEnabled: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterEventTriggerRename(bogus); err == nil {
		t.Error("DecodeAlterEventTriggerRename: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterEventTriggerOwner(bogus); err == nil {
		t.Error("DecodeAlterEventTriggerOwner: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateEventTrigger", []byte{RecordKindCreateEventTrigger, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, err := DecodeCreateEventTrigger(p)
			return err
		}},
		{"DropEventTrigger", []byte{RecordKindDropEventTrigger, 10, 0}, func(p []byte) error {
			_, err := DecodeDropEventTrigger(p)
			return err
		}},
		{"AlterEventTriggerEnabled", []byte{RecordKindAlterEventTriggerEnabled, 'D', 10, 0}, func(p []byte) error {
			_, _, err := DecodeAlterEventTriggerEnabled(p)
			return err
		}},
		{"AlterEventTriggerRename", []byte{RecordKindAlterEventTriggerRename, 10, 0}, func(p []byte) error {
			_, _, err := DecodeAlterEventTriggerRename(p)
			return err
		}},
		{"AlterEventTriggerOwner", []byte{RecordKindAlterEventTriggerOwner, 0, 0, 0, 0, 0}, func(p []byte) error {
			_, _, err := DecodeAlterEventTriggerOwner(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
