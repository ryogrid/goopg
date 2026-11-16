package initdb

import "testing"

// TestPgTypeDelimBox pins pg_type.typdelim (M0149-0001k3): ';' for box and
// _box as pg_type.dat declares, ',' for everything else. A PG node splits
// box[] text on it; with ',' it read '{(1,1),(0,0)}' as "(1" and failed.
func TestPgTypeDelimBox(t *testing.T) {
	for oid, want := range map[uint32]string{603: ";", 1020: ";", 600: ",", 23: ",", 1017: ","} {
		if got := pgTypeDelim(oid); got != want {
			t.Errorf("typdelim(%d) = %q, want %q", oid, got, want)
		}
	}
}
