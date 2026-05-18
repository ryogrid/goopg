package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree guards
// M0106-0010 step 3cs. After step 3cr changed pg_class.reltablespace=1664
// for shared catalogs, PG's RelationInitPhysicalAddr resolves the file
// path of pg_database_oid_index (OID 2672) to global/2672 instead of
// base/<MyDatabaseId>/2672. The empty btree placeholder seeded by
// bootstrapPostgresDatabase was sufficient to satisfy mdopen() but PG's
// CheckMyDatabase (postinit.c:335) probes syscache DATABASEOID — backed
// by pg_database_oid_index — to validate that MyDatabaseId references a
// live pg_database row. Without populated index entries the syscache
// lookup returns NULL and the backend FATALs with
// `cache lookup failed for database 5`.
//
// This test calls bootstrapPgDatabaseOidIndex in a temp dir and asserts
// the resulting global/2672 file contains 2 IndexTuples with the OID keys
// matching the two pg_database heap rows written by
// bootstrapPostgresDatabase (template1 OID 1 at heap TID (0,1) and
// postgres OID 5 at heap TID (0,2)).
func TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := bootstrapPgDatabaseOidIndex(dir); err != nil {
		t.Fatalf("bootstrapPgDatabaseOidIndex: %v", err)
	}

	buf, err := os.ReadFile(filepath.Join(dir, "global", "2672"))
	if err != nil {
		t.Fatalf("read global/2672: %v", err)
	}
	if len(buf) != 2*storage.BlockSize {
		t.Fatalf("file size = %d bytes, want %d (metapage + leaf root)", len(buf), 2*storage.BlockSize)
	}

	// Leaf-root page is at block 1.
	leaf := buf[storage.BlockSize:]

	// Parse line-pointer count from the page header. PG page header:
	//   pd_lsn (8), pd_checksum (2), pd_flags (2), pd_lower (2), pd_upper (2),
	//   pd_special (2), pd_pagesize_version (2)
	// Item pointers start at offset 24 (PageHeaderData size). Each
	// ItemId is 4 bytes packed.
	pdLower := binary.LittleEndian.Uint16(leaf[12:14])
	const pageHeaderSize = 24
	const itemIDSize = 4
	if pdLower < pageHeaderSize {
		t.Fatalf("pd_lower=%d < page header size", pdLower)
	}
	numItems := (int(pdLower) - pageHeaderSize) / itemIDSize
	// pgBuildBtreeLeafRootPage emits one ItemId per data tuple (no
	// P_HIKEY synthesised for leaf-root); two pg_database rows produce
	// two line pointers.
	if numItems != 2 {
		t.Fatalf("leaf line-pointer count = %d, want 2 (2 data tuples)", numItems)
	}

	// Walk the 2 data tuples (slots 1, 2) and assert their OID keys
	// equal the sorted pg_database OIDs.
	gotOids := make([]uint32, 0, 2)
	for slot := 1; slot <= 2; slot++ {
		ipPos := pageHeaderSize + (slot-1)*itemIDSize
		raw := binary.LittleEndian.Uint32(leaf[ipPos : ipPos+4])
		offset := int(raw & 0x7FFF)        // 15-bit lp_off
		length := int((raw >> 17) & 0x7FFF) // 15-bit lp_len
		if offset == 0 || length == 0 {
			t.Fatalf("slot %d: empty line pointer (offset=%d length=%d)", slot, offset, length)
		}
		if offset+length > len(leaf) {
			t.Fatalf("slot %d: tuple extends past page (offset=%d length=%d)", slot, offset, length)
		}
		// IndexTuple layout: t_tid (6) + t_info (2) + key data.
		// hoff = MAXALIGN(sizeof(IndexTupleData)) = 8 for no-nulls case.
		const hoff = 8
		if length < hoff+4 {
			t.Fatalf("slot %d: tuple too short for oid key (length=%d)", slot, length)
		}
		oid := binary.LittleEndian.Uint32(leaf[offset+hoff : offset+hoff+4])
		// Also assert heap TID block=0, offset=(slot-1).
		blkHi := binary.LittleEndian.Uint16(leaf[offset : offset+2])
		blkLo := binary.LittleEndian.Uint16(leaf[offset+2 : offset+4])
		heapOff := binary.LittleEndian.Uint16(leaf[offset+4 : offset+6])
		heapBlk := uint32(blkHi)<<16 | uint32(blkLo)
		if heapBlk != 0 {
			t.Errorf("slot %d (oid=%d): heap block = %d, want 0", slot, oid, heapBlk)
		}
		gotOids = append(gotOids, oid)
		_ = heapOff
	}

	// Index entries are written sorted by oid (key ascending), so oids
	// 1 (template1) and 5 (postgres) appear at slots 1, 2 respectively.
	want := []uint32{1, 5}
	for i, w := range want {
		if gotOids[i] != w {
			t.Errorf("slot %d: oid key = %d, want %d", i+1, gotOids[i], w)
		}
	}
}
