package initdb

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// pg_class (FormData_pg_class) field offsets inside the tuple's data area; see
// pgClassColDefs.
const (
	pgClassOffOID           = 0
	pgClassOffRelfilenode   = 88
	pgClassOffReltablespace = 92
	pgClassOffRelpages      = 96
	pgClassOffReltuples     = 100
	pgClassOffRelallvisible = 104
	pgClassOffRelallfrozen  = 108
	pgClassOffRelkind       = 119

	relStatsBlockSize = 8192
)

// stampPgClassRelStats fills relpages / reltuples / relallvisible /
// relallfrozen of every bootstrapped pg_class row in base/1 and base/5 from
// the relation files goopg actually wrote. PG's initdb ends with VACUUM FREEZE
// (initdb.c vacuum_db), so a fresh PG cluster's catalogs carry measured sizes;
// goopg's catalog heaps are laid out differently, so copying PG's numbers would
// describe files that do not exist. Measured here instead, per relkind:
//
//   - heap (r, t, m): relpages = blocks; reltuples = LP_NORMAL line pointers;
//     relallvisible / relallfrozen = blocks with PD_ALL_VISIBLE set (goopg
//     writes no visibility map at initdb, so this is the honest count —
//     vac_update_relstats' visibilitymap_count would see the same).
//   - btree index (i): relpages = blocks; reltuples = leaf items excluding high
//     keys (btvacuumcleanup's num_index_tuples); relallvisible / relallfrozen 0.
//   - view / composite (v, c): reltuples = -1, the never-vacuumed value
//     heap_create_with_catalog leaves.
//
// A PG node plans catalog queries from these (estimate_rel_size scales
// reltuples/relpages by the current block count). goopg's own planner does not
// read catalog stats from pg_class. Must run after every catalog file is
// written and before template0 is copied from template1. M0149-0001g slice 25.
func stampPgClassRelStats(dataDir string) error {
	for _, db := range []string{"1", "5"} {
		dbDir := filepath.Join(dataDir, "base", db)
		path := filepath.Join(dbDir, "1259")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("pg_class stats: %w", err)
		}
		le := binary.LittleEndian
		for pg := 0; pg+relStatsBlockSize <= len(data); pg += relStatsBlockSize {
			page := data[pg : pg+relStatsBlockSize]
			lower := int(le.Uint16(page[12:14]))
			for slot := 24; slot+4 <= lower; slot += 4 {
				lp := le.Uint32(page[slot : slot+4])
				if (lp>>15)&3 != 1 { // LP_NORMAL
					continue
				}
				off := int(lp & 0x7FFF)
				d := page[off+int(page[off+22]):]
				oid := le.Uint32(d[pgClassOffOID:])
				fileNode := le.Uint32(d[pgClassOffRelfilenode:])
				if fileNode == 0 {
					fileNode = oid // mapped catalog: pg_filenode.map maps it to its OID
				}
				dir := dbDir
				if le.Uint32(d[pgClassOffReltablespace:]) == 1664 {
					dir = filepath.Join(dataDir, "global")
				}
				pages, tuples, allVisible, err := measureRelation(filepath.Join(dir, fmt.Sprint(fileNode)), d[pgClassOffRelkind])
				if err != nil {
					return fmt.Errorf("pg_class stats for %d: %w", oid, err)
				}
				le.PutUint32(d[pgClassOffRelpages:], uint32(pages))
				le.PutUint32(d[pgClassOffReltuples:], math.Float32bits(tuples))
				le.PutUint32(d[pgClassOffRelallvisible:], uint32(allVisible))
				le.PutUint32(d[pgClassOffRelallfrozen:], uint32(allVisible))
			}
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("pg_class stats: %w", err)
		}
	}
	return nil
}

// measureRelation returns relpages, reltuples and the all-visible page count
// for the relation file at path (a missing file is an empty relation).
func measureRelation(path string, relkind byte) (int32, float32, int32, error) {
	switch relkind {
	case 'v', 'c':
		return 0, -1, 0, nil
	case 'r', 't', 'm', 'i':
	default:
		return 0, 0, 0, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, 0, 0, nil
	}
	if err != nil {
		return 0, 0, 0, err
	}
	le := binary.LittleEndian
	nblocks := len(data) / relStatsBlockSize
	var tuples, allVisible int
	for b := 0; b < nblocks; b++ {
		page := data[b*relStatsBlockSize : (b+1)*relStatsBlockSize]
		lower := int(le.Uint16(page[12:14]))
		if lower < 24 { // never-initialised page
			continue
		}
		nitems := (lower - 24) / 4
		if relkind == 'i' {
			special := int(le.Uint16(page[16:18]))
			if special+16 > relStatsBlockSize {
				continue
			}
			opaque := page[special:]
			flags := le.Uint16(opaque[12:14])
			const btpLeaf, btpMeta = 1 << 0, 1 << 3
			if flags&btpMeta != 0 || flags&btpLeaf == 0 {
				continue
			}
			if le.Uint32(opaque[4:8]) != 0 { // btpo_next: not rightmost, item 1 is the high key
				nitems--
			}
			if nitems > 0 {
				tuples += nitems
			}
			continue
		}
		for slot := 0; slot < nitems; slot++ {
			lp := le.Uint32(page[24+slot*4:])
			if (lp>>15)&3 == 1 {
				tuples++
			}
		}
		if le.Uint16(page[10:12])&0x0004 != 0 { // PD_ALL_VISIBLE
			allVisible++
		}
	}
	if relkind == 'i' {
		allVisible = 0
	}
	return int32(nblocks), float32(tuples), int32(allVisible), nil
}
