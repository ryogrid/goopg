package wal

import "reflect"
import "testing"

// TestEncodeDecodeCreateUserMappingRoundTrip pins the M0122-0007 user-mapping
// registry restart-durability follow-up's CREATE USER MAPPING WAL record
// format. Encode -> Decode must return the original user/server/options/OID.
func TestEncodeDecodeCreateUserMappingRoundTrip(t *testing.T) {
	cases := []struct {
		user, server string
		options      []string
		oid          uint32
	}{
		{"someuser", "myserver", nil, 16384},
		{"produser", "typedserver", []string{"user=remote", "password=secret"}, 16400},
		{"emptyopts", "srv", []string{}, 16401},
		{"日本語ユーザ", "他のサーバ", []string{"オプション=値"}, 4294967295}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateUserMapping(c.user, c.server, c.options, c.oid)
		if raw[0] != RecordKindCreateUserMapping {
			t.Errorf("%q: kind byte = %d, want %d", c.user, raw[0], RecordKindCreateUserMapping)
			continue
		}
		gotUser, gotServer, gotOptions, gotOID, err := DecodeCreateUserMapping(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.user, err)
			continue
		}
		if gotUser != c.user || gotServer != c.server || gotOID != c.oid {
			t.Errorf("%q: decoded (%q, %q, %d)", c.user, gotUser, gotServer, gotOID)
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
	cases := []struct{ user, server string }{
		{"someuser", "myserver"},
		{"日本語ユーザ", "他のサーバ"},
	}
	for _, c := range cases {
		raw := EncodeDropUserMapping(c.user, c.server)
		if raw[0] != RecordKindDropUserMapping {
			t.Errorf("%q/%q: kind byte = %d, want %d", c.user, c.server, raw[0], RecordKindDropUserMapping)
			continue
		}
		gotUser, gotServer, err := DecodeDropUserMapping(raw)
		if err != nil {
			t.Errorf("%q/%q: decode err: %v", c.user, c.server, err)
			continue
		}
		if gotUser != c.user || gotServer != c.server {
			t.Errorf("decoded (%q, %q), want (%q, %q)", gotUser, gotServer, c.user, c.server)
		}
	}
}

// TestDecodeUserMappingRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record.
func TestDecodeUserMappingRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropUserMapping("u", "s")

	if _, _, _, _, err := DecodeCreateUserMapping(bogus); err == nil {
		t.Error("DecodeCreateUserMapping: expected error on wrong kind")
	}
	if _, _, err := DecodeDropUserMapping(EncodeCreateUserMapping("u", "s", nil, 1)); err == nil {
		t.Error("DecodeDropUserMapping: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateUserMapping", []byte{RecordKindCreateUserMapping, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, err := DecodeCreateUserMapping(p)
			return err
		}},
		{"DropUserMapping", []byte{RecordKindDropUserMapping, 10, 0}, func(p []byte) error {
			_, _, err := DecodeDropUserMapping(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
