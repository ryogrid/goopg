package executor

import "testing"

// TestPGClassFrozenIDsByRelkind pins PostgreSQL's rule (heap_create,
// catalog/heap.c; RELKIND_HAS_TABLE_AM, pg_class.h): only relkinds with a
// table access method get a valid relfrozenxid / relminmxid. PG 18.3 asserts
// !TransactionIdIsValid(relfrozenxid) for every other relkind when VACUUM or
// autovacuum recomputes datfrozenxid (vac_update_datfrozenxid), so a goopg
// pg_class row with a valid value on a view crashes a PG node started on the
// directory (M0149-0001g slice 5). Values measured on PG 18.3: r/t/m valid,
// i/I/S/v/c/p (and f) 0/0.
func TestPGClassFrozenIDsByRelkind(t *testing.T) {
	for _, k := range []string{"r", "t", "m"} {
		if x, m := PGClassFrozenIDs(k); x == 0 || m == 0 {
			t.Errorf("relkind %q: got (%d,%d), want valid relfrozenxid/relminmxid", k, x, m)
		}
	}
	for _, k := range []string{"i", "I", "S", "v", "c", "p", "f"} {
		if x, m := PGClassFrozenIDs(k); x != 0 || m != 0 {
			t.Errorf("relkind %q: got (%d,%d), want (0,0)", k, x, m)
		}
	}
}
