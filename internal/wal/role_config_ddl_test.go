package wal

import "testing"

// TestEncodeDecodeAlterRoleSetConfigRoundTrip pins the M0119-0004-ACLHEAP
// (ALTER ROLE ... SET follow-up) restart-persistence WAL record format —
// the setrole != 0 counterpart of database_config_ddl_test.go.
func TestEncodeDecodeAlterRoleSetConfigRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, dbOid uint32
		name, value    string
	}{
		{16385, 0, "work_mem", "64MB"},                     // cluster-wide (dbOid=0)
		{16385, 16384, "search_path", "public,pg_catalog"}, // IN DATABASE
		{5, 5, "日本語設定", "日本語の値"},                           // multi-byte UTF-8
		{4294967295, 4294967295, "x", ""},                  // empty value, max OIDs
	}
	for _, c := range cases {
		raw := EncodeAlterRoleSetConfig(c.roleOid, c.dbOid, c.name, c.value)
		if raw[0] != RecordKindAlterRoleSetConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterRoleSetConfig)
			continue
		}
		gotRoleOid, gotDBOid, gotName, gotValue, err := DecodeAlterRoleSetConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotRoleOid != c.roleOid || gotDBOid != c.dbOid || gotName != c.name || gotValue != c.value {
			t.Errorf("decoded (%d, %d, %q, %q), want (%d, %d, %q, %q)",
				gotRoleOid, gotDBOid, gotName, gotValue, c.roleOid, c.dbOid, c.name, c.value)
		}
	}
}

// TestDecodeAlterRoleSetConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterRoleSetConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterRoleSetConfig(16385, 16384, "work_mem", "64MB")
	if _, _, _, _, err := DecodeAlterRoleSetConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, _, err := DecodeAlterRoleSetConfig([]byte{RecordKindAlterRoleResetConfig, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeAlterRoleResetConfigRoundTrip is the RESET <name>
// counterpart.
func TestEncodeDecodeAlterRoleResetConfigRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, dbOid uint32
		name           string
	}{
		{16385, 0, "work_mem"},
		{16385, 16384, "search_path"},
		{5, 5, "日本語設定"},
	}
	for _, c := range cases {
		raw := EncodeAlterRoleResetConfig(c.roleOid, c.dbOid, c.name)
		if raw[0] != RecordKindAlterRoleResetConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterRoleResetConfig)
			continue
		}
		gotRoleOid, gotDBOid, gotName, err := DecodeAlterRoleResetConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotRoleOid != c.roleOid || gotDBOid != c.dbOid || gotName != c.name {
			t.Errorf("decoded (%d, %d, %q), want (%d, %d, %q)", gotRoleOid, gotDBOid, gotName, c.roleOid, c.dbOid, c.name)
		}
	}
}

// TestDecodeAlterRoleResetConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterRoleResetConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterRoleResetConfig(16385, 16384, "work_mem")
	if _, _, _, err := DecodeAlterRoleResetConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, err := DecodeAlterRoleResetConfig([]byte{RecordKindAlterRoleSetConfig, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeAlterRoleResetAllConfigRoundTrip is the RESET ALL
// counterpart.
func TestEncodeDecodeAlterRoleResetAllConfigRoundTrip(t *testing.T) {
	cases := []struct{ roleOid, dbOid uint32 }{
		{16385, 0},
		{16385, 16384},
		{4294967295, 4294967295},
	}
	for _, c := range cases {
		raw := EncodeAlterRoleResetAllConfig(c.roleOid, c.dbOid)
		if raw[0] != RecordKindAlterRoleResetAllConfig {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindAlterRoleResetAllConfig)
			continue
		}
		gotRoleOid, gotDBOid, err := DecodeAlterRoleResetAllConfig(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotRoleOid != c.roleOid || gotDBOid != c.dbOid {
			t.Errorf("decoded (%d, %d), want (%d, %d)", gotRoleOid, gotDBOid, c.roleOid, c.dbOid)
		}
	}
}

// TestDecodeAlterRoleResetAllConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterRoleResetAllConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterRoleResetAllConfig(16385, 16384)
	if _, _, err := DecodeAlterRoleResetAllConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, err := DecodeAlterRoleResetAllConfig([]byte{RecordKindAlterRoleSetConfig, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}
