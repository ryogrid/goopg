package wal

import "reflect"
import "testing"

// TestEncodeDecodeCreateForeignServerRoundTrip pins the M0122-0007
// foreign-server registry restart-durability follow-up's CREATE SERVER WAL
// record format. Encode -> Decode must return the original
// name/fdwName/type/version/options/OID/dbOid (dbOid: M0122-0007 4e
// follow-up 36's trailing-appended field).
func TestEncodeDecodeCreateForeignServerRoundTrip(t *testing.T) {
	cases := []struct {
		name, fdwName, srvType, srvVersion string
		options                            []string
		oid                                uint32
		dbOid                              uint32
	}{
		{"myserver", "postgres_fdw", "", "", nil, 16384, 1},
		{"typedserver", "postgres_fdw", "prod", "9.1", []string{"host=localhost", "port=5432"}, 16400, 5},
		{"emptyopts", "file_fdw", "", "", []string{}, 16401, 0},
		{"日本語サーバ", "他のfdw", "型", "版", []string{"オプション=値"}, 4294967295, 4294967294}, // multi-byte UTF-8, max OID/dbOid
	}
	for _, c := range cases {
		raw := EncodeCreateForeignServer(c.name, c.fdwName, c.srvType, c.srvVersion, c.options, c.oid, c.dbOid)
		if raw[0] != RecordKindCreateForeignServer {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateForeignServer)
			continue
		}
		gotName, gotFdwName, gotSrvType, gotSrvVersion, gotOptions, gotOID, gotDBOid, err := DecodeCreateForeignServer(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotFdwName != c.fdwName || gotSrvType != c.srvType ||
			gotSrvVersion != c.srvVersion || gotOID != c.oid || gotDBOid != c.dbOid {
			t.Errorf("%q: decoded (%q, %q, %q, %q, %d, %d)", c.name, gotName, gotFdwName, gotSrvType, gotSrvVersion, gotOID, gotDBOid)
		}
		if len(c.options) == 0 && len(gotOptions) != 0 {
			t.Errorf("%q: options = %v, want empty", c.name, gotOptions)
		} else if len(c.options) > 0 && !reflect.DeepEqual(gotOptions, c.options) {
			t.Errorf("%q: options = %v, want %v", c.name, gotOptions, c.options)
		}
	}
}

// TestEncodeDecodeDropForeignServerRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropForeignServerRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dbOid uint32
	}{{"myserver", 1}, {"日本語サーバ", 4294967295}} {
		raw := EncodeDropForeignServer(tc.name, tc.dbOid)
		if raw[0] != RecordKindDropForeignServer {
			t.Errorf("%q: kind byte = %d, want %d", tc.name, raw[0], RecordKindDropForeignServer)
			continue
		}
		gotName, gotDBOid, err := DecodeDropForeignServer(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", tc.name, err)
			continue
		}
		if gotName != tc.name || gotDBOid != tc.dbOid {
			t.Errorf("decoded (%q, %d), want (%q, %d)", gotName, gotDBOid, tc.name, tc.dbOid)
		}
	}
}

// TestDecodeCreateForeignServerPreTrailerPayloadDefaultsDBOidZero pins the
// backward-compatibility contract: a payload built before the M0122-0007 4e
// follow-up 36 trailing dbOid field (i.e. with no trailing 4 bytes) must
// still decode, reporting dbOid 0 (DefaultDBOid via catalog.NamespaceDBOid).
func TestDecodeCreateForeignServerPreTrailerPayloadDefaultsDBOidZero(t *testing.T) {
	full := EncodeCreateForeignServer("myserver", "postgres_fdw", "", "", nil, 16384, 5)
	pre := full[:len(full)-4] // strip the trailing dbOid to simulate an old payload
	name, fdwName, _, _, _, oid, dbOid, err := DecodeCreateForeignServer(pre)
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if name != "myserver" || fdwName != "postgres_fdw" || oid != 16384 {
		t.Errorf("decoded (%q, %q, %d), want (\"myserver\", \"postgres_fdw\", 16384)", name, fdwName, oid)
	}
	if dbOid != 0 {
		t.Errorf("dbOid = %d, want 0 for a pre-trailer payload", dbOid)
	}
}

// TestDecodeForeignServerRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record.
func TestDecodeForeignServerRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropForeignServer("x", 1)

	if _, _, _, _, _, _, _, err := DecodeCreateForeignServer(bogus); err == nil {
		t.Error("DecodeCreateForeignServer: expected error on wrong kind")
	}
	if _, _, err := DecodeDropForeignServer(EncodeCreateForeignServer("x", "fdw", "", "", nil, 1, 1)); err == nil {
		t.Error("DecodeDropForeignServer: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateForeignServer", []byte{RecordKindCreateForeignServer, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, _, err := DecodeCreateForeignServer(p)
			return err
		}},
		{"DropForeignServer", []byte{RecordKindDropForeignServer, 10, 0}, func(p []byte) error {
			_, _, err := DecodeDropForeignServer(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
