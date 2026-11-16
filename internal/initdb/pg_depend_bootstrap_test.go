package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestBootstrapPgDependHeap pins M0149-0001g slice 31: base/{1,5}/2608 holds
// PG 18.3 initdb's pg_depend rows — all 1725 since slice 32 bootstrapped
// pg_settings' rules 12108/12109 — including plpgsql's extension-membership
// rows.
func TestBootstrapPgDependHeap(t *testing.T) {
	if len(pgDependInitdb) != 1725 {
		t.Fatalf("pgDependInitdb has %d rows, want PG 18.3 initdb's 1725", len(pgDependInitdb))
	}
	want := pgDependBootstrapRows()
	if len(want) != 1725 {
		t.Fatalf("pgDependBootstrapRows = %d rows, want all 1725 (slice 32: no missing objects)", len(want))
	}
	members := 0
	for _, d := range want {
		if d.DepType == 'e' && d.RefClassID == 3079 && d.RefObjID == pgPlpgsqlExtension.OID {
			members++
		}
	}
	if members != 4 {
		t.Errorf("plpgsql extension members = %d, want 4 (three handlers + the language)", members)
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	le := binary.LittleEndian
	for _, db := range []string{"1", "5"} {
		data, err := os.ReadFile(filepath.Join(dir, "base", db, "2608"))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for pg := 0; pg+8192 <= len(data); pg += 8192 {
			page := data[pg : pg+8192]
			lower := int(le.Uint16(page[12:14]))
			for slot := 24; slot+4 <= lower; slot += 4 {
				if (le.Uint32(page[slot:])>>15)&3 == 1 {
					n++
				}
			}
		}
		if n != len(want) {
			t.Errorf("base/%s/2608 has %d tuples, want %d", db, n, len(want))
		}
	}
}
