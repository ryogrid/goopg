package wal

import "testing"

// TestEncodeDecodeCreateRangeTypeRoundTrip pins the DU-002
// restart-persistence (M0110-0001, DU-002 slice 429 ledger resume point,
// sub-item (c)) CREATE TYPE ... AS RANGE WAL record format. Encode → Decode
// must return the original name/subtypeName/multirangeName/OID/arrayOID/
// multirangeOID/multirangeArrayOID/opclassOID (the array OIDs were added by
// the array-type follow-up).
func TestEncodeDecodeCreateRangeTypeRoundTrip(t *testing.T) {
	cases := []struct {
		name               string
		subtypeName        string
		multirangeName     string
		oid                uint32
		arrayOID           uint32
		multirangeOID      uint32
		multirangeArrayOID uint32
		opclassOID         uint32
	}{
		{"myrange", "int4", "mymultirange", 16384, 16385, 16386, 16387, 1978},
		{"tsrange", "timestamp with time zone", "tsmultirange", 16388, 16389, 16390, 16391, 3127},
		{"日本語レンジ", "text", "日本語マルチレンジ", 4294967295, 4294967294, 4294967293, 4294967292, 3125}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateRangeType(c.name, c.subtypeName, c.multirangeName, c.oid, c.arrayOID, c.multirangeOID, c.multirangeArrayOID, c.opclassOID)
		if raw[0] != RecordKindCreateRangeType {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateRangeType)
			continue
		}
		gotName, gotSubtype, gotMR, gotOID, gotArrayOID, gotMROID, gotMRArrayOID, gotOpclass, err := DecodeCreateRangeType(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSubtype != c.subtypeName || gotMR != c.multirangeName ||
			gotOID != c.oid || gotArrayOID != c.arrayOID || gotMROID != c.multirangeOID ||
			gotMRArrayOID != c.multirangeArrayOID || gotOpclass != c.opclassOID {
			t.Errorf("%q: decoded (%q, %q, %q, %d, %d, %d, %d, %d), want (%q, %q, %q, %d, %d, %d, %d, %d)",
				c.name, gotName, gotSubtype, gotMR, gotOID, gotArrayOID, gotMROID, gotMRArrayOID, gotOpclass,
				c.name, c.subtypeName, c.multirangeName, c.oid, c.arrayOID, c.multirangeOID, c.multirangeArrayOID, c.opclassOID)
		}
	}
}

// TestEncodeDecodeDropRangeTypeRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropRangeTypeRoundTrip(t *testing.T) {
	for _, name := range []string{"myrange", "日本語レンジ"} {
		raw := EncodeDropRangeType(name)
		if raw[0] != RecordKindDropRangeType {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropRangeType)
			continue
		}
		got, err := DecodeDropRangeType(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestDecodeRangeTypeRejectsWrongKindAndTruncatedPayload guards the decoders
// against a mismatched kind byte and a corrupt/truncated on-disk record, for
// both range type record kinds.
func TestDecodeRangeTypeRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropRangeType("x")

	if _, _, _, _, _, _, _, _, err := DecodeCreateRangeType(bogus); err == nil {
		t.Error("DecodeCreateRangeType: expected error on wrong kind")
	}
	if _, err := DecodeDropRangeType(EncodeCreateRangeType("x", "int4", "xmr", 1, 2, 3, 4, 1978)); err == nil {
		t.Error("DecodeDropRangeType: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateRangeType", []byte{RecordKindCreateRangeType, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, _, _, err := DecodeCreateRangeType(p)
			return err
		}},
		{"CreateRangeTypeTruncatedString", append(append([]byte{RecordKindCreateRangeType}, make([]byte, 20)...), 5, 0), func(p []byte) error {
			// header (21 bytes) + a 2-byte length prefix declaring 5 bytes of
			// subtype name that are never appended.
			_, _, _, _, _, _, _, _, err := DecodeCreateRangeType(p)
			return err
		}},
		{"DropRangeType", []byte{RecordKindDropRangeType, 10, 0}, func(p []byte) error {
			_, err := DecodeDropRangeType(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
