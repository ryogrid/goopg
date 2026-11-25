package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestMappedCatalogsMatchPG18 pins M0149-0001g slice 27: the 63 mapped
// catalogs carry pg_class.relfilenode = 0 and are listed, in PG 18.3 initdb's
// order, in global/ and base/<db>/pg_filenode.map; every other relation keeps
// relfilenode = oid. With the old non-zero relfilenode a PG node's VACUUM FULL
// pg_proc left later backends reading the truncated old file.
func TestMappedCatalogsMatchPG18(t *testing.T) {
	if n := len(pgGlobalRelMapOIDs) + len(pgLocalRelMapOIDs); n != 63 {
		t.Fatalf("mapped catalogs = %d, want PG 18.3's 63", n)
	}
	cols := pgClassColDefs()
	fn := -1
	for i, c := range cols {
		if c.Name == "relfilenode" {
			fn = i
		}
	}
	seen := 0
	for _, rel := range pgClassBootstrapRels() {
		got := pgClassRow(rel)[fn].Int
		switch {
		case isMappedCatalogOID(rel.OID):
			seen++
			if got != 0 {
				t.Errorf("mapped %d (%s) relfilenode=%d, want 0", rel.OID, rel.RelName, got)
			}
		case rel.RelKind == 'r' || rel.RelKind == 'i' || rel.RelKind == 't':
			if got != int64(rel.OID) {
				t.Errorf("%d (%s) relfilenode=%d, want its OID", rel.OID, rel.RelName, got)
			}
		}
	}
	if seen != 63 {
		t.Errorf("bootstrapped pg_class rows for %d mapped catalogs, want 63", seen)
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for path, want := range map[string][]uint32{
		filepath.Join(dir, "global", "pg_filenode.map"):    pgGlobalRelMapOIDs,
		filepath.Join(dir, "base", "1", "pg_filenode.map"): pgLocalRelMapOIDs,
		filepath.Join(dir, "base", "5", "pg_filenode.map"): pgLocalRelMapOIDs,
		filepath.Join(dir, "base", "4", "pg_filenode.map"): pgLocalRelMapOIDs,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		n := int(binary.LittleEndian.Uint32(data[4:8]))
		if n != len(want) {
			t.Errorf("%s: %d mappings, want %d", path, n, len(want))
			continue
		}
		for i, oid := range want {
			got := binary.LittleEndian.Uint32(data[8+i*8:])
			file := binary.LittleEndian.Uint32(data[12+i*8:])
			if got != oid || file != oid {
				t.Errorf("%s: mapping %d = %d→%d, want %d→%d", path, i, got, file, oid, oid)
			}
		}
	}
}
