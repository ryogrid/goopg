package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreateDomainRoundTrip pins the M0122-0005
// restart-persistence follow-up's CREATE DOMAIN WAL record format. Encode →
// Decode must return the original payload byte-for-byte, including every
// CHECK constraint (name/expr/OID/InValues).
func TestEncodeDecodeCreateDomainRoundTrip(t *testing.T) {
	cases := []CreateDomainPayload{
		{
			Name: "posint", OID: 16384, ArrayOID: 16385,
			BaseName: "int4", BaseOID: 0, BaseIsEnum: false,
			NotNull: true, Owner: 10, DefaultSQL: "",
		},
		{
			Name: "us_zip", OID: 16390, ArrayOID: 16391,
			BaseName: "varchar", BaseArgs: []int64{10}, BaseOID: 0,
			NotNull: false, Owner: 16400, DefaultSQL: "'00000'::character varying",
			Checks: []DomainCheckPayload{
				{OID: 16392, Name: "us_zip_check", Expr: "(length((VALUE)::text) = 5)"},
			},
		},
		{
			// enum base + multiple CHECKs including an IN-list check.
			Name: "日本語ドメイン", OID: 4294967295, ArrayOID: 4294967294,
			BaseName: "myenum", BaseOID: 20000, BaseIsEnum: true,
			NotNull: true, Owner: 4294967293, DefaultSQL: "'a'::myenum",
			Checks: []DomainCheckPayload{
				{OID: 4294967292, Name: "chk1", Expr: "(VALUE > 0)"},
				{OID: 4294967291, Name: "chk2", Expr: "(VALUE = ANY (ARRAY['a'::text, 'b'::text]))", InValues: []string{"a", "b"}},
			},
		},
	}
	for i, c := range cases {
		raw := EncodeCreateDomain(c)
		if raw[0] != RecordKindCreateDomain {
			t.Errorf("case %d: kind byte = %d, want %d", i, raw[0], RecordKindCreateDomain)
			continue
		}
		got, err := DecodeCreateDomain(raw)
		if err != nil {
			t.Errorf("case %d: decode err: %v", i, err)
			continue
		}
		if !reflect.DeepEqual(got, c) {
			t.Errorf("case %d: decoded %+v, want %+v", i, got, c)
		}
	}
}

// TestEncodeDecodeDropDomainRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropDomainRoundTrip(t *testing.T) {
	for _, name := range []string{"posint", "日本語ドメイン"} {
		raw := EncodeDropDomain(name)
		if raw[0] != RecordKindDropDomain {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropDomain)
			continue
		}
		got, err := DecodeDropDomain(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestDecodeDomainRejectsWrongKindAndTruncatedPayload guards the decoders
// against a mismatched kind byte and a corrupt/truncated on-disk record.
func TestDecodeDomainRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropDomain("x")

	if _, err := DecodeCreateDomain(bogus); err == nil {
		t.Error("DecodeCreateDomain: expected error on wrong kind")
	}
	if _, err := DecodeDropDomain(EncodeCreateDomain(CreateDomainPayload{Name: "x", BaseName: "int4"})); err == nil {
		t.Error("DecodeDropDomain: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateDomain", []byte{RecordKindCreateDomain, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateDomain(p)
			return err
		}},
		{"CreateDomainTruncatedString", append(append([]byte{RecordKindCreateDomain}, make([]byte, 16)...), 5, 0), func(p []byte) error {
			// header (18 bytes) + a 2-byte length prefix declaring 5 bytes of
			// name that are never appended.
			_, err := DecodeCreateDomain(p)
			return err
		}},
		{"DropDomain", []byte{RecordKindDropDomain, 10, 0}, func(p []byte) error {
			_, err := DecodeDropDomain(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
