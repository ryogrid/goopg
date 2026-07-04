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
