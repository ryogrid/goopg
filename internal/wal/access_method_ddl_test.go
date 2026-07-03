package wal

import "testing"

// TestEncodeDecodeCreateAccessMethodRoundTrip pins the DU-002
// restart-persistence (M0119-0004, DU-002 slice 426 ledger resume point)
// CREATE ACCESS METHOD WAL record format. Encode → Decode must return the
// original name/amType/OID/handlerOID.
func TestEncodeDecodeCreateAccessMethodRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		amType     string
		oid        uint32
		handlerOID uint32
	}{
		{"myam", "i", 16384, 16390},
		{"tableam", "t", 16385, 16391},
		{"日本語アクセスメソッド", "i", 4294967295, 16392}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateAccessMethod(c.name, c.amType, c.oid, c.handlerOID)
		if raw[0] != RecordKindCreateAccessMethod {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateAccessMethod)
			continue
		}
		gotName, gotAMType, gotOID, gotHandlerOID, err := DecodeCreateAccessMethod(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotAMType != c.amType || gotOID != c.oid || gotHandlerOID != c.handlerOID {
			t.Errorf("%q: decoded (%q, %q, %d, %d), want (%q, %q, %d, %d)",
				c.name, gotName, gotAMType, gotOID, gotHandlerOID, c.name, c.amType, c.oid, c.handlerOID)
		}
	}
}

// TestEncodeDecodeDropAccessMethodRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropAccessMethodRoundTrip(t *testing.T) {
	for _, name := range []string{"myam", "日本語アクセスメソッド"} {
		raw := EncodeDropAccessMethod(name)
		if raw[0] != RecordKindDropAccessMethod {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropAccessMethod)
			continue
		}
		got, err := DecodeDropAccessMethod(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestDecodeAccessMethodRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record, for both access method record kinds.
func TestDecodeAccessMethodRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropAccessMethod("x")

	if _, _, _, _, err := DecodeCreateAccessMethod(bogus); err == nil {
		t.Error("DecodeCreateAccessMethod: expected error on wrong kind")
	}
	if _, err := DecodeDropAccessMethod(EncodeCreateAccessMethod("x", "i", 1, 20)); err == nil {
		t.Error("DecodeDropAccessMethod: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateAccessMethod", []byte{RecordKindCreateAccessMethod, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, err := DecodeCreateAccessMethod(p)
			return err
		}},
		{"DropAccessMethod", []byte{RecordKindDropAccessMethod, 10, 0}, func(p []byte) error {
			_, err := DecodeDropAccessMethod(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
