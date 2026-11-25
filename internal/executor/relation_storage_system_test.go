package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRelationStorageForSystemOIDsFollowsSeededPgClass pins the storage the
// size/path functions and pg_prewarm report for system relations
// (M0149-0029): a shared catalog lives in global/ (DBOid 0, relpathbackend
// with GLOBALTABLESPACE_OID), and a system view has no storage at all, so
// pg_relation_filenode is NULL — as PG's RELKIND_HAS_STORAGE has it. A
// per-database catalog keeps its database's storage.
func TestRelationStorageForSystemOIDsFollowsSeededPgClass(t *testing.T) {
	cat := catalog.NewInMemory()
	if rel, ok := relationStorageForOID(cat, 1262); !ok || rel.DBOid != 0 || rel.RelOid != 1262 {
		t.Fatalf("pg_database storage = %+v, %v; want global/1262", rel, ok)
	}
	if rel, ok := relationStorageForOID(cat, 12226); ok {
		t.Fatalf("pg_stat_activity (a view) has storage %+v; want none", rel)
	}
	if _, relkind, shared, ok := catalog.SeedRelationStorage(1259); !ok || shared || relkind != "r" {
		t.Fatalf("pg_class seed = %q shared=%v ok=%v; want a per-database table", relkind, shared, ok)
	}
}
