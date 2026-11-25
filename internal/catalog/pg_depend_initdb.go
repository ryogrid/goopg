package catalog

import "strconv"

// InitdbDepend is one pg_depend row a PG 18.3 initdb leaves
// (FormData_pg_depend, postgres/src/include/catalog/pg_depend.h).
type InitdbDepend struct {
	ClassID, ObjID       uint32
	ObjSubID             int32
	RefClassID, RefObjID uint32
	RefObjSubID          int32
	DepType              byte
}

// initdbDependRows renders PGDependInitdb as pg_depend view rows. initdb writes
// the same table to the on-disk catalog (base/<db>/2608), so goopg's
// pg_depend and the one a PG node reads agree on the bootstrap dependencies —
// system views, information_schema, snowball, plpgsql — that every database
// inherits from template1. M0149-0001g slice 34.
func initdbDependRows() [][]string {
	out := make([][]string, len(PGDependInitdb))
	for i, d := range PGDependInitdb {
		out[i] = []string{
			strconv.FormatUint(uint64(d.ClassID), 10),
			strconv.FormatUint(uint64(d.ObjID), 10),
			strconv.FormatInt(int64(d.ObjSubID), 10),
			strconv.FormatUint(uint64(d.RefClassID), 10),
			strconv.FormatUint(uint64(d.RefObjID), 10),
			strconv.FormatInt(int64(d.RefObjSubID), 10),
			string(d.DepType),
		}
	}
	return out
}
