package executor

// PG18-canonical pg_class / pg_attribute row builders for user CREATE TABLE
// (M0106-0010 batched-36 loop 8 / batched-37).
//
// The historical helpers `catalog.PGClassColumns` / `catalog.PGAttributeColumns`
// emit a goopg-native 8-column pg_class row and 6-column pg_attribute row in
// goopg-specific ordering. A PostgreSQL 18 standby attaching to a goopg
// basebackup deforms the on-disk pg_class heap with PG18's 34-column tupdesc
// and reads garbage for `relname`, `relkind`, `relfilenode`, ... — the
// `relation "public.bench_log" does not exist` failure observed at the end of
// M0106-0010 batched-36 loop 7. These builders emit the same physical row
// layout that `internal/initdb/initdb.go::pgClassRow` / `pgAttributeRow` use
// for nailed system catalogs, so an attaching PG standby reads the row with
// the canonical PG18 tupdesc and locates the user table correctly.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// pgClassColumnsPG18 mirrors initdb.pgClassColDefs — the canonical PG18
// pg_class row layout (34 columns, matching the Form_pg_class struct in
// postgres/src/include/catalog/pg_class.h).
func pgClassColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "relname", Type: catalog.Type{Name: "name"}},
		{Name: "relnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "reltype", Type: catalog.Type{Name: "oid"}},
		{Name: "reloftype", Type: catalog.Type{Name: "oid"}},
		{Name: "relowner", Type: catalog.Type{Name: "oid"}},
		{Name: "relam", Type: catalog.Type{Name: "oid"}},
		{Name: "relfilenode", Type: catalog.Type{Name: "oid"}},
		{Name: "reltablespace", Type: catalog.Type{Name: "oid"}},
		{Name: "relpages", Type: catalog.Type{Name: "int4"}},
		{Name: "reltuples", Type: catalog.Type{Name: "float4"}},
		{Name: "relallvisible", Type: catalog.Type{Name: "int4"}},
		{Name: "relallfrozen", Type: catalog.Type{Name: "int4"}},
		{Name: "reltoastrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "relhasindex", Type: catalog.Type{Name: "bool"}},
		{Name: "relisshared", Type: catalog.Type{Name: "bool"}},
		{Name: "relpersistence", Type: catalog.Type{Name: "char"}},
		{Name: "relkind", Type: catalog.Type{Name: "char"}},
		{Name: "relnatts", Type: catalog.Type{Name: "int2"}},
		{Name: "relchecks", Type: catalog.Type{Name: "int2"}},
		{Name: "relhasrules", Type: catalog.Type{Name: "bool"}},
		{Name: "relhastriggers", Type: catalog.Type{Name: "bool"}},
		{Name: "relhassubclass", Type: catalog.Type{Name: "bool"}},
		{Name: "relrowsecurity", Type: catalog.Type{Name: "bool"}},
		{Name: "relforcerowsecurity", Type: catalog.Type{Name: "bool"}},
		{Name: "relispopulated", Type: catalog.Type{Name: "bool"}},
		{Name: "relreplident", Type: catalog.Type{Name: "char"}},
		{Name: "relispartition", Type: catalog.Type{Name: "bool"}},
		{Name: "relrewrite", Type: catalog.Type{Name: "oid"}},
		{Name: "relfrozenxid", Type: catalog.Type{Name: "xid"}},
		{Name: "relminmxid", Type: catalog.Type{Name: "xid"}},
		{Name: "relacl", Type: catalog.Type{Name: "aclitem[]"}},
		{Name: "reloptions", Type: catalog.Type{Name: "text[]"}},
		{Name: "relpartbound", Type: catalog.Type{Name: "pg_node_tree"}},
	}
}

// pgAttributeColumnsPG18 mirrors initdb.pgAttrColDefs — the canonical PG18
// pg_attribute row layout (25 columns).
func pgAttributeColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "attrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "attname", Type: catalog.Type{Name: "name"}},
		{Name: "atttypid", Type: catalog.Type{Name: "oid"}},
		{Name: "attlen", Type: catalog.Type{Name: "int2"}},
		{Name: "attnum", Type: catalog.Type{Name: "int2"}},
		{Name: "atttypmod", Type: catalog.Type{Name: "int4"}},
		{Name: "attndims", Type: catalog.Type{Name: "int2"}},
		{Name: "attbyval", Type: catalog.Type{Name: "bool"}},
		{Name: "attalign", Type: catalog.Type{Name: "char"}},
		{Name: "attstorage", Type: catalog.Type{Name: "char"}},
		{Name: "attcompression", Type: catalog.Type{Name: "char"}},
		{Name: "attnotnull", Type: catalog.Type{Name: "bool"}},
		{Name: "atthasdef", Type: catalog.Type{Name: "bool"}},
		{Name: "atthasmissing", Type: catalog.Type{Name: "bool"}},
		{Name: "attidentity", Type: catalog.Type{Name: "char"}},
		{Name: "attgenerated", Type: catalog.Type{Name: "char"}},
		{Name: "attisdropped", Type: catalog.Type{Name: "bool"}},
		{Name: "attislocal", Type: catalog.Type{Name: "bool"}},
		{Name: "attinhcount", Type: catalog.Type{Name: "int2"}},
		{Name: "attcollation", Type: catalog.Type{Name: "oid"}},
		{Name: "attacl", Type: catalog.Type{Name: "text"}},
		{Name: "attoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attfdwoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attmissingval", Type: catalog.Type{Name: "text"}},
	}
}

// userTypeAttrs captures the four pg_type-derived properties needed to write
// a PG18-canonical pg_attribute row: typlen (attlen), typbyval (attbyval),
// typalign (attalign), typstorage (attstorage).
type userTypeAttrs struct {
	TypLen     int16 // -1 == variable-length
	TypByVal   bool
	TypAlign   byte // 'c' | 's' | 'i' | 'd'
	TypStorage byte // 'p' | 'e' | 'x' | 'm'
}

// userTypeAttrsForOID returns the PG18 pg_type attributes for the OIDs that
// user CREATE TABLE can produce. Values match
// postgres/src/include/catalog/pg_type.dat. Unknown OIDs default to a varlena
// text-shaped descriptor so PG can still parse the tuple, even if it has the
// wrong type — preferable to a 4-byte by-val descriptor that would dereference
// inline bytes as a pointer.
func userTypeAttrsForOID(oid uint32) userTypeAttrs {
	switch oid {
	case catalog.OIDBool: // 16
		return userTypeAttrs{TypLen: 1, TypByVal: true, TypAlign: 'c', TypStorage: 'p'}
	case catalog.OIDBytea: // 17
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case 18: // "char"
		return userTypeAttrs{TypLen: 1, TypByVal: true, TypAlign: 'c', TypStorage: 'p'}
	case 19: // name
		return userTypeAttrs{TypLen: 64, TypByVal: false, TypAlign: 'c', TypStorage: 'p'}
	case catalog.OIDInt8: // 20
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDInt2: // 21
		return userTypeAttrs{TypLen: 2, TypByVal: true, TypAlign: 's', TypStorage: 'p'}
	case catalog.OIDInt4: // 23
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDText: // 25
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDOID: // 26
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDFloat4: // 700
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDFloat8: // 701
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDBpChar: // 1042
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDVarChar: // 1043
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDDate: // 1082
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDTime: // 1083
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTimestamp: // 1114
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTimestampTZ: // 1184
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDNumeric: // 1700
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'm'}
	}
	return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
}

const (
	// bootstrapSuperuserOID is the OID assigned to the bootstrap superuser
	// at initdb time — see postgres/src/include/access/transam.h:200
	// (BOOTSTRAP_SUPERUSERID). All system catalogs and (until ALTER OWNER
	// support lands) user tables list this as their relowner.
	bootstrapSuperuserOID int64 = 10

	// pgHeapAccessMethodOID is pg_am.oid for the heap table AM.
	pgHeapAccessMethodOID int64 = 2

	// pgBTreeAccessMethodOID is pg_am.oid for the btree index AM.
	pgBTreeAccessMethodOID int64 = 403

	// minFrozenXID matches initdb's bootstrap value for relfrozenxid /
	// relminmxid on freshly created relations (transam.h FirstNormalTransactionId).
	minFrozenXID int64 = 3
	minFrozenMXID int64 = 1
)

// buildUserPGClassRow constructs a 34-column PG18-canonical pg_class row for
// a user-defined table. Mirrors initdb.pgClassRow's per-column ordering and
// default values.
func buildUserPGClassRow(tbl *catalog.Table) Row {
	return Row{
		NewIntDatum(int64(tbl.OID)),                                  // oid
		NewStringDatum(tbl.Name),                                     // relname (name)
		NewIntDatum(int64(namespaceOIDForSchema(tbl.Schema))),        // relnamespace
		NewIntDatum(0),                                               // reltype (no composite type seeded yet)
		NewIntDatum(0),                                               // reloftype
		NewIntDatum(bootstrapSuperuserOID),                           // relowner
		NewIntDatum(pgHeapAccessMethodOID),                           // relam
		NewIntDatum(int64(tbl.OID)),                                  // relfilenode
		NewIntDatum(0),                                               // reltablespace (default per-db tablespace)
		NewIntDatum(0),                                               // relpages
		NewIntDatum(0),                                               // reltuples (float4 here; stored 0 == 0.0)
		NewIntDatum(0),                                               // relallvisible
		NewIntDatum(0),                                               // relallfrozen
		NewIntDatum(0),                                               // reltoastrelid
		NewBoolDatum(false),                                          // relhasindex (updated by CREATE INDEX later)
		NewBoolDatum(false),                                          // relisshared
		NewStringDatum("p"),                                          // relpersistence
		NewStringDatum("r"),                                          // relkind
		NewIntDatum(int64(len(tbl.Columns))),                         // relnatts
		NewIntDatum(0),                                               // relchecks
		NewBoolDatum(false),                                          // relhasrules
		NewBoolDatum(false),                                          // relhastriggers
		NewBoolDatum(false),                                          // relhassubclass
		NewBoolDatum(false),                                          // relrowsecurity
		NewBoolDatum(false),                                          // relforcerowsecurity
		NewBoolDatum(true),                                           // relispopulated
		NewStringDatum("n"),                                          // relreplident (REPLICA_IDENTITY_DEFAULT)
		NewBoolDatum(false),                                          // relispartition
		NewIntDatum(0),                                               // relrewrite
		NewIntDatum(minFrozenXID),                                    // relfrozenxid
		NewIntDatum(minFrozenMXID),                                   // relminmxid
		NewStringDatum("{}"),                                         // relacl (encoded as empty aclitem[] ArrayType)
		NewStringDatum("{}"),                                         // reloptions (encoded as empty text[] ArrayType)
		NewStringDatum(""),                                           // relpartbound (NULL-equivalent empty pg_node_tree)
	}
}

// buildUserPGClassRowForIndex constructs the 34-column PG18-canonical
// pg_class row for a user-defined index.
func buildUserPGClassRowForIndex(idx *catalog.Index) Row {
	natts := int64(len(idx.Columns))
	return Row{
		NewIntDatum(int64(idx.OID)),
		NewStringDatum(idx.Name),
		NewIntDatum(int64(namespaceOIDForSchema(idx.Schema))),
		NewIntDatum(0),                                               // reltype
		NewIntDatum(0),                                               // reloftype
		NewIntDatum(bootstrapSuperuserOID),                           // relowner
		NewIntDatum(pgBTreeAccessMethodOID),                          // relam
		NewIntDatum(int64(idx.OID)),                                  // relfilenode
		NewIntDatum(0),                                               // reltablespace
		NewIntDatum(0),                                               // relpages
		NewIntDatum(0),                                               // reltuples
		NewIntDatum(0),                                               // relallvisible
		NewIntDatum(0),                                               // relallfrozen
		NewIntDatum(0),                                               // reltoastrelid
		NewBoolDatum(false),                                          // relhasindex (indexes never have indexes themselves)
		NewBoolDatum(false),                                          // relisshared
		NewStringDatum("p"),                                          // relpersistence
		NewStringDatum("i"),                                          // relkind
		NewIntDatum(natts),                                           // relnatts
		NewIntDatum(0),                                               // relchecks
		NewBoolDatum(false),                                          // relhasrules
		NewBoolDatum(false),                                          // relhastriggers
		NewBoolDatum(false),                                          // relhassubclass
		NewBoolDatum(false),                                          // relrowsecurity
		NewBoolDatum(false),                                          // relforcerowsecurity
		NewBoolDatum(true),                                           // relispopulated
		NewStringDatum("n"),                                          // relreplident
		NewBoolDatum(false),                                          // relispartition
		NewIntDatum(0),                                               // relrewrite
		NewIntDatum(minFrozenXID),                                    // relfrozenxid
		NewIntDatum(minFrozenMXID),                                   // relminmxid
		NewStringDatum("{}"),                                         // relacl
		NewStringDatum("{}"),                                         // reloptions
		NewStringDatum(""),                                           // relpartbound
	}
}

// buildUserPGAttributeRow constructs a 25-column PG18-canonical pg_attribute
// row for a user-defined column.
func buildUserPGAttributeRow(tbl *catalog.Table, col catalog.Column) Row {
	typOID := catalog.TypeNameToOID(col.Type.Name)
	attrs := userTypeAttrsForOID(typOID)
	return Row{
		NewIntDatum(int64(tbl.OID)),               // attrelid
		NewStringDatum(col.Name),                  // attname (name)
		NewIntDatum(int64(typOID)),                // atttypid
		NewIntDatum(int64(attrs.TypLen)),          // attlen
		NewIntDatum(int64(col.Ordinal + 1)),       // attnum (1-based)
		NewIntDatum(-1),                           // atttypmod
		NewIntDatum(0),                            // attndims
		NewBoolDatum(attrs.TypByVal),              // attbyval
		NewStringDatum(string(attrs.TypAlign)),    // attalign
		NewStringDatum(string(attrs.TypStorage)),  // attstorage
		NewStringDatum(""),                        // attcompression (PG18 default: '\0' meaning "default")
		NewBoolDatum(col.NotNull),                 // attnotnull
		NewBoolDatum(false),                       // atthasdef
		NewBoolDatum(false),                       // atthasmissing
		NewStringDatum(""),                        // attidentity
		NewStringDatum(attGeneratedFor(col)),      // attgenerated
		NewBoolDatum(false),                       // attisdropped
		NewBoolDatum(true),                        // attislocal
		NewIntDatum(0),                            // attinhcount
		NewIntDatum(0),                            // attcollation
		// attacl / attoptions / attfdwoptions / attmissingval are nullable
		// varlena columns; PG18 stores NULL when unset. NullDatum signals
		// EncodeRowPG to skip the column and the bitmap helper to clear
		// its bit.
		NullDatum,
		NullDatum,
		NullDatum,
		NullDatum,
	}
}

// attGeneratedFor returns the attgenerated discriminator: 's' for GENERATED
// ALWAYS AS … STORED columns, '\0' (empty string, encoded by EncodeRowPG as
// a single zero byte) for ordinary columns. PG18 also supports 'v' (virtual)
// but goopg's catalog model does not surface that yet.
func attGeneratedFor(col catalog.Column) string {
	if col.GeneratedExpr != "" {
		return "s"
	}
	return ""
}
