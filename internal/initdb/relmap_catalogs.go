package initdb

// Mapped catalogs (RelationIsMapped, relmapper.c): pg_class.relfilenode is 0
// and the file number is looked up in pg_filenode.map. The lists are PG 18.3
// initdb's two map files verbatim, in their insertion order: global/ for the
// shared catalogs (relisshared), base/<db>/ for the four nailed local catalogs
// pg_class/pg_attribute/pg_proc/pg_type, their TOAST pairs and indexes, and
// pg_class_tblspc_relfilenode_index. goopg keeps each file named by its OID,
// so every mapping is the identity. M0149-0001g slice 27.
var (
	pgGlobalRelMapOIDs = []uint32{
		1262, 2964, 1213, 1260, 1261, 1214, 2396, 6000, 3592, 6243, 6100,
		4177, 4178, 2966, 2967, 4185, 4186, 2846, 2847, 4060, 4061, 6244,
		6245, 4183, 4184, 2671, 2672, 2965, 2697, 2698, 2676, 2677, 6303,
		2694, 2695, 6302, 1232, 1233, 2397, 6001, 6002, 3593, 6246, 6247,
		6114, 6115,
	}
	pgLocalRelMapOIDs = []uint32{
		1259, 1249, 1255, 1247, 2836, 2837, 4171, 4172, 2690, 2691, 2703,
		2704, 2658, 2659, 2662, 2663, 3455,
	}
)

// isMappedCatalogOID reports whether a bootstrapped relation is mapped, i.e.
// carries pg_class.relfilenode = 0. A PG node that rewrites a mapped catalog
// (VACUUM FULL, CLUSTER, REINDEX) records the new file number in the map, not
// in pg_class; with a non-zero relfilenode it would update pg_class instead
// and every later backend — whose nailed relcache entry is built from the map
// — would keep reading the old, truncated file.
func isMappedCatalogOID(oid uint32) bool {
	for _, o := range pgGlobalRelMapOIDs {
		if o == oid {
			return true
		}
	}
	for _, o := range pgLocalRelMapOIDs {
		if o == oid {
			return true
		}
	}
	return false
}

// identityRelMap renders oids as identity (oid → oid) relmap entries.
func identityRelMap(oids []uint32) [][2]uint32 {
	out := make([][2]uint32, len(oids))
	for i, o := range oids {
		out[i] = [2]uint32{o, o}
	}
	return out
}
