package catalog

import "testing"

// TestPGClassListsEverySystemRelation pins M0149-0001h1a35: the virtual
// pg_class carries every system relation a PG 18.3 initdb lists (415 rows
// below 16384), and the captured row replaces goopg's bootstrap facsimile of
// the ones it models live (pg_class's relhasindex / relisshared / relacl).
func TestPGClassListsEverySystemRelation(t *testing.T) {
	c := NewInMemory()
	rows := c.PGClassRowsForDBOid(DefaultDBOid)
	byOID := map[string][]string{}
	system := 0
	for _, r := range rows {
		if _, dup := byOID[r[0]]; dup {
			t.Fatalf("pg_class row %s (%s) appears twice", r[0], r[1])
		}
		byOID[r[0]] = r
		if len(r[0]) < 5 || r[0] < "16384" {
			system++
		}
	}
	if system != len(seedPgClassRows) {
		t.Errorf("system pg_class rows = %d, want %d", system, len(seedPgClassRows))
	}
	for oid, want := range map[string][2]string{"2615": {"pg_namespace", "r"}, "1255": {"pg_proc", "r"}, "12226": {"pg_stat_activity", "v"}, "2662": {"pg_class_oid_index", "i"}} {
		r, ok := byOID[oid]
		if !ok || r[1] != want[0] || r[17] != want[1] {
			t.Errorf("pg_class %s = %v, want %s relkind %s", oid, r, want[0], want[1])
		}
	}
	if r := byOID["1259"]; r[14] != "t" || r[15] != "f" || r[31] == "" {
		t.Errorf("pg_class row for pg_class: relhasindex=%q relisshared=%q relacl=%q, want t, f, a default ACL", r[14], r[15], r[31])
	}
}
