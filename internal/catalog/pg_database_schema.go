package catalog

import "github.com/goopg/goopg/internal/storage"

// PgDatabaseRelationOID is pg_database's fixed shared-catalog OID
// (postgres/src/include/catalog/pg_database.h).
const PgDatabaseRelationOID uint32 = 1262

// PgDatabaseDatFrozenXIDOrdinal is the 0-based column ordinal of
// pg_database.datfrozenxid within PgDatabaseColumnsPG18.
const PgDatabaseDatFrozenXIDOrdinal = 9

// PgDatabaseColumnsPG18 returns the PG18 pg_database column schema (18
// columns; postgres/src/include/catalog/pg_database.h). Shared between
// initdb's on-disk bootstrap (bootstrapPostgresDatabase) and the runtime
// datfrozenxid persistence path (M0117-0008 Part B) so the two encode/decode
// call sites can never drift out of sync (pattern_sibling_paths_must_agree).
func PgDatabaseColumnsPG18() []Column {
	return []Column{
		{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
		{Name: "datname", Type: Type{Name: "name"}, Ordinal: 1},
		{Name: "datdba", Type: Type{Name: "oid"}, Ordinal: 2},
		{Name: "encoding", Type: Type{Name: "int4"}, Ordinal: 3},
		{Name: "datlocprovider", Type: Type{Name: "char"}, Ordinal: 4},
		{Name: "datistemplate", Type: Type{Name: "bool"}, Ordinal: 5},
		{Name: "datallowconn", Type: Type{Name: "bool"}, Ordinal: 6},
		{Name: "dathasloginevt", Type: Type{Name: "bool"}, Ordinal: 7},
		{Name: "datconnlimit", Type: Type{Name: "int4"}, Ordinal: 8},
		{Name: "datfrozenxid", Type: Type{Name: "xid"}, Ordinal: 9},
		{Name: "datminmxid", Type: Type{Name: "xid"}, Ordinal: 10},
		{Name: "dattablespace", Type: Type{Name: "oid"}, Ordinal: 11},
		{Name: "datcollate", Type: Type{Name: "text"}, Ordinal: 12},
		{Name: "datctype", Type: Type{Name: "text"}, Ordinal: 13},
		{Name: "datlocale", Type: Type{Name: "text"}, Ordinal: 14},
		{Name: "daticurules", Type: Type{Name: "text"}, Ordinal: 15},
		{Name: "datcollversion", Type: Type{Name: "text"}, Ordinal: 16},
		{Name: "datacl", Type: Type{Name: "aclitem[]"}, Ordinal: 17},
	}
}

// SharedCatalogRelFileNode returns the storage identity for a shared
// (cluster-wide) system catalog such as pg_database, which PostgreSQL stores
// once at global/<oid> rather than per-database under base/<dbOid>/<oid>.
// DBOid 0 is goopg's sentinel for "shared" (mirrored in
// storage.Manager's relPath/RelPath); no live database ever has OID 0 —
// DefaultDBOid is 1, and the live default resolves to "postgres"'s OID 5 via
// detectCatalogDBOID at startup. M0117-0008 Part B.
func SharedCatalogRelFileNode(oid uint32) storage.RelFileNode {
	return storage.RelFileNode{DBOid: 0, RelOid: oid, Fork: storage.MainFork}
}
