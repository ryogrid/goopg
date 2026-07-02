package catalog

import "testing"

// TestLookupBuiltinOperator pins the curated built-in pg_operator.dat set
// (builtinOperatorsByKey) — the 5 int8 btree comparison strategies the
// upstream `op_class` pg_dump fixture's OPERATOR entries need. Type-name
// synonyms ("bigint" vs "int8") must resolve to the same entry since
// TypeNameToOID canonicalizes both before keying. M0119-0004 (DU-002)
// slice 413.
func TestLookupBuiltinOperator(t *testing.T) {
	tests := []struct {
		name, left, right string
		wantOID           uint32
		wantOK            bool
	}{
		{"<", "int8", "int8", 412, true},
		{"<=", "int8", "int8", 414, true},
		{"=", "int8", "int8", 410, true},
		{">=", "int8", "int8", 415, true},
		{">", "int8", "int8", 413, true},
		{"<", "bigint", "bigint", 412, true}, // synonym spelling
		{"<", "int4", "int4", 0, false},      // not curated
		{"~=~", "int8", "int8", 0, false},    // unknown name
	}
	for _, tt := range tests {
		got, ok := LookupBuiltinOperator(tt.name, tt.left, tt.right)
		if ok != tt.wantOK || got.OID != tt.wantOID {
			t.Errorf("LookupBuiltinOperator(%q,%q,%q) = (%+v,%v), want OID=%d ok=%v",
				tt.name, tt.left, tt.right, got, ok, tt.wantOID, tt.wantOK)
		}
	}
}

// TestLookupBuiltinOperatorByOID pins the OID→row reverse direction
// regoper/regoperator rendering needs.
func TestLookupBuiltinOperatorByOID(t *testing.T) {
	op, ok := LookupBuiltinOperatorByOID(412)
	if !ok || op.Name != "<" || op.LeftType != "int8" || op.RightType != "int8" {
		t.Errorf("LookupBuiltinOperatorByOID(412) = (%+v,%v), want name=< left=int8 right=int8 ok=true", op, ok)
	}
	if _, ok := LookupBuiltinOperatorByOID(999999999); ok {
		t.Errorf("LookupBuiltinOperatorByOID(999999999) = ok, want not found")
	}
}

// TestRegoperatorNameAndSchemaBuiltinFallback verifies a builtin operator OID
// (not present in the user-operator registry) resolves via
// RegoperatorNameAndSchema's new builtin fallback, schema always
// "pg_catalog". M0119-0004 (DU-002) slice 413.
func TestRegoperatorNameAndSchemaBuiltinFallback(t *testing.T) {
	c := NewInMemory()
	schema, sig, ok := c.RegoperatorNameAndSchema(412)
	if !ok || schema != "pg_catalog" || sig != "<(bigint,bigint)" {
		t.Errorf("RegoperatorNameAndSchema(412) = (%q,%q,%v), want (pg_catalog,\"<(bigint,bigint)\",true)", schema, sig, ok)
	}
	if _, _, ok := c.RegoperatorNameAndSchema(999999999); ok {
		t.Errorf("RegoperatorNameAndSchema(999999999) = ok, want not found")
	}
}
