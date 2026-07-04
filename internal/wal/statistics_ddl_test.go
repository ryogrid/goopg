package wal

import "testing"

// TestEncodeDecodeCreateStatisticsRoundTrip pins the DU-002
// restart-persistence follow-up (slice 441's own resume point) encode/decode
// contract for CREATE STATISTICS, including the three variable-length string
// slices (Kinds/Columns/Exprs) and the HasExpr flag.
func TestEncodeDecodeCreateStatisticsRoundTrip(t *testing.T) {
	cases := []struct {
		name, schema                string
		oid, tableOID, ownerOID     uint32
		kinds, columns, exprs       []string
		hasExpr                     bool
	}{
		{"s1", "public", 40900, 16400, 10, nil, []string{"a", "b"}, nil, false},
		{"s2", "myschema", 40901, 16401, 16384, []string{"ndistinct", "dependencies"}, []string{"a"}, []string{"(a + b)"}, true},
		{"s3", "public", 40902, 16402, 0, nil, nil, nil, false},
	}
	for _, c := range cases {
		raw := EncodeCreateStatistics(c.name, c.schema, c.oid, c.tableOID, c.ownerOID, c.kinds, c.columns, c.exprs, c.hasExpr)
		if raw[0] != RecordKindCreateStatistics {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateStatistics)
		}
		gotName, gotSchema, gotOID, gotTableOID, gotOwnerOID, gotKinds, gotColumns, gotExprs, gotHasExpr, err := DecodeCreateStatistics(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotSchema != c.schema || gotOID != c.oid || gotTableOID != c.tableOID || gotOwnerOID != c.ownerOID || gotHasExpr != c.hasExpr {
			t.Errorf("%q: got (name=%q schema=%q oid=%d tableOID=%d ownerOID=%d hasExpr=%v)", c.name, gotName, gotSchema, gotOID, gotTableOID, gotOwnerOID, gotHasExpr)
		}
		if !stringSlicesEqual(gotKinds, c.kinds) {
			t.Errorf("%q: kinds = %v, want %v", c.name, gotKinds, c.kinds)
		}
		if !stringSlicesEqual(gotColumns, c.columns) {
			t.Errorf("%q: columns = %v, want %v", c.name, gotColumns, c.columns)
		}
		if !stringSlicesEqual(gotExprs, c.exprs) {
			t.Errorf("%q: exprs = %v, want %v", c.name, gotExprs, c.exprs)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEncodeDecodeDropStatisticsRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropStatisticsRoundTrip(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"s1", "public"},
		{"s2", "myschema"},
		{"", ""},
	}
	for _, c := range cases {
		raw := EncodeDropStatistics(c.name, c.schema)
		if raw[0] != RecordKindDropStatistics {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindDropStatistics)
		}
		gotName, gotSchema, err := DecodeDropStatistics(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotSchema != c.schema {
			t.Errorf("%q: got (name=%q schema=%q)", c.name, gotName, gotSchema)
		}
	}
}

// TestDecodeCreateStatisticsRejectsWrongKind confirms the decoder surfaces an
// error rather than silently misinterpreting a differently-kinded payload.
func TestDecodeCreateStatisticsRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropStatistics("s", "public")
	if _, _, _, _, _, _, _, _, _, err := DecodeCreateStatistics(bogus); err == nil {
		t.Error("expected error decoding drop-statistics payload as create-statistics, got nil")
	}
}

// TestDecodeCreateStatisticsRejectsTruncatedPayload guards against silently
// reading past the end of a truncated payload.
func TestDecodeCreateStatisticsRejectsTruncatedPayload(t *testing.T) {
	full := EncodeCreateStatistics("s", "public", 1, 2, 3, []string{"ndistinct"}, []string{"a", "b"}, nil, false)
	for _, n := range []int{0, 1, 14, len(full) - 1} {
		truncated := full[:n]
		if _, _, _, _, _, _, _, _, _, err := DecodeCreateStatistics(truncated); err == nil {
			t.Errorf("truncated to %d bytes: expected error, got nil", n)
		}
	}
}

// TestDecodeDropStatisticsRejectsTruncatedPayload mirrors the create case for
// DROP STATISTICS.
func TestDecodeDropStatisticsRejectsTruncatedPayload(t *testing.T) {
	truncated := []byte{RecordKindDropStatistics, 10, 0}
	if _, _, err := DecodeDropStatistics(truncated); err == nil {
		t.Error("expected error decoding truncated drop-statistics payload, got nil")
	}
}

// TestEncodeDecodeAlterStatisticsRenameRoundTrip pins the resume-point-(1)
// follow-up (slice 441/445 ledger rows): ALTER STATISTICS ... RENAME TO.
func TestEncodeDecodeAlterStatisticsRenameRoundTrip(t *testing.T) {
	cases := []struct{ name, newName string }{
		{"public.s1", "s1_renamed"},
		{"s2", "s2_new"},
		{"", ""},
	}
	for _, c := range cases {
		raw := EncodeAlterStatisticsRename(c.name, c.newName)
		if raw[0] != RecordKindAlterStatisticsRename {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterStatisticsRename)
		}
		gotName, gotNewName, err := DecodeAlterStatisticsRename(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotNewName != c.newName {
			t.Errorf("%q: got (name=%q newName=%q)", c.name, gotName, gotNewName)
		}
	}
}

// TestEncodeDecodeAlterStatisticsOwnerRoundTrip is the OWNER TO counterpart.
func TestEncodeDecodeAlterStatisticsOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"public.s1", 16384},
		{"s2", 10},
		{"", 0},
	}
	for _, c := range cases {
		raw := EncodeAlterStatisticsOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterStatisticsOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterStatisticsOwner)
		}
		gotName, gotOwnerOID, err := DecodeAlterStatisticsOwner(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: got (name=%q ownerOID=%d)", c.name, gotName, gotOwnerOID)
		}
	}
}

// TestEncodeDecodeAlterStatisticsSetSchemaRoundTrip is the SET SCHEMA
// counterpart.
func TestEncodeDecodeAlterStatisticsSetSchemaRoundTrip(t *testing.T) {
	cases := []struct{ name, newSchema string }{
		{"public.s1", "myschema"},
		{"s2", "public"},
		{"", ""},
	}
	for _, c := range cases {
		raw := EncodeAlterStatisticsSetSchema(c.name, c.newSchema)
		if raw[0] != RecordKindAlterStatisticsSetSchema {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterStatisticsSetSchema)
		}
		gotName, gotNewSchema, err := DecodeAlterStatisticsSetSchema(raw)
		if err != nil {
			t.Fatalf("%q: Decode: %v", c.name, err)
		}
		if gotName != c.name || gotNewSchema != c.newSchema {
			t.Errorf("%q: got (name=%q newSchema=%q)", c.name, gotName, gotNewSchema)
		}
	}
}

// TestDecodeAlterStatisticsRejectsWrongKind confirms each new decoder
// surfaces an error rather than silently misinterpreting a
// differently-kinded payload.
func TestDecodeAlterStatisticsRejectsWrongKind(t *testing.T) {
	bogus := EncodeDropStatistics("s", "public")
	if _, _, err := DecodeAlterStatisticsRename(bogus); err == nil {
		t.Error("DecodeAlterStatisticsRename: expected error decoding drop-statistics payload, got nil")
	}
	if _, _, err := DecodeAlterStatisticsOwner(bogus); err == nil {
		t.Error("DecodeAlterStatisticsOwner: expected error decoding drop-statistics payload, got nil")
	}
	if _, _, err := DecodeAlterStatisticsSetSchema(bogus); err == nil {
		t.Error("DecodeAlterStatisticsSetSchema: expected error decoding drop-statistics payload, got nil")
	}
}

// TestDecodeAlterStatisticsRejectsTruncatedPayload guards against silently
// reading past the end of a truncated payload for each new decoder.
func TestDecodeAlterStatisticsRejectsTruncatedPayload(t *testing.T) {
	renameFull := EncodeAlterStatisticsRename("public.s1", "s1_new")
	for _, n := range []int{0, 1, 4, len(renameFull) - 1} {
		if _, _, err := DecodeAlterStatisticsRename(renameFull[:n]); err == nil {
			t.Errorf("rename truncated to %d bytes: expected error, got nil", n)
		}
	}
	ownerFull := EncodeAlterStatisticsOwner("public.s1", 16384)
	for _, n := range []int{0, 1, 6, len(ownerFull) - 1} {
		if _, _, err := DecodeAlterStatisticsOwner(ownerFull[:n]); err == nil {
			t.Errorf("owner truncated to %d bytes: expected error, got nil", n)
		}
	}
	setSchemaFull := EncodeAlterStatisticsSetSchema("public.s1", "myschema")
	for _, n := range []int{0, 1, 4, len(setSchemaFull) - 1} {
		if _, _, err := DecodeAlterStatisticsSetSchema(setSchemaFull[:n]); err == nil {
			t.Errorf("set-schema truncated to %d bytes: expected error, got nil", n)
		}
	}
}
