package wal

import "reflect"
import "testing"

// TestEncodeDecodeCreateForeignServerRoundTrip pins the M0122-0007
// foreign-server registry restart-durability follow-up's CREATE SERVER WAL
// record format. Encode -> Decode must return the original
// name/fdwName/type/version/options/OID.
func TestEncodeDecodeCreateForeignServerRoundTrip(t *testing.T) {
	cases := []struct {
		name, fdwName, srvType, srvVersion string
		options                            []string
		oid                                uint32
	}{
		{"myserver", "postgres_fdw", "", "", nil, 16384},
		{"typedserver", "postgres_fdw", "prod", "9.1", []string{"host=localhost", "port=5432"}, 16400},
		{"emptyopts", "file_fdw", "", "", []string{}, 16401},
		{"日本語サーバ", "他のfdw", "型", "版", []string{"オプション=値"}, 4294967295}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateForeignServer(c.name, c.fdwName, c.srvType, c.srvVersion, c.options, c.oid)
		if raw[0] != RecordKindCreateForeignServer {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateForeignServer)
			continue
		}
		gotName, gotFdwName, gotSrvType, gotSrvVersion, gotOptions, gotOID, err := DecodeCreateForeignServer(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotFdwName != c.fdwName || gotSrvType != c.srvType ||
			gotSrvVersion != c.srvVersion || gotOID != c.oid {
			t.Errorf("%q: decoded (%q, %q, %q, %q, %d)", c.name, gotName, gotFdwName, gotSrvType, gotSrvVersion, gotOID)
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
	for _, name := range []string{"myserver", "日本語サーバ"} {
		raw := EncodeDropForeignServer(name)
		if raw[0] != RecordKindDropForeignServer {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropForeignServer)
			continue
		}
		got, err := DecodeDropForeignServer(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestDecodeForeignServerRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record.
func TestDecodeForeignServerRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropForeignServer("x")

	if _, _, _, _, _, _, err := DecodeCreateForeignServer(bogus); err == nil {
		t.Error("DecodeCreateForeignServer: expected error on wrong kind")
	}
	if _, err := DecodeDropForeignServer(EncodeCreateForeignServer("x", "fdw", "", "", nil, 1)); err == nil {
		t.Error("DecodeDropForeignServer: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateForeignServer", []byte{RecordKindCreateForeignServer, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, err := DecodeCreateForeignServer(p)
			return err
		}},
		{"DropForeignServer", []byte{RecordKindDropForeignServer, 10, 0}, func(p []byte) error {
			_, err := DecodeDropForeignServer(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
