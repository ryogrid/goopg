package catalog

import "testing"

// TestLookupBuiltinProc pins the hand-curated built-in pg_proc entries
// (postgres/src/include/catalog/pg_proc.dat) that CREATE CAST/CONVERSION/
// TRANSFORM's WITH FUNCTION resolution and the SQL-queryable pg_proc view
// both read from a single source. Case-insensitive; the pg_transform
// DU-002 fixture references both by their real PG18 OIDs (3721/2406,
// also pinned by TestPgTransformVirtualRows).
func TestLookupBuiltinProc(t *testing.T) {
	tests := []struct {
		name     string
		lookup   string
		wantOID  uint32
		wantRet  string
		wantArgs []string
		wantVol  string
	}{
		{"int4recv exact", "int4recv", 2406, "int4", []string{"internal"}, "i"},
		{"int4recv upper", "INT4RECV", 2406, "int4", []string{"internal"}, "i"},
		{"prsd_lextype exact", "prsd_lextype", 3721, "internal", []string{"internal"}, "i"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := LookupBuiltinProc(tt.lookup)
			if !ok {
				t.Fatalf("LookupBuiltinProc(%q) not found", tt.lookup)
			}
			if p.OID != tt.wantOID {
				t.Errorf("OID = %d, want %d", p.OID, tt.wantOID)
			}
			if p.RetType != tt.wantRet {
				t.Errorf("RetType = %q, want %q", p.RetType, tt.wantRet)
			}
			if len(p.ArgTypes) != len(tt.wantArgs) || (len(p.ArgTypes) > 0 && p.ArgTypes[0] != tt.wantArgs[0]) {
				t.Errorf("ArgTypes = %v, want %v", p.ArgTypes, tt.wantArgs)
			}
			if p.Volatile != tt.wantVol {
				t.Errorf("Volatile = %q, want %q", p.Volatile, tt.wantVol)
			}
			if p.Namespace != 11 {
				t.Errorf("Namespace = %d, want 11 (pg_catalog)", p.Namespace)
			}
		})
	}

	if _, ok := LookupBuiltinProc("not_a_real_function"); ok {
		t.Error("LookupBuiltinProc(unknown) = found, want not found")
	}
}

// TestBuiltinProcs pins OID-ordering (callers like the pg_proc view need a
// deterministic row order) and that every entry round-trips through
// LookupBuiltinProc.
func TestBuiltinProcs(t *testing.T) {
	procs := BuiltinProcs()
	if len(procs) == 0 {
		t.Fatal("BuiltinProcs() = empty, want at least int4recv/prsd_lextype")
	}
	for i := 1; i < len(procs); i++ {
		if procs[i-1].OID >= procs[i].OID {
			t.Errorf("not OID-ordered: procs[%d].OID=%d >= procs[%d].OID=%d", i-1, procs[i-1].OID, i, procs[i].OID)
		}
	}
	for _, p := range procs {
		if got, ok := LookupBuiltinProc(p.Name); !ok || got.OID != p.OID {
			t.Errorf("LookupBuiltinProc(%q) = %v, %v, want %v, true", p.Name, got, ok, p)
		}
	}
}
