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
