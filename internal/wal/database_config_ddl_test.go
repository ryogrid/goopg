package wal

import "testing"

// TestEncodeDecodeAlterDatabaseSetConfigRoundTrip pins the M0119-0004-ACLHEAP
// (ALTER DATABASE ... SET follow-up) restart-persistence WAL record format.
func TestEncodeDecodeAlterDatabaseSetConfigRoundTrip(t *testing.T) {
	cases := []struct {
		dbOid       uint32
		name, value string
	}{
		{16384, "work_mem", "64MB"},
		{16384, "search_path", "public,pg_catalog"},
		{5, "日本語設定", "日本語の値"}, // multi-byte UTF-8
		{4294967295, "x", ""},   // empty value, max OID
	}
	for _, c := range cases {
		raw := EncodeAlterDatabaseSetConfig(c.dbOid, c.name, c.value)
		if raw[0] != RecordKindAlterDatabaseSetConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterDatabaseSetConfig)
			continue
		}
		gotOid, gotName, gotValue, err := DecodeAlterDatabaseSetConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotOid != c.dbOid || gotName != c.name || gotValue != c.value {
			t.Errorf("decoded (%d, %q, %q), want (%d, %q, %q)", gotOid, gotName, gotValue, c.dbOid, c.name, c.value)
		}
	}
}

// TestDecodeAlterDatabaseSetConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterDatabaseSetConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterDatabaseSetConfig(16384, "work_mem", "64MB")
	if _, _, _, err := DecodeAlterDatabaseSetConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, err := DecodeAlterDatabaseSetConfig([]byte{RecordKindAlterDatabaseResetConfig, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeAlterDatabaseResetConfigRoundTrip is the RESET <name>
// counterpart.
func TestEncodeDecodeAlterDatabaseResetConfigRoundTrip(t *testing.T) {
	cases := []struct {
		dbOid uint32
		name  string
	}{
		{16384, "work_mem"},
		{5, "日本語設定"},
	}
	for _, c := range cases {
		raw := EncodeAlterDatabaseResetConfig(c.dbOid, c.name)
		if raw[0] != RecordKindAlterDatabaseResetConfig {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterDatabaseResetConfig)
			continue
		}
		gotOid, gotName, err := DecodeAlterDatabaseResetConfig(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotOid != c.dbOid || gotName != c.name {
			t.Errorf("decoded (%d, %q), want (%d, %q)", gotOid, gotName, c.dbOid, c.name)
		}
	}
}

// TestDecodeAlterDatabaseResetConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterDatabaseResetConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterDatabaseResetConfig(16384, "work_mem")
	if _, _, err := DecodeAlterDatabaseResetConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, err := DecodeAlterDatabaseResetConfig([]byte{RecordKindAlterDatabaseSetConfig, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeAlterDatabaseResetAllConfigRoundTrip is the RESET ALL
// counterpart.
func TestEncodeDecodeAlterDatabaseResetAllConfigRoundTrip(t *testing.T) {
	for _, dbOid := range []uint32{16384, 5, 4294967295} {
		raw := EncodeAlterDatabaseResetAllConfig(dbOid)
		if raw[0] != RecordKindAlterDatabaseResetAllConfig {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindAlterDatabaseResetAllConfig)
			continue
		}
		gotOid, err := DecodeAlterDatabaseResetAllConfig(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotOid != dbOid {
			t.Errorf("decoded oid = %d, want %d", gotOid, dbOid)
		}
	}
}

// TestDecodeAlterDatabaseResetAllConfigRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeAlterDatabaseResetAllConfigRejectsTruncated(t *testing.T) {
	raw := EncodeAlterDatabaseResetAllConfig(16384)
	if _, err := DecodeAlterDatabaseResetAllConfig(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, err := DecodeAlterDatabaseResetAllConfig([]byte{RecordKindAlterDatabaseSetConfig, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}
