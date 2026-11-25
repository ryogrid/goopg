package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNoteCatalogBlocksSharedPgDatabase pins the replayer's catalog-refresh
// keys (M0149-0032f): a block of a database's own catalog marks that
// database, and a block of the shared pg_database (global/1262) marks key 0,
// the database-list refresh — before this a replayed CREATE DATABASE never
// refreshed anything, and a goopg standby refused connections to the new
// database.
func TestNoteCatalogBlocksSharedPgDatabase(t *testing.T) {
	sr := &StreamReplayer{onCatalogReplay: func(uint32) {}}
	sr.noteCatalogBlocks(Record{XLog: &XLogDecodedRecord{Blocks: []XLogBlockRef{
		{Rel: storage.RelFileNode{DBOid: 0, RelOid: 1262}},
		{Rel: storage.RelFileNode{DBOid: 5, RelOid: 1259}},
		{Rel: storage.RelFileNode{DBOid: 5, RelOid: 16384}},
		{Rel: storage.RelFileNode{DBOid: 0, RelOid: 1260}},
	}}})
	if _, ok := sr.catalogDBs[0]; !ok {
		t.Error("pg_database block did not mark the database-list refresh (key 0)")
	}
	if _, ok := sr.catalogDBs[5]; !ok {
		t.Error("pg_class block of db 5 did not mark db 5")
	}
	if len(sr.catalogDBs) != 2 {
		t.Errorf("catalogDBs = %v, want keys 0 and 5 only", sr.catalogDBs)
	}
}
