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
