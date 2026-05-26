package initdb

// catalog_cache.go — M0114: pg_internal.init fast-start cache read path.
//
// goopg writes pg_internal.init (PG18 binary relcache format) for PG standby
// compatibility (M0106). This file adds a symmetric goopg-native read path:
// after startup, a lightweight JSON snapshot of user table/column info is
// written to <dataDir>/base/1/pg_goopg_catalog_cache.json. On the next
// startup this snapshot is read back — if it is valid and not stale — to
// populate catalog.InMemory without scanning pg_class/pg_attribute pages.
//
// Staleness: the cache file is unlinked whenever pg_internal.init is unlinked
// (DDL commit → RelcacheInitFilePreInvalidate). Both files track the same
// invalidation epoch so no new invalidation machinery is needed.
//
// On any parse or validation error the function returns (false, nil) and the
// caller falls through to the normal heap scan.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/catalog"
)

const catalogCacheFilename = "pg_goopg_catalog_cache.json"

// catalogCacheVersion is bumped whenever the schema changes to force a
// cold-start rebuild.
const catalogCacheVersion = 1

// catalogCacheEntry is the on-disk JSON representation of one user table.
type catalogCacheEntry struct {
	OID            uint32               `json:"oid"`
	Schema         string               `json:"schema"`
	Name           string               `json:"name"`
	Columns        []catalogCacheColumn `json:"columns"`
	RelFileNodeOID uint32               `json:"relfilenode_oid,omitempty"`
	SmallDimension bool                 `json:"small_dimension,omitempty"`
}

type catalogCacheColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	NotNull  bool   `json:"not_null,omitempty"`
	Ordinal  int    `json:"ordinal"`
}

type catalogCacheFile struct {
	Version int                  `json:"version"`
	Tables  []catalogCacheEntry  `json:"tables"`
}

// catalogCachePath returns the absolute path to the catalog cache file for
// the given data directory.
func catalogCachePath(dataDir string, dbOid uint32) string {
	return filepath.Join(dataDir, "base", fmt.Sprint(dbOid), catalogCacheFilename)
}

// writeCatalogCache persists all user tables from cat to the catalog cache
// file. Called after a successful startup (after loadUserTablesFromHeap
// completes) so the next startup can skip the heap scan.
func writeCatalogCache(dataDir string, dbOid uint32, cat *catalog.InMemory) error {
	tables := cat.AllTables()
	entries := make([]catalogCacheEntry, 0, len(tables))
	for _, tbl := range tables {
		if tbl.Virtual || tbl.OID < catalog.FirstUserOID {
			continue
		}
		cols := make([]catalogCacheColumn, len(tbl.Columns))
		for i, c := range tbl.Columns {
			cols[i] = catalogCacheColumn{
				Name:    c.Name,
				Type:    c.Type.Name,
				NotNull: c.NotNull,
				Ordinal: c.Ordinal,
			}
		}
		entries = append(entries, catalogCacheEntry{
			OID:            tbl.OID,
			Schema:         tbl.Schema,
			Name:           tbl.Name,
			Columns:        cols,
			RelFileNodeOID: tbl.RelFileNodeOID,
			SmallDimension: tbl.SmallDimension,
		})
	}
	cf := catalogCacheFile{Version: catalogCacheVersion, Tables: entries}
	data, err := json.Marshal(cf)
	if err != nil {
		return fmt.Errorf("catalogCache marshal: %w", err)
	}
	path := catalogCachePath(dataDir, dbOid)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("catalogCache write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// readCatalogCache attempts to load the catalog cache. Returns (true, nil) on
// success, (false, nil) when the cache is absent or stale, and (false, err)
// on unexpected errors.
func readCatalogCache(dataDir string, dbOid uint32, cat *catalog.InMemory) (bool, error) {
	path := catalogCachePath(dataDir, dbOid)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("catalogCache read: %w", err)
	}
	var cf catalogCacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		slog.Warn("catalogCache: corrupt JSON, ignoring", "err", err)
		_ = os.Remove(path)
		return false, nil
	}
	if cf.Version != catalogCacheVersion {
		slog.Warn("catalogCache: version mismatch, ignoring", "got", cf.Version, "want", catalogCacheVersion)
		_ = os.Remove(path)
		return false, nil
	}

	for _, e := range cf.Tables {
		cols := make([]catalog.Column, len(e.Columns))
		for i, c := range e.Columns {
			cols[i] = catalog.Column{
				Name:    c.Name,
				Type:    catalog.Type{Name: c.Type},
				NotNull: c.NotNull,
				Ordinal: c.Ordinal,
			}
		}
		tbl := &catalog.Table{
			OID:            e.OID,
			Schema:         e.Schema,
			Name:           e.Name,
			Columns:        cols,
			RelFileNodeOID: e.RelFileNodeOID,
			SmallDimension: e.SmallDimension,
		}
		if err := cat.TryRegisterUserTable(tbl); err != nil {
			slog.Warn("catalogCache: TryRegisterUserTable failed", "table", e.Name, "err", err)
		}
	}
	return true, nil
}

// UnlinkCatalogCache removes the catalog cache file. Called alongside
// RelcacheInitFileUnlink so both files are invalidated atomically on DDL.
func UnlinkCatalogCache(dataDir string, dbOid uint32) {
	path := catalogCachePath(dataDir, dbOid)
	_ = os.Remove(path)
}
