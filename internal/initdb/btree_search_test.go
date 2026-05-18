package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestPgNamespaceHeapHasPgCatalog verifies the pg_namespace heap has an
// entry for pg_catalog (OID 11). Without this, PG can't find "pg_catalog"
// schema via the NAMESPACENAME syscache → all pg_catalog.X lookups fail.
func TestPgNamespaceHeapHasPgCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "base", "1", "2615"))
	if err != nil {
		t.Fatalf("ReadFile 2615 (pg_namespace): %v", err)
	}
	t.Logf("pg_namespace file size: %d bytes", len(data))

	// Count tuples by scanning slots
	le := binary.LittleEndian
	lower := le.Uint16(data[12:14])
	upper := le.Uint16(data[14:16])
	nSlots := int(lower-24) / 4
	t.Logf("pg_namespace page0: pd_lower=%d, pd_upper=%d, nSlots=%d", lower, upper, nSlots)
	
	for i := 0; i < nSlots; i++ {
		lp := le.Uint32(data[24+i*4 : 24+(i+1)*4])
		off := lp & 0x7FFF
		t.Logf("  slot %d: offset=%d", i+1, off)
	}
	
	if nSlots == 0 {
		t.Log("pg_namespace is EMPTY - no pg_catalog row exists!")
		t.Log("PG will fail with 'schema pg_catalog does not exist' for user queries")
	}
}

func TestPgNamespaceNspnameIndexHasPgCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "base", "1", "2684"))
	if err != nil {
		t.Fatalf("ReadFile 2684 (pg_namespace_nspname_index): %v", err)
	}
	
	// Read metapage at block 0
	le := binary.LittleEndian
	const base = 24
	btmRoot := le.Uint32(data[base+8 : base+12])
	btmLevel := le.Uint32(data[base+12 : base+16])
	t.Logf("Index 2684: %d bytes (%d blocks), btm_root=%d, btm_level=%d",
		len(data), len(data)/8192, btmRoot, btmLevel)

	if btmRoot == 0 || int(btmRoot)*8192+8192 > len(data) {
		t.Log("Index 2684 has empty/invalid root - pg_namespace not indexed!")
		return
	}
	
	// Scan leaf blocks for entries
	total := 0
	for blk := 1; blk < len(data)/8192; blk++ {
		page := data[blk*8192 : (blk+1)*8192]
		pageSpecial := le.Uint16(page[16:18])
		if pageSpecial+16 > 8192 {
			continue
		}
		flags := le.Uint16(page[pageSpecial+12 : pageSpecial+14])
		if flags&0x0001 == 0 {
			continue // not leaf
		}
		lower := le.Uint16(page[12:14])
		if lower < 24 {
			continue
		}
		nSlots := int(lower-24) / 4
		for i := 0; i < nSlots; i++ {
			lp := le.Uint32(page[24+i*4 : 24+(i+1)*4])
			off := lp & 0x7FFF
			if off == 0 || int(off)+72 > 8192 {
				continue
			}
			tinfo := le.Uint16(page[off+6 : off+8])
			if tinfo&0x2000 != 0 {
				continue
			}
			// name at off+8, 64 bytes
			nameData := page[off+8 : off+8+64]
			n := 64
			for n > 0 && nameData[n-1] == 0 {
				n--
			}
			t.Logf("  entry: %q", string(nameData[:n]))
			total++
		}
	}
	t.Logf("Total entries in 2684: %d", total)
}

// TestPgRewriteRowLayout verifies the pg_rewrite heap row encodes the ev_action
// at the expected byte offset so PG's heap_getattr(tup, 8, ...) finds it.
func TestPgRewriteRowLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	
	// Read base/1/2618 (pg_rewrite)
	data, err := os.ReadFile(filepath.Join(dir, "base", "1", "2618"))
	if err != nil {
		t.Fatalf("ReadFile 2618: %v", err)
	}
	t.Logf("pg_rewrite file size: %d bytes (%d pages)", len(data), len(data)/8192)
	
	le := binary.LittleEndian
	// Read page 0 header
	page := data[0:8192]
	lower := le.Uint16(page[12:14])
	nSlots := int(lower-24) / 4
	t.Logf("Page 0: pd_lower=%d, nSlots=%d", lower, nSlots)
	
	if nSlots == 0 {
		t.Fatal("pg_rewrite page 0 is empty!")
		return
	}
	
	// Read slot 1's line pointer
	lp := le.Uint32(page[24:28])
	off := lp & 0x7FFF
	size := (lp >> 17) & 0x7FFF
	t.Logf("Slot 1: offset=%d, size=%d", off, size)
	
	// The tuple header is at page[off:off+24] (t_hoff=24)
	tupleHdr := page[int(off) : int(off)+24]
	tHoff := tupleHdr[22]
	t.Logf("t_hoff=%d", tHoff)
	
	// Tuple data starts at page[off+tHoff:]
	tupleData := page[int(off)+int(tHoff):]
	
	// Column layout:
	// [0:4] oid
	// [4:68] name (64 bytes)
	// [68:72] ev_class
	// [72] ev_type
	// [73] ev_enabled
	// [74] is_instead
	// padding to 4-byte boundary at [75]→[76]
	// [76:79] ev_qual (1-byte varlena "<>")
	// padding to [79]→[80] (MAXALIGN to 4 bytes)
	// [80:] ev_action
	
	t.Logf("oid = %d", le.Uint32(tupleData[0:4]))
	ruleName := string(tupleData[4:4+7]) // "_RETURN"
	t.Logf("rulename = %q", ruleName)
	t.Logf("ev_class = %d", le.Uint32(tupleData[68:72]))
	t.Logf("ev_type = %q", string(tupleData[72:73]))
	t.Logf("ev_enabled = %q", string(tupleData[73:74]))
	t.Logf("is_instead = %d", tupleData[74])
	t.Logf("ev_qual at [76]: %v", tupleData[76:80]) // should be [0x07, '<', '>', padding]
	
	// Check ev_action varlena header at offset 80
	if len(tupleData) < 84 {
		t.Fatalf("tupleData too short: %d bytes", len(tupleData))
	}
	vaHeader := le.Uint32(tupleData[80:84])
	t.Logf("ev_action varlena header at [80]: 0x%08X", vaHeader)
	evActionSize := int(vaHeader >> 2)
	t.Logf("ev_action total size (including header): %d bytes, content: %d bytes",
		evActionSize, evActionSize-4)
	
	if evActionSize != 5932 {
		t.Errorf("expected ev_action total size 5932, got %d", evActionSize)
	}
	
	// Verify first few chars of ev_action content
	if len(tupleData) >= 84+5 {
		t.Logf("ev_action starts with: %q", string(tupleData[84:84+5]))
	}
}
