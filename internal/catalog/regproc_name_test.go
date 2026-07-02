package catalog

import "testing"

// TestRegprocName pins the generated OID→proname reverse index
// (cmd/gen-pg-proc-data -names) that backs regproc/regprocedure output
// rendering in internal/executor and internal/server.
func TestRegprocName(t *testing.T) {
	tests := []struct {
		oid    uint32
		want   string
		wantOK bool
	}{
		{42, "int4in", true},
		{43, "int4out", true},
		{45, "regprocout", true},
		{0, "", false},
		{999999999, "", false},
	}
	for _, tt := range tests {
		got, ok := RegprocName(tt.oid)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("RegprocName(%d) = (%q, %v), want (%q, %v)", tt.oid, got, ok, tt.want, tt.wantOK)
		}
	}
}

// TestRegprocedureName pins RegprocedureName's "name(argtypes)" rendering
// (format_procedure/regprocedureout, regproc.c) for both a built-in (via the
// generated pgProcArgTypeNamesByOID index) and a CREATE FUNCTION-defined
// routine (via the live Routines registry), including its OUT-param
// filtering. DU-002 (M0119-0004).
func TestRegprocedureName(t *testing.T) {
	t.Run("builtin", func(t *testing.T) {
		tests := []struct {
			oid    uint32
			want   string
			wantOK bool
		}{
			{177, "int4pl(integer,integer)", true},    // int4pl(int4,int4)
			{351, "btint4cmp(integer,integer)", true}, // btint4cmp(int4,int4)
			{0, "", false},
			{999999999, "", false},
		}
		for _, tt := range tests {
			got, ok := RegprocedureName(tt.oid, nil)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("RegprocedureName(%d, nil) = (%q, %v), want (%q, %v)", tt.oid, got, ok, tt.want, tt.wantOK)
			}
		}
	})

	t.Run("user routine", func(t *testing.T) {
		rs := NewRoutines()
		created, err := rs.Create(&Routine{
			Name:     "my_add",
			ArgTypes: []Type{{Name: "int4"}, {Name: "int4"}},
			ArgModes: []string{"i", "i"},
		}, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, ok := RegprocedureName(created.OID, rs)
		if !ok || got != "my_add(integer,integer)" {
			t.Errorf("RegprocedureName(%d, rs) = (%q, %v), want (\"my_add(integer,integer)\", true)", created.OID, got, ok)
		}
	})

	t.Run("user routine with OUT param", func(t *testing.T) {
		rs := NewRoutines()
		created, err := rs.Create(&Routine{
			Name:     "my_split",
			ArgTypes: []Type{{Name: "int4"}, {Name: "int4"}},
			ArgModes: []string{"i", "o"},
		}, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, ok := RegprocedureName(created.OID, rs)
		if !ok || got != "my_split(integer)" {
			t.Errorf("RegprocedureName(%d, rs) = (%q, %v), want (\"my_split(integer)\", true)", created.OID, got, ok)
		}
	})
}
