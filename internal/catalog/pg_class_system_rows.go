package catalog

import (
	"strconv"
	"sync"
)

var (
	capturedSystemRelOnce sync.Once
	capturedSystemRelSet  map[uint32]struct{}
)

// IsCapturedSystemRelation reports whether oid is one of the system relations
// PG 18.3's initdb lists in pg_class (seedPgClassRows, M0149-0001h1a35). Most
// of them are served only as captured pg_class rows: goopg models the catalog
// virtually or not at all, so no heap or index backs the OID. Callers that
// open a relation by OID (amcheck's verify_heapam / bt_index_check, reached
// from pg_amcheck's whole-cluster walk over pg_class) use this to tell such a
// row from a relation that does not exist.
func IsCapturedSystemRelation(oid uint32) bool {
	capturedSystemRelOnce.Do(func() {
		capturedSystemRelSet = make(map[uint32]struct{}, len(seedPgClassRows))
		for _, r := range seedPgClassRows {
			if v, err := strconv.ParseUint(r[0], 10, 32); err == nil {
				capturedSystemRelSet[uint32(v)] = struct{}{}
			}
		}
	})
	_, ok := capturedSystemRelSet[oid]
	return ok
}

// SeedRelationStorage reports where a system relation of PG 18.3's initdb
// keeps its main fork, from its seeded pg_class row: the file number
// (relfilenode, or the OID for a mapped catalog whose relfilenode is 0), the
// relkind, and whether it is a shared catalog (reltablespace pg_global, so
// global/<filenode>). ok is false for an OID that is not a seeded relation.
// M0149-0006.
func SeedRelationStorage(oid uint32) (filenode uint32, relkind string, shared bool, ok bool) {
	key := strconv.FormatUint(uint64(oid), 10)
	for _, r := range seedPgClassRows {
		if len(r) < 18 || r[0] != key {
			continue
		}
		fn, err := strconv.ParseUint(r[7], 10, 32)
		if err != nil {
			return 0, "", false, false
		}
		if fn == 0 {
			fn = uint64(oid)
		}
		return uint32(fn), r[17], r[8] == "1664", true
	}
	return 0, "", false, false
}
