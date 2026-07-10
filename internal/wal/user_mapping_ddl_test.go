package wal

import "reflect"
import "testing"

// TestEncodeDecodeCreateUserMappingRoundTrip pins the M0122-0007 user-mapping
// registry restart-durability follow-up's CREATE USER MAPPING WAL record
// format. Encode -> Decode must return the original
// user/server/options/OID/dbOid (dbOid: M0122-0007 4e follow-up 37's
// trailing-appended field).
func TestEncodeDecodeCreateUserMappingRoundTrip(t *testing.T) {
	cases := []struct {
		user, server string
		options      []string
		oid          uint32
		dbOid        uint32
	}{
		{"someuser", "myserver", nil, 16384, 1},
		{"produser", "typedserver", []string{"user=remote", "password=secret"}, 16400, 5},
		{"emptyopts", "srv", []string{}, 16401, 0},
		{"日本語ユーザ", "他のサーバ", []string{"オプション=値"}, 4294967295, 4294967294}, // multi-byte UTF-8, max OID/dbOid
	}
	for _, c := range cases {
		raw := EncodeCreateUserMapping(c.user, c.server, c.options, c.oid, c.dbOid)
		if raw[0] != RecordKindCreateUserMapping {
			t.Errorf("%q: kind byte = %d, want %d", c.user, raw[0], RecordKindCreateUserMapping)
			continue
		}
		gotUser, gotServer, gotOptions, gotOID, gotDBOid, err := DecodeCreateUserMapping(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.user, err)
			continue
		}
		if gotUser != c.user || gotServer != c.server || gotOID != c.oid || gotDBOid != c.dbOid {
			t.Errorf("%q: decoded (%q, %q, %d, %d)", c.user, gotUser, gotServer, gotOID, gotDBOid)
		}
		if len(c.options) == 0 && len(gotOptions) != 0 {
			t.Errorf("%q: options = %v, want empty", c.user, gotOptions)
		} else if len(c.options) > 0 && !reflect.DeepEqual(gotOptions, c.options) {
			t.Errorf("%q: options = %v, want %v", c.user, gotOptions, c.options)
		}
	}
}

// TestEncodeDecodeDropUserMappingRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropUserMappingRoundTrip(t *testing.T) {
	cases := []struct {
		user, server string
		dbOid        uint32
	}{
		{"someuser", "myserver", 1},
		{"日本語ユーザ", "他のサーバ", 4294967295},
	}
	for _, c := range cases {
		raw := EncodeDropUserMapping(c.user, c.server, c.dbOid)
		if raw[0] != RecordKindDropUserMapping {
			t.Errorf("%q/%q: kind byte = %d, want %d", c.user, c.server, raw[0], RecordKindDropUserMapping)
			continue
		}
		gotUser, gotServer, gotDBOid, err := DecodeDropUserMapping(raw)
		if err != nil {
			t.Errorf("%q/%q: decode err: %v", c.user, c.server, err)
			continue
		}
		if gotUser != c.user || gotServer != c.server || gotDBOid != c.dbOid {
			t.Errorf("decoded (%q, %q, %d), want (%q, %q, %d)", gotUser, gotServer, gotDBOid, c.user, c.server, c.dbOid)
		}
	}
}

// TestDecodeCreateUserMappingPreTrailerPayloadDefaultsDBOidZero pins the
// backward-compatibility contract: a payload built before the M0122-0007 4e
// follow-up 37 trailing dbOid field (i.e. with no trailing 4 bytes) must
// still decode, reporting dbOid 0 (DefaultDBOid via catalog.NamespaceDBOid).
func TestDecodeCreateUserMappingPreTrailerPayloadDefaultsDBOidZero(t *testing.T) {
	full := EncodeCreateUserMapping("someuser", "myserver", nil, 16384, 5)
	pre := full[:len(full)-4] // strip the trailing dbOid to simulate an old payload
	user, server, _, oid, dbOid, err := DecodeCreateUserMapping(pre)
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if user != "someuser" || server != "myserver" || oid != 16384 {
		t.Errorf("decoded (%q, %q, %d), want (\"someuser\", \"myserver\", 16384)", user, server, oid)
	}
	if dbOid != 0 {
		t.Errorf("dbOid = %d, want 0 for a pre-trailer payload", dbOid)
	}
}

// TestDecodeDropUserMappingPreTrailerPayloadDefaultsDBOidZero is the DROP
// counterpart of TestDecodeCreateUserMappingPreTrailerPayloadDefaultsDBOidZero.
func TestDecodeDropUserMappingPreTrailerPayloadDefaultsDBOidZero(t *testing.T) {
	full := EncodeDropUserMapping("someuser", "myserver", 5)
	pre := full[:len(full)-4] // strip the trailing dbOid to simulate an old payload
	user, server, dbOid, err := DecodeDropUserMapping(pre)
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if user != "someuser" || server != "myserver" {
		t.Errorf("decoded (%q, %q), want (\"someuser\", \"myserver\")", user, server)
	}
	if dbOid != 0 {
		t.Errorf("dbOid = %d, want 0 for a pre-trailer payload", dbOid)
	}
}

// TestDecodeUserMappingRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record.
func TestDecodeUserMappingRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropUserMapping("u", "s", 1)

	if _, _, _, _, _, err := DecodeCreateUserMapping(bogus); err == nil {
		t.Error("DecodeCreateUserMapping: expected error on wrong kind")
	}
	if _, _, _, err := DecodeDropUserMapping(EncodeCreateUserMapping("u", "s", nil, 1, 1)); err == nil {
		t.Error("DecodeDropUserMapping: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateUserMapping", []byte{RecordKindCreateUserMapping, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, err := DecodeCreateUserMapping(p)
			return err
		}},
		{"DropUserMapping", []byte{RecordKindDropUserMapping, 10, 0}, func(p []byte) error {
			_, _, _, err := DecodeDropUserMapping(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
