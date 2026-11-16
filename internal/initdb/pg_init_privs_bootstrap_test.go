package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBootstrapPgInitPrivsHeap pins M0149-0001g slice 22: both per-database
// copies of pg_init_privs (3394) hold PG 18.3 initdb's rows in generated
// order. A PG node's pg_dump LEFT JOINs pg_init_privs to tell built-in ACLs
// from user changes; an empty heap makes every built-in ACL look user-made.
func TestBootstrapPgInitPrivsHeap(t *testing.T) {
	want := catalog.PGInitdbInitPrivs
	if len(want) != 229 {
		t.Fatalf("PGInitdbInitPrivs has %d rows, want PG 18.3 initdb's 229", len(want))
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	le := binary.LittleEndian
	for _, db := range []string{"1", "5"} {
		data, err := os.ReadFile(filepath.Join(dir, "base", db, "3394"))
		if err != nil {
			t.Fatalf("base/%s/3394: %v", db, err)
		}
		var got []uint32 // objoid of each normal tuple, in TID order
		for pg := 0; pg+8192 <= len(data); pg += 8192 {
			page := data[pg : pg+8192]
			lower := int(le.Uint16(page[12:14]))
			for slot := 24; slot+4 <= lower; slot += 4 {
				lp := le.Uint32(page[slot : slot+4])
				if (lp>>15)&3 != 1 { // LP_NORMAL
					continue
				}
				off := int(lp & 0x7FFF)
				hoff := int(page[off+22])
				got = append(got, le.Uint32(page[off+hoff:off+hoff+4]))
			}
		}
		if len(got) != len(want) {
			t.Fatalf("base/%s/3394 has %d tuples, want %d", db, len(got), len(want))
		}
		for i, r := range want {
			if got[i] != r.ObjOID {
				t.Fatalf("base/%s/3394 tuple %d objoid=%d, want %d", db, i, got[i], r.ObjOID)
			}
		}
		if fi, err := os.Stat(filepath.Join(dir, "base", db, "3395")); err != nil || fi.Size() < 2*8192 {
			t.Errorf("base/%s/3395 (pg_init_privs_o_c_o_index) not built: %v", db, err)
		}
	}
}
