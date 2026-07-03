package wal

import "testing"

// TestEncodeDecodeCreateOperatorFamilyRoundTrip pins the DU-002
// restart-persistence (M0119-0004/M0110-0001, closing the loop #65/#66
// deferral ledger row's "still open" item (1)) CREATE OPERATOR FAMILY WAL
// record format. Encode → Decode must return every field unchanged.
func TestEncodeDecodeCreateOperatorFamilyRoundTrip(t *testing.T) {
	cases := []CreateOperatorFamilyPayload{
		{OID: 16384, Schema: "public", Name: "myintops", Method: 403},
		{OID: 4294967295, Schema: "日本語スキーマ", Name: "f", Method: 405},
	}
	for _, c := range cases {
		raw := EncodeCreateOperatorFamily(c)
		if raw[0] != RecordKindCreateOperatorFamily {
			t.Errorf("%q: kind byte = %d, want %d", c.Name, raw[0], RecordKindCreateOperatorFamily)
			continue
		}
		got, err := DecodeCreateOperatorFamily(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.Name, err)
			continue
		}
		if got != c {
			t.Errorf("%q: decoded %+v, want %+v", c.Name, got, c)
		}
	}
}

// TestEncodeDecodeCreateOperatorClassRoundTrip is the CREATE OPERATOR CLASS
// counterpart.
func TestEncodeDecodeCreateOperatorClassRoundTrip(t *testing.T) {
	cases := []CreateOperatorClassPayload{
		{OID: 16400, Schema: "public", Name: "myintops", Method: 403, FamilyOID: 16384, InTypeOID: 23, KeyTypeOID: 0, IsDefault: true},
		{OID: 1, Schema: "s", Name: "c", Method: 405, FamilyOID: 2, InTypeOID: 25, KeyTypeOID: 25, IsDefault: false},
	}
	for _, c := range cases {
		raw := EncodeCreateOperatorClass(c)
		if raw[0] != RecordKindCreateOperatorClass {
			t.Errorf("%q: kind byte = %d, want %d", c.Name, raw[0], RecordKindCreateOperatorClass)
			continue
		}
		got, err := DecodeCreateOperatorClass(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.Name, err)
			continue
		}
		if got != c {
			t.Errorf("%q: decoded %+v, want %+v", c.Name, got, c)
		}
	}
}

// TestEncodeDecodeDropOperatorClassRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropOperatorClassRoundTrip(t *testing.T) {
	for _, oid := range []uint32{1, 16384, 4294967295} {
		raw := EncodeDropOperatorClass(oid)
		if raw[0] != RecordKindDropOperatorClass {
			t.Errorf("oid %d: kind byte = %d, want %d", oid, raw[0], RecordKindDropOperatorClass)
			continue
		}
		got, err := DecodeDropOperatorClass(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", oid, err)
			continue
		}
		if got != oid {
			t.Errorf("decoded %d, want %d", got, oid)
		}
	}
}

// TestEncodeDecodeDropOperatorFamilyRoundTrip pins the DROP OPERATOR FAMILY
// WAL record format (closes the loop #69 ledger row's "DROP OPERATOR FAMILY
// never actually calls DropUserOperatorFamily" discovery).
func TestEncodeDecodeDropOperatorFamilyRoundTrip(t *testing.T) {
	for _, oid := range []uint32{1, 16384, 4294967295} {
		raw := EncodeDropOperatorFamily(oid)
		if raw[0] != RecordKindDropOperatorFamily {
			t.Errorf("oid %d: kind byte = %d, want %d", oid, raw[0], RecordKindDropOperatorFamily)
			continue
		}
		got, err := DecodeDropOperatorFamily(raw)
		if err != nil {
			t.Errorf("oid %d: decode err: %v", oid, err)
			continue
		}
		if got != oid {
			t.Errorf("decoded %d, want %d", got, oid)
		}
	}
}

// TestEncodeDecodeAmOpMemberRoundTrip pins the pg_amop-row WAL record shape
// (covers both a CREATE OPERATOR CLASS ... AS list entry and an ALTER
// OPERATOR FAMILY ... ADD entry).
func TestEncodeDecodeAmOpMemberRoundTrip(t *testing.T) {
	cases := []AmOpMemberPayload{
		{OID: 16401, FamilyOID: 16384, ClassOID: 16400, LeftType: 23, RightType: 23, Strategy: 1, OperOID: 20000, Method: 403, SortFamilyOID: 0},
		{OID: 1, FamilyOID: 2, ClassOID: 0, LeftType: 25, RightType: 25, Strategy: 27, OperOID: 4, Method: 783, SortFamilyOID: 1979},
	}
	for _, c := range cases {
		raw := EncodeCreateAmOpMember(c)
		if raw[0] != RecordKindCreateAmOpMember {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindCreateAmOpMember)
			continue
		}
		got, err := DecodeCreateAmOpMember(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if got != c {
			t.Errorf("decoded %+v, want %+v", got, c)
		}
	}
}

// TestEncodeDecodeDropAmOpMemberRoundTrip is the ALTER OPERATOR FAMILY ...
// DROP OPERATOR counterpart.
func TestEncodeDecodeDropAmOpMemberRoundTrip(t *testing.T) {
	raw := EncodeDropAmOpMember(16384, 23, 23, 1)
	if raw[0] != RecordKindDropAmOpMember {
		t.Fatalf("kind byte = %d, want %d", raw[0], RecordKindDropAmOpMember)
	}
	fam, left, right, strat, err := DecodeDropAmOpMember(raw)
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if fam != 16384 || left != 23 || right != 23 || strat != 1 {
		t.Errorf("decoded (%d,%d,%d,%d), want (16384,23,23,1)", fam, left, right, strat)
	}
}

// TestEncodeDecodeAmProcMemberRoundTrip pins the pg_amproc-row WAL record
// shape. Mirrors TestEncodeDecodeAmOpMemberRoundTrip.
func TestEncodeDecodeAmProcMemberRoundTrip(t *testing.T) {
	cases := []AmProcMemberPayload{
		{OID: 16402, FamilyOID: 16384, ClassOID: 16400, LeftType: 23, RightType: 23, ProcNum: 1, ProcOID: 20001, Method: 403},
		{OID: 1, FamilyOID: 2, ClassOID: 0, LeftType: 25, RightType: 25, ProcNum: 3, ProcOID: 4, Method: 783},
	}
	for _, c := range cases {
		raw := EncodeCreateAmProcMember(c)
		if raw[0] != RecordKindCreateAmProcMember {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindCreateAmProcMember)
			continue
		}
		got, err := DecodeCreateAmProcMember(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if got != c {
			t.Errorf("decoded %+v, want %+v", got, c)
		}
	}
}

// TestEncodeDecodeDropAmProcMemberRoundTrip is the ALTER OPERATOR FAMILY ...
// DROP FUNCTION counterpart.
func TestEncodeDecodeDropAmProcMemberRoundTrip(t *testing.T) {
	raw := EncodeDropAmProcMember(16384, 23, 23, 2)
	if raw[0] != RecordKindDropAmProcMember {
		t.Fatalf("kind byte = %d, want %d", raw[0], RecordKindDropAmProcMember)
	}
	fam, left, right, procNum, err := DecodeDropAmProcMember(raw)
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if fam != 16384 || left != 23 || right != 23 || procNum != 2 {
		t.Errorf("decoded (%d,%d,%d,%d), want (16384,23,23,2)", fam, left, right, procNum)
	}
}

// TestDecodeOperatorClassRejectsWrongKindAndTruncatedPayload guards the new
// decoders against a mismatched kind byte and a corrupt/truncated on-disk
// record.
func TestDecodeOperatorClassRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	if _, err := DecodeCreateOperatorFamily(EncodeDropOperatorClass(1)); err == nil {
		t.Error("DecodeCreateOperatorFamily: expected error on wrong kind")
	}
	if _, err := DecodeCreateOperatorClass(EncodeCreateOperatorFamily(CreateOperatorFamilyPayload{Name: "x"})); err == nil {
		t.Error("DecodeCreateOperatorClass: expected error on wrong kind")
	}
	if _, err := DecodeDropOperatorClass(EncodeCreateOperatorFamily(CreateOperatorFamilyPayload{Name: "x"})); err == nil {
		t.Error("DecodeDropOperatorClass: expected error on wrong kind")
	}
	if _, err := DecodeDropOperatorFamily(EncodeDropOperatorClass(1)); err == nil {
		t.Error("DecodeDropOperatorFamily: expected error on wrong kind")
	}
	if _, err := DecodeCreateAmOpMember(EncodeCreateAmProcMember(AmProcMemberPayload{})); err == nil {
		t.Error("DecodeCreateAmOpMember: expected error on wrong kind")
	}
	if _, _, _, _, err := DecodeDropAmOpMember(EncodeDropAmProcMember(1, 2, 3, 4)); err == nil {
		t.Error("DecodeDropAmOpMember: expected error on wrong kind")
	}
	if _, err := DecodeCreateAmProcMember(EncodeCreateAmOpMember(AmOpMemberPayload{})); err == nil {
		t.Error("DecodeCreateAmProcMember: expected error on wrong kind")
	}
	if _, _, _, _, err := DecodeDropAmProcMember(EncodeDropAmOpMember(1, 2, 3, 4)); err == nil {
		t.Error("DecodeDropAmProcMember: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreateOperatorFamily", []byte{RecordKindCreateOperatorFamily, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateOperatorFamily(p)
			return err
		}},
		{"CreateOperatorClass", []byte{RecordKindCreateOperatorClass, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateOperatorClass(p)
			return err
		}},
		{"DropOperatorClass", []byte{RecordKindDropOperatorClass, 10, 0}, func(p []byte) error {
			_, err := DecodeDropOperatorClass(p)
			return err
		}},
		{"DropOperatorFamily", []byte{RecordKindDropOperatorFamily, 10, 0}, func(p []byte) error {
			_, err := DecodeDropOperatorFamily(p)
			return err
		}},
		{"CreateAmOpMember", []byte{RecordKindCreateAmOpMember, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateAmOpMember(p)
			return err
		}},
		{"DropAmOpMember", []byte{RecordKindDropAmOpMember, 0, 0}, func(p []byte) error {
			_, _, _, _, err := DecodeDropAmOpMember(p)
			return err
		}},
		{"CreateAmProcMember", []byte{RecordKindCreateAmProcMember, 0, 0, 0, 0}, func(p []byte) error {
			_, err := DecodeCreateAmProcMember(p)
			return err
		}},
		{"DropAmProcMember", []byte{RecordKindDropAmProcMember, 0, 0}, func(p []byte) error {
			_, _, _, _, err := DecodeDropAmProcMember(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
