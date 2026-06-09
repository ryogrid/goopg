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
	"encoding/binary"
	"math"
	"strconv"

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
		TypLen        int16 // -1 == variable-length
		TypByVal      bool
		TypAlign      byte // 'c' | 's' | 'i' | 'd'
		TypStorage    byte // 'p' | 'e' | 'x' | 'm'
		TypCollation  uint32
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
	case 19: // name -- pg_type.dat: typcollation => 'C' (C_COLLATION_OID = 950)
		return userTypeAttrs{TypLen: 64, TypByVal: false, TypAlign: 'c', TypStorage: 'p', TypCollation: cCollationOID}
	case catalog.OIDInt8: // 20
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDInt2: // 21
		return userTypeAttrs{TypLen: 2, TypByVal: true, TypAlign: 's', TypStorage: 'p'}
	case catalog.OIDInt4: // 23
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDText: // 25 -- pg_type.dat: typcollation => 'default'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
	case catalog.OIDOID: // 26
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDFloat4: // 700
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDFloat8: // 701
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDBpChar: // 1042 -- pg_type.dat: typcollation => 'default'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
	case catalog.OIDVarChar: // 1043 -- pg_type.dat: typcollation => 'default'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
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
	minFrozenXID  int64 = 3
	minFrozenMXID int64 = 1

	// defaultCollationOID matches PG18's pg_collation_d.h::DEFAULT_COLLATION_OID
	// (100). Used for text/varchar/bpchar pg_attribute.attcollation so a
	// PG-side standby can resolve the default collation when planning
	// expressions like `WHERE textcol = 'literal'`.
	defaultCollationOID uint32 = 100
	// cCollationOID matches PG18's pg_collation_d.h::C_COLLATION_OID (950).
	// Used for the `name` type whose pg_type.dat entry pins
	// `typcollation => 'C'`.
	cCollationOID uint32 = 950
)

// buildUserPGClassRow constructs a 34-column PG18-canonical pg_class row for
// a user-defined table. Mirrors initdb.pgClassRow's per-column ordering and
// default values.
func buildUserPGClassRow(tbl *catalog.Table) Row {
	relkind := "r"
	if tbl.PartitionMethod != "" {
		relkind = "p" // partitioned table
	}
	relfilenode := int64(tbl.OID)
	if relkind == "p" {
		relfilenode = 0 // partitioned tables have no physical storage
	}
	isPartition := tbl.PartitionParentOID != 0
	return Row{
		NewIntDatum(int64(tbl.OID)),                                  // oid
		NewStringDatum(tbl.Name),                                     // relname (name)
		NewIntDatum(int64(namespaceOIDForSchema(tbl.Schema))),        // relnamespace
		NewIntDatum(0),                                               // reltype (no composite type seeded yet)
		NewIntDatum(0),                                               // reloftype
		NewIntDatum(bootstrapSuperuserOID),                           // relowner
		NewIntDatum(pgHeapAccessMethodOID),                           // relam
		NewIntDatum(relfilenode),                                     // relfilenode
		NewIntDatum(0),                                               // reltablespace (default per-db tablespace)
		NewIntDatum(0),                                               // relpages
		NewIntDatum(0),                                               // reltuples (float4 here; stored 0 == 0.0)
		NewIntDatum(0),                                               // relallvisible
		NewIntDatum(0),                                               // relallfrozen
		NewIntDatum(0),                                               // reltoastrelid
		NewBoolDatum(false),                                          // relhasindex (updated by CREATE INDEX later)
		NewBoolDatum(false),                                          // relisshared
		NewStringDatum("p"),                                          // relpersistence
		NewStringDatum(relkind),                                      // relkind
		NewIntDatum(int64(len(tbl.Columns))),                         // relnatts
		NewIntDatum(0),                                               // relchecks
		NewBoolDatum(false),                                          // relhasrules
		NewBoolDatum(false),                                          // relhastriggers
		NewBoolDatum(false),                                          // relhassubclass
		NewBoolDatum(false),                                          // relrowsecurity
		NewBoolDatum(false),                                          // relforcerowsecurity
		NewBoolDatum(true),                                           // relispopulated
		NewStringDatum("n"),                                          // relreplident (REPLICA_IDENTITY_DEFAULT)
		NewBoolDatum(isPartition),                                    // relispartition
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
		NewBoolDatum(col.DefaultExpr != nil),      // atthasdef
		NewBoolDatum(false),                       // atthasmissing
		NewStringDatum(""),                        // attidentity
		NewStringDatum(attGeneratedFor(col)),      // attgenerated
		NewBoolDatum(false),                       // attisdropped
		NewBoolDatum(!col.Inherited),              // attislocal
		func() Datum {
			if col.Inherited {
				return NewIntDatum(1)
			}
			return NewIntDatum(0)
		}(),                                       // attinhcount
		NewIntDatum(int64(attrs.TypCollation)),    // attcollation
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

// --- pg_index row builders (M0113) ---

// pgIndexColumnsPG18 mirrors initdb.pgIndexColDefs — the canonical PG18
// pg_index row layout (21 columns, matching FormData_pg_index in
// postgres/src/include/catalog/pg_index.h).
func pgIndexColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "indexrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "indrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "indnatts", Type: catalog.Type{Name: "int2"}},
		{Name: "indnkeyatts", Type: catalog.Type{Name: "int2"}},
		{Name: "indisunique", Type: catalog.Type{Name: "bool"}},
		{Name: "indnullsnotdistinct", Type: catalog.Type{Name: "bool"}},
		{Name: "indisprimary", Type: catalog.Type{Name: "bool"}},
		{Name: "indisexclusion", Type: catalog.Type{Name: "bool"}},
		{Name: "indimmediate", Type: catalog.Type{Name: "bool"}},
		{Name: "indisclustered", Type: catalog.Type{Name: "bool"}},
		{Name: "indisvalid", Type: catalog.Type{Name: "bool"}},
		{Name: "indcheckxmin", Type: catalog.Type{Name: "bool"}},
		{Name: "indisready", Type: catalog.Type{Name: "bool"}},
		{Name: "indislive", Type: catalog.Type{Name: "bool"}},
		{Name: "indisreplident", Type: catalog.Type{Name: "bool"}},
		{Name: "indkey", Type: catalog.Type{Name: "int2vector"}},
		{Name: "indcollation", Type: catalog.Type{Name: "oidvector"}},
		{Name: "indclass", Type: catalog.Type{Name: "oidvector"}},
		{Name: "indoption", Type: catalog.Type{Name: "int2vector"}},
		{Name: "indexprs", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "indpred", Type: catalog.Type{Name: "pg_node_tree"}},
	}
}

// buildUserPGIndexRow constructs the 21-column PG18-canonical pg_index row
// for a user-defined index. Column names are mapped to 1-based attnums via
// the index's parent table column list.
func buildUserPGIndexRow(idx *catalog.Index) Row {
	n := len(idx.Columns)
	natts := int64(n)

	attnums := make([]int16, n)
	if idx.Table != nil {
		for i, colName := range idx.Columns {
			for _, col := range idx.Table.Columns {
				if col.Name == colName {
					attnums[i] = int16(col.Ordinal + 1)
					break
				}
			}
		}
	}
	zeros32 := make([]uint32, n)
	zeros16 := make([]int16, n)

	return Row{
		NewIntDatum(int64(idx.OID)),                          // indexrelid
		NewIntDatum(int64(tableOIDForIndex(idx))),            // indrelid
		NewIntDatum(natts),                                   // indnatts
		NewIntDatum(natts),                                   // indnkeyatts
		NewBoolDatum(idx.Unique),                             // indisunique
		NewBoolDatum(false),                                  // indnullsnotdistinct
		NewBoolDatum(idx.Primary),                            // indisprimary
		NewBoolDatum(false),                                  // indisexclusion
		NewBoolDatum(true),                                   // indimmediate
		NewBoolDatum(false),                                  // indisclustered
		NewBoolDatum(true),                                   // indisvalid
		NewBoolDatum(false),                                  // indcheckxmin
		NewBoolDatum(true),                                   // indisready
		NewBoolDatum(true),                                   // indislive
		NewBoolDatum(false),                                  // indisreplident
		NewBytesDatum(pgInt2VectorBytes(attnums)),            // indkey
		NewBytesDatum(pgOIDVectorBytes(zeros32)),             // indcollation
		NewBytesDatum(pgOIDVectorBytes(zeros32)),             // indclass
		NewBytesDatum(pgInt2VectorBytes(zeros16)),            // indoption
		NullDatum,                                            // indexprs (NULL)
		NullDatum,                                            // indpred  (NULL)
	}
}

func tableOIDForIndex(idx *catalog.Index) uint32 {
	if idx.Table != nil {
		return idx.Table.OID
	}
	return 0
}

// pgInt2VectorBytes builds the on-disk int2vector blob.
// Mirrors initdb.int2VectorBytes.
func pgInt2VectorBytes(values []int16) []byte {
	const hdrSize = 24
	total := hdrSize + 2*len(values)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 21) // INT2OID
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(values)))
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	for i, v := range values {
		binary.LittleEndian.PutUint16(buf[hdrSize+2*i:hdrSize+2*i+2], uint16(v))
	}
	return buf
}

// pgOIDVectorBytes builds the on-disk oidvector blob.
// Mirrors initdb.oidVectorBytes.
func pgOIDVectorBytes(oids []uint32) []byte {
	const hdrSize = 24
	total := hdrSize + 4*len(oids)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 26) // OIDOID
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(oids)))
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	for i, o := range oids {
		binary.LittleEndian.PutUint32(buf[hdrSize+4*i:hdrSize+4*i+4], o)
	}
	return buf
}

// --- pg_statistic row builders (M0112) ---

// pgStatisticColumnsPG18 mirrors the canonical PG18 pg_statistic row layout
// (31 columns, matching FormData_pg_statistic in pg_statistic.h).
func pgStatisticColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "starelid", Type: catalog.Type{Name: "oid"}},
		{Name: "staattnum", Type: catalog.Type{Name: "int2"}},
		{Name: "stainherit", Type: catalog.Type{Name: "bool"}},
		{Name: "stanullfrac", Type: catalog.Type{Name: "float4"}},
		{Name: "stawidth", Type: catalog.Type{Name: "int4"}},
		{Name: "stadistinct", Type: catalog.Type{Name: "float4"}},
		{Name: "stakind1", Type: catalog.Type{Name: "int2"}},
		{Name: "stakind2", Type: catalog.Type{Name: "int2"}},
		{Name: "stakind3", Type: catalog.Type{Name: "int2"}},
		{Name: "stakind4", Type: catalog.Type{Name: "int2"}},
		{Name: "stakind5", Type: catalog.Type{Name: "int2"}},
		{Name: "staop1", Type: catalog.Type{Name: "oid"}},
		{Name: "staop2", Type: catalog.Type{Name: "oid"}},
		{Name: "staop3", Type: catalog.Type{Name: "oid"}},
		{Name: "staop4", Type: catalog.Type{Name: "oid"}},
		{Name: "staop5", Type: catalog.Type{Name: "oid"}},
		{Name: "stacoll1", Type: catalog.Type{Name: "oid"}},
		{Name: "stacoll2", Type: catalog.Type{Name: "oid"}},
		{Name: "stacoll3", Type: catalog.Type{Name: "oid"}},
		{Name: "stacoll4", Type: catalog.Type{Name: "oid"}},
		{Name: "stacoll5", Type: catalog.Type{Name: "oid"}},
		{Name: "stanumbers1", Type: catalog.Type{Name: "float4[]"}},
		{Name: "stanumbers2", Type: catalog.Type{Name: "float4[]"}},
		{Name: "stanumbers3", Type: catalog.Type{Name: "float4[]"}},
		{Name: "stanumbers4", Type: catalog.Type{Name: "float4[]"}},
		{Name: "stanumbers5", Type: catalog.Type{Name: "float4[]"}},
		{Name: "stavalues1", Type: catalog.Type{Name: "anyarray"}},
		{Name: "stavalues2", Type: catalog.Type{Name: "anyarray"}},
		{Name: "stavalues3", Type: catalog.Type{Name: "anyarray"}},
		{Name: "stavalues4", Type: catalog.Type{Name: "anyarray"}},
		{Name: "stavalues5", Type: catalog.Type{Name: "anyarray"}},
	}
}

const (
	statisticKindMCV       int16 = 1
	statisticKindHistogram int16 = 2
	// eqOp is the OID for text equality operator (used for staop1 MCV slot).
	eqOp uint32 = 98 // text =
)

// buildUserPGStatisticRow builds a 31-column PG18-canonical pg_statistic row
// for one column of a user table. MCV values are stored in slot 1
// (stakind1=1), histogram in slot 2 (stakind2=2), remaining slots empty.
func buildUserPGStatisticRow(tableOID uint32, attNum int16, stats catalog.ColumnStats) Row {
	// float4 columns (stanullfrac, stadistinct) are encoded as varlena text
	// by EncodeRowPG's "float4" branch; pass KindString.
	nullFracStr := strconv.FormatFloat(stats.NullFrac, 'g', -1, 32)
	var distinctF64 float64
	if stats.NDistinct > 0 {
		distinctF64 = float64(stats.NDistinct)
	}
	distinctStr := strconv.FormatFloat(distinctF64, 'g', -1, 32)

	var stakind1, stakind2 int16
	var staop1 uint32
	var stanumbers1 Datum = NullDatum
	var stavalues1 Datum = NullDatum
	var stanumbers2 Datum = NullDatum
	var stavalues2 Datum = NullDatum

	if len(stats.MCV) > 0 {
		stakind1 = statisticKindMCV
		staop1 = eqOp
		freqs := make([]float32, len(stats.MCV))
		vals := make([]string, len(stats.MCV))
		for i, e := range stats.MCV {
			freqs[i] = float32(e.Frequency)
			vals[i] = e.Value
		}
		stanumbers1 = NewBytesDatum(pgFloat4ArrayBytes(freqs))
		stavalues1 = NewBytesDatum(pgTextArrayBytes(vals))
	}
	if len(stats.Histogram) > 0 {
		stakind2 = statisticKindHistogram
		stavalues2 = NewBytesDatum(pgTextArrayBytes(stats.Histogram))
	}

	return Row{
		NewIntDatum(int64(tableOID)),      // starelid
		NewIntDatum(int64(attNum)),        // staattnum
		NewBoolDatum(false),               // stainherit
		NewStringDatum(nullFracStr),       // stanullfrac (float4 as varlena text)
		NewIntDatum(8),                    // stawidth (avg col width, placeholder)
		NewStringDatum(distinctStr),       // stadistinct (float4 as varlena text)
		NewIntDatum(int64(stakind1)),      // stakind1
		NewIntDatum(int64(stakind2)),      // stakind2
		NewIntDatum(0),                    // stakind3
		NewIntDatum(0),                    // stakind4
		NewIntDatum(0),                    // stakind5
		NewIntDatum(int64(staop1)),        // staop1
		NewIntDatum(0),                    // staop2
		NewIntDatum(0),                    // staop3
		NewIntDatum(0),                    // staop4
		NewIntDatum(0),                    // staop5
		NewIntDatum(0),                    // stacoll1
		NewIntDatum(0),                    // stacoll2
		NewIntDatum(0),                    // stacoll3
		NewIntDatum(0),                    // stacoll4
		NewIntDatum(0),                    // stacoll5
		stanumbers1,                       // stanumbers1
		stanumbers2,                       // stanumbers2
		NullDatum,                         // stanumbers3
		NullDatum,                         // stanumbers4
		NullDatum,                         // stanumbers5
		stavalues1,                        // stavalues1
		stavalues2,                        // stavalues2
		NullDatum,                         // stavalues3
		NullDatum,                         // stavalues4
		NullDatum,                         // stavalues5
	}
}

// pgFloat4ArrayBytes builds a PG _float4 ArrayType blob from a slice of float32.
func pgFloat4ArrayBytes(vals []float32) []byte {
	const hdrSize = 24
	total := hdrSize + 4*len(vals)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)    // ndim
	binary.LittleEndian.PutUint32(buf[8:12], 0)   // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(buf[12:16], 700) // float4 OID
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(vals)))
	binary.LittleEndian.PutUint32(buf[20:24], 1)  // lbound=1
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[hdrSize+4*i:hdrSize+4*i+4], math.Float32bits(v))
	}
	return buf
}

// pgTextArrayBytes builds a PG text[] (OID 25) ArrayType blob from a slice of strings.
// Each element is a varlena with 4-byte header (total_size << 2) followed by UTF-8 bytes.
func pgTextArrayBytes(strs []string) []byte {
	const hdrSize = 24
	totalData := 0
	for _, s := range strs {
		n := len(s) + 4 // 4-byte header + data bytes
		// align to 4 bytes
		totalData += (n + 3) &^ 3
	}
	total := hdrSize + totalData
	buf := make([]byte, 0, total)
	hdr := make([]byte, hdrSize)
	binary.LittleEndian.PutUint32(hdr[12:16], 25) // text OID
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(strs)))
	binary.LittleEndian.PutUint32(hdr[20:24], 1) // lbound=1
	// vl_len_ filled after computing total
	buf = append(buf, hdr...)
	for _, s := range strs {
		raw := []byte(s)
		n := 4 + len(raw)
		elem := make([]byte, (n+3)&^3)
		binary.LittleEndian.PutUint32(elem[0:4], uint32(n)<<2)
		copy(elem[4:], raw)
		buf = append(buf, elem...)
	}
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(buf))<<2)
	return buf
}

// pgTypeColumnsPG18 mirrors initdb.pgTypeColDefs — the canonical PG18 pg_type
// row layout (32 columns, matching the Form_pg_type struct in
// postgres/src/include/catalog/pg_type.h). Used when inserting user-defined
// type rows (e.g. enum types) into the pg_type heap. M0097-0022.
func pgTypeColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "typname", Type: catalog.Type{Name: "name"}},
		{Name: "typnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "typowner", Type: catalog.Type{Name: "oid"}},
		{Name: "typlen", Type: catalog.Type{Name: "int2"}},
		{Name: "typbyval", Type: catalog.Type{Name: "bool"}},
		{Name: "typtype", Type: catalog.Type{Name: "char"}},
		{Name: "typcategory", Type: catalog.Type{Name: "char"}},
		{Name: "typispreferred", Type: catalog.Type{Name: "bool"}},
		{Name: "typisdefined", Type: catalog.Type{Name: "bool"}},
		{Name: "typdelim", Type: catalog.Type{Name: "char"}},
		{Name: "typrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "typsubscript", Type: catalog.Type{Name: "regproc"}},
		{Name: "typelem", Type: catalog.Type{Name: "oid"}},
		{Name: "typarray", Type: catalog.Type{Name: "oid"}},
		{Name: "typinput", Type: catalog.Type{Name: "regproc"}},
		{Name: "typoutput", Type: catalog.Type{Name: "regproc"}},
		{Name: "typreceive", Type: catalog.Type{Name: "regproc"}},
		{Name: "typsend", Type: catalog.Type{Name: "regproc"}},
		{Name: "typmodin", Type: catalog.Type{Name: "regproc"}},
		{Name: "typmodout", Type: catalog.Type{Name: "regproc"}},
		{Name: "typanalyze", Type: catalog.Type{Name: "regproc"}},
		{Name: "typalign", Type: catalog.Type{Name: "char"}},
		{Name: "typstorage", Type: catalog.Type{Name: "char"}},
		{Name: "typnotnull", Type: catalog.Type{Name: "bool"}},
		{Name: "typbasetype", Type: catalog.Type{Name: "oid"}},
		{Name: "typtypmod", Type: catalog.Type{Name: "int4"}},
		{Name: "typndims", Type: catalog.Type{Name: "int4"}},
		{Name: "typcollation", Type: catalog.Type{Name: "oid"}},
		{Name: "typdefaultbin", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "typdefault", Type: catalog.Type{Name: "text"}},
		{Name: "typacl", Type: catalog.Type{Name: "aclitem[]"}},
	}
}

// buildUserPGTypeRowForEnum builds a 32-column pg_type Row for a user-defined
// enum type. The row is PG18-canonical so that a PG18 standby attaching to a
// goopg basebackup can read it correctly. M0097-0022.
func buildUserPGTypeRowForEnum(et *catalog.EnumType) Row {
	return Row{
		NewIntDatum(int64(et.OID)),          // oid
		NewStringDatum(et.Name),             // typname (name type)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),  // typowner
		NewIntDatum(4),                      // typlen (enum = 4 bytes, like oid)
		NewBoolDatum(false),                 // typbyval
		NewStringDatum("e"),                 // typtype = 'e' (enum)
		NewStringDatum("E"),                 // typcategory = TYPCATEGORY_ENUM
		NewBoolDatum(false),                 // typispreferred
		NewBoolDatum(true),                  // typisdefined
		NewStringDatum(","),                 // typdelim
		NewIntDatum(0),                      // typrelid
		NewIntDatum(0),                      // typsubscript
		NewIntDatum(0),                      // typelem
		NewIntDatum(0),                      // typarray
		NewIntDatum(0),                      // typinput
		NewIntDatum(0),                      // typoutput
		NewIntDatum(0),                      // typreceive
		NewIntDatum(0),                      // typsend
		NewIntDatum(0),                      // typmodin
		NewIntDatum(0),                      // typmodout
		NewIntDatum(0),                      // typanalyze
		NewStringDatum("i"),                 // typalign = 'i' (int-aligned, 4-byte)
		NewStringDatum("p"),                 // typstorage = 'p' (plain)
		NewBoolDatum(false),                 // typnotnull
		NewIntDatum(0),                      // typbasetype
		NewIntDatum(-1),                     // typtypmod
		NewIntDatum(0),                      // typndims
		NewIntDatum(0),                      // typcollation
		NullDatum,                           // typdefaultbin (NULL)
		NullDatum,                           // typdefault (NULL)
		NullDatum,                           // typacl (NULL)
	}
}
