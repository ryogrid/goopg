package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreateFunctionRoundTrip pins the DU-002
// restart-persistence (M0119-0004, loop #71 ledger resume point) CREATE
// FUNCTION/PROCEDURE WAL record format. Encode → Decode must return the
// original payload verbatim.
func TestEncodeDecodeCreateFunctionRoundTrip(t *testing.T) {
	cases := []CreateFunctionPayload{
		{
			OID: 16384, Schema: "public", Name: "myfunc",
			Args: []FunctionArgPayload{
				{Name: "a", TypeName: "integer", Mode: "i"},
				{Name: "b", TypeName: "numeric", TypeArgs: []int64{10, 2}, Mode: "i", Default: "0"},
			},
			ReturnTypeName: "integer",
			Language:       "sql", Body: "select $1 + $2",
			Volatile: "i", Parallel: "s", Cost: "1", Rows: "0",
			KindChar: "f",
		},
		{
			// Empty args/return, plpgsql, all boolean flags set.
			OID: 16385, Schema: "app", Name: "noargs",
			ReturnTypeName: "void",
			Language:       "plpgsql", Body: "BEGIN NULL; END;",
			ReturnsSet: true, ReturnsTable: true, Strict: true,
			SecurityDefiner: true, Leakproof: true, IsProcedure: false,
			IsWindow: false, BeginAtomic: false, IsReturnForm: false,
			Volatile: "v", KindChar: "f",
		},
		{
			// Procedure with OUT/INOUT/VARIADIC args and an array return
			// type modifier, mirroring what execCreateProcedure produces.
			OID: 16386, Schema: "public", Name: "myproc",
			Args: []FunctionArgPayload{
				{Name: "x", TypeName: "integer[]", Mode: "v"},
				{Name: "y", TypeName: "text", Mode: "o"},
				{Name: "z", TypeName: "text", Mode: "b"},
			},
			ReturnTypeName: "",
			Language:       "sql", Body: "CALL other()",
			IsProcedure: true, BeginAtomic: true, Volatile: "v",
			KindChar: "p",
		},
		{
			// Multi-byte UTF-8, max OID, empty Args slice (nil vs empty
			// round-trips as empty either way since Decode always
			// allocates a len-0 slice).
			OID: 4294967295, Schema: "日本語スキーマ", Name: "関数",
			ReturnTypeName: "text",
			Language:       "sql", Body: "select '日本語'",
			Volatile: "s", KindChar: "f",
		},
	}
	for i, c := range cases {
		raw := EncodeCreateFunction(c)
		if raw[0] != RecordKindCreateFunction {
			t.Errorf("case %d: kind byte = %d, want %d", i, raw[0], RecordKindCreateFunction)
			continue
		}
		got, err := DecodeCreateFunction(raw)
		if err != nil {
			t.Errorf("case %d: decode err: %v", i, err)
			continue
		}
		if got.Args == nil {
			got.Args = []FunctionArgPayload{}
		}
		want := c
		if want.Args == nil {
			want.Args = []FunctionArgPayload{}
		}
		for j := range want.Args {
			if want.Args[j].TypeArgs == nil {
				want.Args[j].TypeArgs = []int64{}
			}
		}
		for j := range got.Args {
			if got.Args[j].TypeArgs == nil {
				got.Args[j].TypeArgs = []int64{}
			}
		}
		if want.ReturnTypeArgs == nil {
			want.ReturnTypeArgs = []int64{}
		}
		if got.ReturnTypeArgs == nil {
			got.ReturnTypeArgs = []int64{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("case %d: decoded payload mismatch:\n got  %+v\n want %+v", i, got, want)
		}
	}
}

// TestEncodeDecodeDropFunctionRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropFunctionRoundTrip(t *testing.T) {
	for _, oid := range []uint32{0, 16384, 4294967295} {
		raw := EncodeDropFunction(oid)
		if raw[0] != RecordKindDropFunction {
			t.Errorf("oid %d: kind byte = %d, want %d", oid, raw[0], RecordKindDropFunction)
			continue
		}
		got, err := DecodeDropFunction(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", oid, err)
			continue
		}
		if got != oid {
			t.Errorf("decoded oid %d, want %d", got, oid)
		}
	}
}

// TestEncodeDecodeAlterFunctionRenameRoundTrip is the RENAME TO
// counterpart.
func TestEncodeDecodeAlterFunctionRenameRoundTrip(t *testing.T) {
	cases := []struct {
		oid     uint32
		newName string
	}{
		{16384, "renamed"},
		{4294967295, "新しい名前"},
	}
	for _, c := range cases {
		raw := EncodeAlterFunctionRename(c.oid, c.newName)
		if raw[0] != RecordKindAlterFunctionRename {
			t.Errorf("oid %d: kind byte = %d, want %d", c.oid, raw[0], RecordKindAlterFunctionRename)
			continue
		}
		gotOID, gotNewName, err := DecodeAlterFunctionRename(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", c.oid, err)
			continue
		}
		if gotOID != c.oid || gotNewName != c.newName {
			t.Errorf("oid %d: decoded (%d, %q)", c.oid, gotOID, gotNewName)
		}
	}
}

// TestEncodeDecodeAlterFunctionFlagsRoundTrip is the
// VOLATILE/SECURITY/LEAKPROOF/STRICT attribute-snapshot counterpart.
func TestEncodeDecodeAlterFunctionFlagsRoundTrip(t *testing.T) {
	cases := []struct {
		oid                                uint32
		volatile                           string
		securityDefiner, leakproof, strict bool
	}{
		{16384, "v", false, false, false},
		{16385, "i", true, true, true},
		{16386, "s", true, false, true},
	}
	for _, c := range cases {
		raw := EncodeAlterFunctionFlags(c.oid, c.volatile, c.securityDefiner, c.leakproof, c.strict)
		if raw[0] != RecordKindAlterFunctionFlags {
			t.Errorf("oid %d: kind byte = %d, want %d", c.oid, raw[0], RecordKindAlterFunctionFlags)
			continue
		}
		gotOID, gotVolatile, gotSD, gotLP, gotStrict, err := DecodeAlterFunctionFlags(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", c.oid, err)
			continue
		}
		if gotOID != c.oid || gotVolatile != c.volatile || gotSD != c.securityDefiner ||
			gotLP != c.leakproof || gotStrict != c.strict {
			t.Errorf("oid %d: decoded (%d, %q, %v, %v, %v)", c.oid, gotOID, gotVolatile, gotSD, gotLP, gotStrict)
		}
	}
}

// TestEncodeDecodeAlterFunctionOwnerRoundTrip is the OWNER TO counterpart
// (M0097-0150).
func TestEncodeDecodeAlterFunctionOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		oid, ownerOID uint32
	}{
		{16384, 10},
		{16385, 4294967295},
		{4294967295, 0},
	}
	for _, c := range cases {
		raw := EncodeAlterFunctionOwner(c.oid, c.ownerOID)
		if raw[0] != RecordKindAlterFunctionOwner {
			t.Errorf("oid %d: kind byte = %d, want %d", c.oid, raw[0], RecordKindAlterFunctionOwner)
			continue
		}
		gotOID, gotOwnerOID, err := DecodeAlterFunctionOwner(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", c.oid, err)
			continue
		}
		if gotOID != c.oid || gotOwnerOID != c.ownerOID {
			t.Errorf("oid %d: decoded (%d, %d)", c.oid, gotOID, gotOwnerOID)
		}
	}
}

// TestEncodeDecodeAlterFunctionSetSchemaRoundTrip is the SET SCHEMA
// counterpart (M0097-0150).
func TestEncodeDecodeAlterFunctionSetSchemaRoundTrip(t *testing.T) {
	cases := []struct {
		oid       uint32
		newSchema string
	}{
		{16384, "app"},
		{4294967295, "日本語スキーマ"},
	}
	for _, c := range cases {
		raw := EncodeAlterFunctionSetSchema(c.oid, c.newSchema)
		if raw[0] != RecordKindAlterFunctionSetSchema {
			t.Errorf("oid %d: kind byte = %d, want %d", c.oid, raw[0], RecordKindAlterFunctionSetSchema)
			continue
		}
		gotOID, gotNewSchema, err := DecodeAlterFunctionSetSchema(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", c.oid, err)
			continue
		}
		if gotOID != c.oid || gotNewSchema != c.newSchema {
			t.Errorf("oid %d: decoded (%d, %q)", c.oid, gotOID, gotNewSchema)
		}
	}
}

// TestDecodeFunctionRejectsWrongKindAndTruncatedPayload guards the
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record, for every function record kind.
func TestDecodeFunctionRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropFunction(1)

	if _, err := DecodeCreateFunction(bogus); err == nil {
		t.Error("DecodeCreateFunction: expected error on wrong kind")
	}
	if _, err := DecodeDropFunction(EncodeCreateFunction(CreateFunctionPayload{OID: 1, Name: "x", KindChar: "f"})); err == nil {
		t.Error("DecodeDropFunction: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterFunctionRename(bogus); err == nil {
		t.Error("DecodeAlterFunctionRename: expected error on wrong kind")
	}
	if _, _, _, _, _, err := DecodeAlterFunctionFlags(bogus); err == nil {
		t.Error("DecodeAlterFunctionFlags: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterFunctionOwner(bogus); err == nil {
		t.Error("DecodeAlterFunctionOwner: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterFunctionSetSchema(bogus); err == nil {
		t.Error("DecodeAlterFunctionSetSchema: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateFunction", []byte{RecordKindCreateFunction, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateFunction(p)
			return err
		}},
		{"DropFunction", []byte{RecordKindDropFunction, 10, 0}, func(p []byte) error {
			_, err := DecodeDropFunction(p)
			return err
		}},
		{"AlterFunctionRename", []byte{RecordKindAlterFunctionRename, 10, 0}, func(p []byte) error {
			_, _, err := DecodeAlterFunctionRename(p)
			return err
		}},
		{"AlterFunctionFlags", []byte{RecordKindAlterFunctionFlags, 0, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, err := DecodeAlterFunctionFlags(p)
			return err
		}},
		{"AlterFunctionOwner", []byte{RecordKindAlterFunctionOwner, 0, 0, 0, 0}, func(p []byte) error {
			_, _, err := DecodeAlterFunctionOwner(p)
			return err
		}},
		{"AlterFunctionSetSchema", []byte{RecordKindAlterFunctionSetSchema, 10, 0}, func(p []byte) error {
			_, _, err := DecodeAlterFunctionSetSchema(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
