package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M-NIGHTLY AI-20260810-011258-003 blocker #8 regression gate.
//
// TestE2E_PGStandbyFullCycle Phase D observed the promoted PG 18.3 performing
// ZERO index maintenance on goopg-created indexes: rows it inserted itself
// were reachable by seq-scan but missing from `s10_t_val_idx`, and
// `pg_waldump` on its own segment showed `Heap INSERT` + `Transaction COMMIT`
// with no RM_BTREE record at all — so nothing was lost in goopg's replay.
//
// Root cause: `pg_index_indrelid_index` (OID 2678, IndexIndrelidIndexId) was
// only an EMPTY btree placeholder. PG's `RelationGetIndexList`
// (postgres/src/backend/utils/cache/relcache.c) enumerates a relation's
// indexes exclusively through a systable scan on 2678 — no seq-scan fallback —
// so every relation looked index-less and `ExecInsertIndexTuples` had nothing
// to maintain. `indisvalid`/`indisready`/`indislive` were all true, which is
// why no catalog assertion caught it.
//
// This test pins the physical half after a real initdb: 2678 must be a
// POPULATED btree (metapage + leaf-root) carrying one indrelid-keyed
// IndexTuple per pg_index seed row, in all three locations PG may resolve it
// from (base/1, base/5, global).
func TestPgIndexIndrelidIndexBootstrapPopulated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Expected key multiset: the indrelid of every pg_index seed row, sorted
	// ascending (duplicates included — a catalog with N indexes contributes N
	// entries under its own OID, which is exactly why 2678 is NON-unique).
	wantKeys := make([]uint32, 0, 128)
	for _, e := range pgIndexInitialEntries() {
		wantKeys = append(wantKeys, e.IndRelid)
	}
	sort.Slice(wantKeys, func(i, j int) bool { return wantKeys[i] < wantKeys[j] })

	for _, loc := range []string{
		filepath.Join("base", "1"),
		filepath.Join("base", "5"),
		"global",
	} {
		path := filepath.Join(dir, loc, "2678")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if len(raw) < 2*storage.BlockSize {
			t.Errorf("%s: len=%d, want at least 2 blocks (%d) — still the EMPTY placeholder?",
				path, len(raw), 2*storage.BlockSize)
			continue
		}
		base := storage.SizeOfPageHeaderData
		root := binary.LittleEndian.Uint32(raw[base+8 : base+12])
		level := binary.LittleEndian.Uint32(raw[base+12 : base+16])
		if root == 0 {
			t.Errorf("%s: btm_root=0 (P_NONE) — index is empty", path)
			continue
		}
		if level != 0 {
			// The seed set fits one leaf today; a future growth past ~407
			// rows must extend this test to descend the tree.
			t.Fatalf("%s: btm_level=%d, want 0 (single leaf-root); test needs a descent", path, level)
		}
		leafOff := int(root) * storage.BlockSize
		leaf := raw[leafOff : leafOff+storage.BlockSize]
		h, err := storage.Header(storage.Page(leaf))
		if err != nil {
			t.Errorf("%s: leaf header: %v", path, err)
			continue
		}
		nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
		if nItems != len(wantKeys) {
			t.Errorf("%s: leaf items=%d, want %d (one per pg_index seed row)", path, nItems, len(wantKeys))
			continue
		}
		for i := 0; i < nItems; i++ {
			lp := binary.LittleEndian.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
			off := lp & 0x7FFF
			length := (lp >> 17) & 0x7FFF
			if length != 16 {
				t.Errorf("%s: item %d length=%d, want 16 (single oid key)", path, i, length)
				continue
			}
			gotBiHi := binary.LittleEndian.Uint16(leaf[off : off+2])
			gotBiLo := binary.LittleEndian.Uint16(leaf[off+2 : off+4])
			gotBlock := (uint32(gotBiHi) << 16) | uint32(gotBiLo)
			gotOff := binary.LittleEndian.Uint16(leaf[off+4 : off+6])
			gotKey := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
			if gotKey != wantKeys[i] {
				t.Errorf("%s: item %d indrelid=%d, want %d (ascending)", path, i, gotKey, wantKeys[i])
				continue
			}
			// The TID must point at a real pg_index heap tuple: block 0..N,
			// offset >= 1 (FirstOffsetNumber).
			if gotOff == 0 {
				t.Errorf("%s: item %d (indrelid=%d) TID=(%d,%d) has offset 0", path, i, gotKey, gotBlock, gotOff)
			}
		}
	}
}

// TestPgIndexIndrelidSeedsCoverNailedCatalogs guards the logical half: the
// nailed catalogs whose index lists PG rebuilds during startup — pg_class,
// pg_attribute, pg_index itself — must each contribute at least one 2678 key,
// and pg_index (2610) must contribute exactly two (2678 + 2679), since
// RelationGetIndexList on pg_index is what the sysscan machinery itself needs.
func TestPgIndexIndrelidSeedsCoverNailedCatalogs(t *testing.T) {
	counts := map[uint32]int{}
	for _, e := range pgIndexInitialEntries() {
		counts[e.IndRelid]++
	}
	for _, c := range []struct {
		relid uint32
		name  string
		want  int
	}{
		{1259, "pg_class", 2},     // 2662 oid, 2663 relname_nsp
		{1249, "pg_attribute", 2}, // 2658 relid_attnam, 2659 relid_attnum
		{2610, "pg_index", 2},     // 2678 indrelid, 2679 indexrelid
		{2604, "pg_attrdef", 2},   // 2656 adrelid_adnum, 2657 oid
	} {
		if got := counts[c.relid]; got != c.want {
			t.Errorf("%s (%d): %d pg_index seed rows, want %d", c.name, c.relid, got, c.want)
		}
	}
}
