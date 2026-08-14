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

	t.Run("user routine with array/varbit/char args", func(t *testing.T) {
		// M0119-0006 (74th slice, rows 1345/1346): the bare builder splits a
		// baked-in "[]" array suffix and aliases the ELEMENT (int[]→integer[],
		// char[]→"char"[]), and ArgTypeDisplayAlias now carries format_type_be's
		// varbit→bit varying and char→"char" cases — the executor's pg-faithful
		// regprocedureArglist already did all three, so this pins the sibling
		// agreeing on the catalog side.
		rs := NewRoutines()
		created, err := rs.Create(&Routine{
			Name:     "my_multi",
			ArgTypes: []Type{{Name: "int[]"}, {Name: "varbit"}, {Name: "char"}, {Name: "char[]"}},
			ArgModes: []string{"i", "i", "i", "i"},
		}, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, ok := RegprocedureName(created.OID, rs)
		want := `my_multi(integer[],bit varying,"char","char"[])`
		if !ok || got != want {
			t.Errorf("RegprocedureName(%d, rs) = (%q, %v), want (%q, true)", created.OID, got, ok, want)
		}
	})
}

// TestArgTypeDisplayAliasFormatTypeBePort pins the alias table to format_type_be
// (format_type.c) — the special-case switch plus the one builtin (char) that
// renders through the default path with keyword-quoting. M0119-0006 (74th
// slice, deferral row 1346).
func TestArgTypeDisplayAliasFormatTypeBePort(t *testing.T) {
	tests := []struct{ in, want string }{
		{"int4", "integer"},
		{"int", "integer"},
		{"int2", "smallint"},
		{"int8", "bigint"},
		{"float4", "real"},
		{"float8", "double precision"},
		{"bool", "boolean"},
		{"bpchar", "character"},
		{"varchar", "character varying"},
		{"timestamptz", "timestamp with time zone"},
		{"timestamp", "timestamp without time zone"},
		{"timetz", "time with time zone"},
		{"time", "time without time zone"},
		{"decimal", "numeric"},
		// 74th slice additions:
		{"varbit", "bit varying"}, // VARBITOID switch case
		{"char", `"char"`},        // default-path quote_identifier (keyword)
		// identity pass-throughs (no switch case / no keyword):
		{"text", "text"},
		{"uuid", "uuid"},
		{"json", "json"},
		{"interval", "interval"},
		{"bit", "bit"},
	}
	for _, tt := range tests {
		if got := ArgTypeDisplayAlias(tt.in); got != tt.want {
			t.Errorf("ArgTypeDisplayAlias(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
