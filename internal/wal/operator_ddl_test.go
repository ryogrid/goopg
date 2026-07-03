package wal

import "testing"

// TestEncodeDecodeCreateOperatorRoundTrip pins the DU-002
// restart-persistence (M0119-0004/M0110-0001, discovered while verifying
// the loop #64 CREATE TYPE ... AS RANGE opclass/collation follow-up — see
// ledger) CREATE OPERATOR WAL record format. Encode → Decode must return
// every field of CreateOperatorPayload unchanged.
func TestEncodeDecodeCreateOperatorRoundTrip(t *testing.T) {
	cases := []CreateOperatorPayload{
		{OID: 16384, Schema: "public", Name: "===", LeftType: "int4", RightType: "int4", FuncOID: 20000, Owner: 10},
		{
			OID: 16385, Schema: "myschema", Name: "!!!", LeftType: "text", RightType: "text",
			FuncOID: 20001, Owner: 16386, CommutatorOID: 16385, NegatorOID: 16387,
			RestrictOID: 20002, JoinOID: 20003, CanMerge: true, CanHash: true,
		},
		{OID: 4294967295, Schema: "s", Name: "~~", LeftType: "", RightType: "int4", FuncOID: 1, Owner: 1},
		{OID: 1, Schema: "日本語スキーマ", Name: "<=>", LeftType: "int8", RightType: "int8", FuncOID: 2, CanMerge: true},
	}
	for _, c := range cases {
		raw := EncodeCreateOperator(c)
		if raw[0] != RecordKindCreateOperator {
			t.Errorf("%q: kind byte = %d, want %d", c.Name, raw[0], RecordKindCreateOperator)
			continue
		}
		got, err := DecodeCreateOperator(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.Name, err)
			continue
		}
		if got != c {
			t.Errorf("%q: decoded %+v, want %+v", c.Name, got, c)
		}
	}
}

// TestEncodeDecodeDropOperatorRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropOperatorRoundTrip(t *testing.T) {
	for _, oid := range []uint32{1, 16384, 4294967295} {
		raw := EncodeDropOperator(oid)
		if raw[0] != RecordKindDropOperator {
			t.Errorf("oid %d: kind byte = %d, want %d", oid, raw[0], RecordKindDropOperator)
			continue
		}
		got, err := DecodeDropOperator(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", oid, err)
			continue
		}
		if got != oid {
			t.Errorf("decoded %d, want %d", got, oid)
		}
	}
}

// TestDecodeOperatorRejectsWrongKindAndTruncatedPayload guards the decoders
// against a mismatched kind byte and a corrupt/truncated on-disk record, for
// both operator record kinds.
func TestDecodeOperatorRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropOperator(1)

	if _, err := DecodeCreateOperator(bogus); err == nil {
		t.Error("DecodeCreateOperator: expected error on wrong kind")
	}
	if _, err := DecodeDropOperator(EncodeCreateOperator(CreateOperatorPayload{OID: 1, Name: "x"})); err == nil {
		t.Error("DecodeDropOperator: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateOperator", []byte{RecordKindCreateOperator, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateOperator(p)
			return err
		}},
		{"CreateOperatorTruncatedString", append([]byte{RecordKindCreateOperator}, append(make([]byte, 29), 5, 0)...), func(p []byte) error {
			// header (30 bytes) + a 2-byte length prefix declaring 5 bytes of
			// schema name that are never appended.
			_, err := DecodeCreateOperator(p)
			return err
		}},
		{"DropOperator", []byte{RecordKindDropOperator, 10, 0}, func(p []byte) error {
			_, err := DecodeDropOperator(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
