package initdb

// catalog_cache.go — the retired M0114 fast-start catalog cache.
//
// M0114 wrote a JSON snapshot of user tables to
// <dataDir>/base/<dbOid>/pg_goopg_catalog_cache.json and, on the next
// startup, registered the tables from it instead of scanning the
// pg_class/pg_attribute heap. The snapshot kept only each column's name,
// type NAME, NOT NULL and ordinal, so a cache-hit restart lost typmods
// (char(n)/varchar(n)/numeric(p,s) lengths), array flags, identity,
// tablespace, database and every other heap-restored attribute — an int4[]
// column read back as a scalar (M0146-0151, found by M0141-S2a-fix2r-c).
//
// PG reads user relations from the system catalogs on every start; its
// relcache init file (pg_internal.init) covers only the nailed system
// relations. goopg now does the same: loadUserTablesFromHeap always runs,
// and the only remaining piece is the unlink, which removes a file an
// older binary left behind (at startup) and keeps the DDL-commit
// invalidation call site harmless.

import (
	"fmt"
	"os"
	"path/filepath"
)

const catalogCacheFilename = "pg_goopg_catalog_cache.json"

// catalogCachePath returns the absolute path of the retired catalog cache
// file for the given data directory.
func catalogCachePath(dataDir string, dbOid uint32) string {
	return filepath.Join(dataDir, "base", fmt.Sprint(dbOid), catalogCacheFilename)
}

// UnlinkCatalogCache removes the retired catalog cache file if present.
func UnlinkCatalogCache(dataDir string, dbOid uint32) {
	_ = os.Remove(catalogCachePath(dataDir, dbOid))
}
