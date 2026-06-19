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
	"strings"

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

// pgAttributeColumnsPG18 mirrors initdb.pgAttrColDefs — the goopg pg_attribute
// row layout (25 columns). attstattarget is appended last (always NULL); see
// catalog.PGAttributeColumns for why it is not at its PG18-canonical position.
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
		{Name: "attstattarget", Type: catalog.Type{Name: "int2"}},
	}
}

// userTypeAttrs captures the four pg_type-derived properties needed to write
// a PG18-canonical pg_attribute row: typlen (attlen), typbyval (attbyval),
// typalign (attalign), typstorage (attstorage).
type userTypeAttrs struct {
	TypLen       int16 // -1 == variable-length
	TypByVal     bool
	TypAlign     byte // 'c' | 's' | 'i' | 'd'
	TypStorage   byte // 'p' | 'e' | 'x' | 'm'
	TypCollation uint32
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
	case catalog.OIDTimeTZ: // 1266 timetz -- pg_type.dat: typlen 12, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 12, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTimestamp: // 1114
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTimestampTZ: // 1184
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDNumeric: // 1700
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'm'}
	case catalog.OIDUUID: // 2950 -- pg_type.dat: typlen 16, typbyval f, typalign 'c', typstorage 'p'
		return userTypeAttrs{TypLen: 16, TypByVal: false, TypAlign: 'c', TypStorage: 'p'}
	case catalog.OIDJSON: // 114 json -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDJsonb: // 3802 jsonb -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDJsonpath: // 4072 jsonpath -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDRefcursor: // 1790 refcursor -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDInterval: // 1186 interval -- pg_type.dat: typlen 16, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 16, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDInet: // 869 inet -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'm'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'm'}
	case catalog.OIDCidr: // 650 cidr -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'm'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'm'}
	case catalog.OIDMacaddr: // 829 macaddr -- pg_type.dat: typlen 6, typbyval f, typalign 'i', typstorage 'p'
		return userTypeAttrs{TypLen: 6, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDMacaddr8: // 774 macaddr8 -- pg_type.dat: typlen 8, typbyval f, typalign 'i', typstorage 'p'
		return userTypeAttrs{TypLen: 8, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDPoint: // 600 point -- pg_type.dat: typlen 16, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 16, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDLseg: // 601 lseg -- pg_type.dat: typlen 32, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 32, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDPath: // 602 path -- pg_type.dat: typlen -1, typbyval f, typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDBox: // 603 box -- pg_type.dat: typlen 32, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 32, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDPolygon: // 604 polygon -- pg_type.dat: typlen -1, typbyval f, typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDLine: // 628 line -- pg_type.dat: typlen 24, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 24, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDCircle: // 718 circle -- pg_type.dat: typlen 24, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 24, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTsvector: // 3614 tsvector -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDTsquery: // 3615 tsquery -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'p'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDXML: // 142 xml -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDMoney: // 790 money -- pg_type.dat: typlen 8, typbyval t, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDBit: // 1560 bit -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDVarbit: // 1562 varbit -- pg_type.dat: typlen -1, typbyval f, typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDPgLsn: // 3220 pg_lsn -- pg_type.dat: typlen 8, typbyval t, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTxidSnapshot: // 2970 txid_snapshot -- pg_type.dat: typlen -1, typbyval f, typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDPgSnapshot: // 5038 pg_snapshot -- pg_type.dat: typlen -1, typbyval f, typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDXid8: // 5069 xid8 -- pg_type.dat: typlen 8, typbyval t, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 8, TypByVal: true, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDTid: // 27 tid -- pg_type.dat: typlen 6, typbyval f, typalign 's', typstorage 'p'
		return userTypeAttrs{TypLen: 6, TypByVal: false, TypAlign: 's', TypStorage: 'p'}
	case catalog.OIDXid: // 28 xid -- pg_type.dat: typlen 4, typbyval t, typalign 'i', typstorage 'p'
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDCid: // 29 cid -- pg_type.dat: typlen 4, typbyval t, typalign 'i', typstorage 'p'
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	// DU-002 slice 80: the OID-reference ("reg*") family. All share oid's shape:
	// pg_type.dat typlen 4, typbyval t, typalign 'i', typstorage 'p'.
	case catalog.OIDRegproc, // 24
		catalog.OIDRegprocedure,  // 2202
		catalog.OIDRegoper,       // 2203
		catalog.OIDRegoperator,   // 2204
		catalog.OIDRegclass,      // 2205
		catalog.OIDRegtype,       // 2206
		catalog.OIDRegconfig,     // 3734
		catalog.OIDRegdictionary, // 3769
		catalog.OIDRegnamespace,  // 4089
		catalog.OIDRegrole,       // 4096
		catalog.OIDRegcollation:  // 4191
		return userTypeAttrs{TypLen: 4, TypByVal: true, TypAlign: 'i', TypStorage: 'p'}
	// DU-002 slice 81: int2vector/oidvector -- pg_type.dat: typlen -1, typbyval f,
	// typalign 'i', typstorage 'p' (plain varlena, no compression/TOAST).
	case catalog.OIDInt2vector, // 22
		catalog.OIDOidvector: // 30
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case catalog.OIDAclitem: // 1033 aclitem -- pg_type.dat: typlen 16, typbyval f, typalign 'd', typstorage 'p'
		return userTypeAttrs{TypLen: 16, TypByVal: false, TypAlign: 'd', TypStorage: 'p'}
	case catalog.OIDArrayBool: // 1000 _bool -- pg_type.dat: typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayBytea: // 1001 _bytea -- element bytea typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayInt2: // 1005 _int2 -- pg_type.dat: typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayInt4: // 1007 _int4
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayText: // 1009 _text -- element collation defaults like text
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
	case catalog.OIDArrayInt8: // 1016 _int8 -- typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayNumeric: // 1231 _numeric -- element typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayFloat8: // 1022 _float8 -- element float8 typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayDate: // 1182 _date -- element date typalign 'i'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayTimestamp: // 1115 _timestamp -- element timestamp typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayFloat4: // 1021 _float4 -- element float4 typalign 'i'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayTime: // 1183 _time -- element time typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayTimeTZ: // 1270 _timetz -- element timetz typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayTimestampTZ: // 1185 _timestamptz -- element timestamptz typalign 'd'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayUUID: // 2951 _uuid -- element uuid typalign 'c' -> array 'i'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayVarChar: // 1015 _varchar -- element varchar collation defaults
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
	case catalog.OIDArrayBpChar: // 1014 _bpchar -- element bpchar collation defaults
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: defaultCollationOID}
	case catalog.OIDArrayChar: // 1002 _char -- element "char" typcollation 0 (no collation)
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayOID: // 1028 _oid -- element oid typalign 'i', no collation
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayJSON: // 199 _json -- element json typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayJsonb: // 3807 _jsonb -- element jsonb typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayJsonpath: // 4073 _jsonpath -- element jsonpath typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayRefcursor: // 2201 _refcursor -- element refcursor typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayInterval: // 1187 _interval -- element interval typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayInet: // 1041 _inet -- element inet typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayCidr: // 651 _cidr -- element cidr typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayMacaddr: // 1040 _macaddr -- element macaddr typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayMacaddr8: // 775 _macaddr8 -- element macaddr8 typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayPoint: // 1017 _point -- element point typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayLseg: // 1018 _lseg -- element lseg typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayPath: // 1019 _path -- element path typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayBox: // 1020 _box -- element box typalign 'd', typstorage 'x' (typdelim ';')
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayPolygon: // 1027 _polygon -- element polygon typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayLine: // 629 _line -- element line typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayCircle: // 719 _circle -- element circle typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayTsvector: // 3643 _tsvector -- element tsvector typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayTsquery: // 3645 _tsquery -- element tsquery typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayXML: // 143 _xml -- element xml typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayMoney: // 791 _money -- element money typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayBit: // 1561 _bit -- element bit typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayVarbit: // 1563 _varbit -- element varbit typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayPgLsn: // 3221 _pg_lsn -- element pg_lsn typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayTxidSnapshot: // 2949 _txid_snapshot -- element txid_snapshot typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayPgSnapshot: // 5039 _pg_snapshot -- element pg_snapshot typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayXid8: // 271 _xid8 -- element xid8 typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case catalog.OIDArrayTid: // 1010 _tid -- element tid typalign 's' -> array 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayXid: // 1011 _xid -- element xid typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayCid: // 1012 _cid -- element cid typalign 'i', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	// DU-002 slice 80: the OID-reference ("reg*") array types. Each element is a
	// 4-byte oid alias (typalign 'i'); the array is varlena typstorage 'x'.
	case catalog.OIDArrayRegproc, // 1008 _regproc
		catalog.OIDArrayRegprocedure,  // 2207 _regprocedure
		catalog.OIDArrayRegoper,       // 2208 _regoper
		catalog.OIDArrayRegoperator,   // 2209 _regoperator
		catalog.OIDArrayRegclass,      // 2210 _regclass
		catalog.OIDArrayRegtype,       // 2211 _regtype
		catalog.OIDArrayRegconfig,     // 3735 _regconfig
		catalog.OIDArrayRegdictionary, // 3770 _regdictionary
		catalog.OIDArrayRegnamespace,  // 4090 _regnamespace
		catalog.OIDArrayRegrole,       // 4097 _regrole
		catalog.OIDArrayRegcollation:  // 4192 _regcollation
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	// DU-002 slice 81: _int2vector (1006) / _oidvector (1013) -- element typalign
	// 'i'; the array is varlena typstorage 'x'.
	case catalog.OIDArrayInt2vector, // 1006 _int2vector
		catalog.OIDArrayOidvector: // 1013 _oidvector
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case catalog.OIDArrayName: // 1003 _name -- element name typcollation 'C'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x', TypCollation: cCollationOID}
	case catalog.OIDArrayAclitem: // 1034 _aclitem -- pg_type.dat: typalign 'd', typstorage 'x'
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
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

// collationNameToOID resolves a bare pg_collation.collname (as captured from a
// column's `COLLATE <name>` clause) to its BKI-pinned OID. The mapping mirrors
// the seven built-in collations populated in catalog.pgCollation.VirtualRows
// (DU-002 slice 187) and PG18's pg_collation.dat. Returns 0 for an unknown name
// so callers leave attcollation at the type default. DU-002 slice 188.
func collationNameToOID(name string) uint32 {
	switch name {
	case "default":
		return 100
	case "C":
		return 950
	case "POSIX":
		return 951
	case "pg_c_utf8":
		return 811
	case "ucs_basic":
		return 962
	case "unicode":
		return 963
	case "pg_unicode_fast":
		return 6411
	default:
		return 0
	}
}

// buildUserPGClassRow constructs a 34-column PG18-canonical pg_class row for
// a user-defined table. Mirrors initdb.pgClassRow's per-column ordering and
// default values.
func buildUserPGClassRow(cat catalog.Catalog, tbl *catalog.Table) Row {
	relkind := "r"
	if tbl.PartitionMethod != "" {
		relkind = "p" // partitioned table
	}
	relfilenode := int64(tbl.OID)
	if relkind == "p" {
		relfilenode = 0 // partitioned tables have no physical storage
	}
	isPartition := tbl.PartitionParentOID != 0
	// relpersistence: 'u' for UNLOGGED tables, 'p' for permanent. pg_dump keys
	// `CREATE UNLOGGED TABLE` off relpersistence == RELPERSISTENCE_UNLOGGED, so
	// hardcoding 'p' silently demoted an UNLOGGED table to a logged one in the
	// dump. (TEMP tables are session-local and never reach the on-disk catalog,
	// so 't' is not produced here.)
	relpersistence := "p"
	if tbl.Unlogged {
		relpersistence = "u"
	}
	// relpartbound: a partition child carries its `FOR VALUES …` bound, which
	// pg_dump reads via pg_get_expr(relpartbound, oid) and re-emits as
	// `ALTER TABLE ONLY parent ATTACH PARTITION child <bound>`. Hardcoding ""
	// silently dropped the bound, so the restored child would attach with an
	// empty (invalid) bound. A parent partitioned table has no bound (""),
	// matching PG. Mirrors catalog.go's VirtualRows pg_class path (sibling).
	relpartbound := ""
	if isPartition && len(tbl.PartitionBounds) > 0 {
		relpartbound = catalog.FormatPartitionBound(tbl.PartitionBounds[0])
	}
	return Row{
		NewIntDatum(int64(tbl.OID)),                                // oid
		NewStringDatum(tbl.Name),                                   // relname (name)
		NewIntDatum(int64(namespaceOIDForSchema(cat, tbl.Schema))), // relnamespace
		NewIntDatum(0),                                             // reltype (no composite type seeded yet)
		NewIntDatum(0),                                             // reloftype
		NewIntDatum(bootstrapSuperuserOID),                         // relowner
		NewIntDatum(pgHeapAccessMethodOID),                         // relam
		NewIntDatum(relfilenode),                                   // relfilenode
		NewIntDatum(0),                                             // reltablespace (default per-db tablespace)
		NewIntDatum(0),                                             // relpages
		NewIntDatum(0),                                             // reltuples (float4 here; stored 0 == 0.0)
		NewIntDatum(0),                                             // relallvisible
		NewIntDatum(0),                                             // relallfrozen
		NewIntDatum(0),                                             // reltoastrelid
		NewBoolDatum(false),                                        // relhasindex (updated by CREATE INDEX later)
		NewBoolDatum(false),                                        // relisshared
		NewStringDatum(relpersistence),                             // relpersistence
		NewStringDatum(relkind),                                    // relkind
		NewIntDatum(int64(len(tbl.Columns))),                       // relnatts
		NewIntDatum(0),                                             // relchecks
		NewBoolDatum(false),                                        // relhasrules
		NewBoolDatum(false),                                        // relhastriggers
		NewBoolDatum(false),                                        // relhassubclass
		NewBoolDatum(false),                                        // relrowsecurity
		NewBoolDatum(false),                                        // relforcerowsecurity
		NewBoolDatum(true),                                         // relispopulated
		NewStringDatum("n"),                                        // relreplident (REPLICA_IDENTITY_DEFAULT)
		NewBoolDatum(isPartition),                                  // relispartition
		NewIntDatum(0),                                             // relrewrite
		NewIntDatum(minFrozenXID),                                  // relfrozenxid
		NewIntDatum(minFrozenMXID),                                 // relminmxid
		NewStringDatum("{}"),                                       // relacl (encoded as empty aclitem[] ArrayType)
		NewStringDatum("{}"),                                       // reloptions (encoded as empty text[] ArrayType)
		NewStringDatum(relpartbound),                               // relpartbound (FOR VALUES … for partition children)
	}
}

// indexPersistence returns the relpersistence char an index inherits from its
// owning table ('u' for an index on an UNLOGGED table, 'p' otherwise). An index
// always shares its table's persistence in PG, so this keeps the two pg_class
// rows consistent for a standby / pg_amcheck reading the catalog.
func indexPersistence(idx *catalog.Index) string {
	if idx.Table != nil && idx.Table.Unlogged {
		return "u"
	}
	return "p"
}

// buildUserPGClassRowForIndex constructs the 34-column PG18-canonical
// pg_class row for a user-defined index.
func buildUserPGClassRowForIndex(cat catalog.Catalog, idx *catalog.Index) Row {
	natts := int64(len(idx.Columns))
	return Row{
		NewIntDatum(int64(idx.OID)),
		NewStringDatum(idx.Name),
		NewIntDatum(int64(namespaceOIDForSchema(cat, idx.Schema))),
		NewIntDatum(0),                        // reltype
		NewIntDatum(0),                        // reloftype
		NewIntDatum(bootstrapSuperuserOID),    // relowner
		NewIntDatum(pgBTreeAccessMethodOID),   // relam
		NewIntDatum(int64(idx.OID)),           // relfilenode
		NewIntDatum(0),                        // reltablespace
		NewIntDatum(0),                        // relpages
		NewIntDatum(0),                        // reltuples
		NewIntDatum(0),                        // relallvisible
		NewIntDatum(0),                        // relallfrozen
		NewIntDatum(0),                        // reltoastrelid
		NewBoolDatum(false),                   // relhasindex (indexes never have indexes themselves)
		NewBoolDatum(false),                   // relisshared
		NewStringDatum(indexPersistence(idx)), // relpersistence (follows the owning table)
		NewStringDatum("i"),                   // relkind
		NewIntDatum(natts),                    // relnatts
		NewIntDatum(0),                        // relchecks
		NewBoolDatum(false),                   // relhasrules
		NewBoolDatum(false),                   // relhastriggers
		NewBoolDatum(false),                   // relhassubclass
		NewBoolDatum(false),                   // relrowsecurity
		NewBoolDatum(false),                   // relforcerowsecurity
		NewBoolDatum(true),                    // relispopulated
		NewStringDatum("n"),                   // relreplident
		NewBoolDatum(false),                   // relispartition
		NewIntDatum(0),                        // relrewrite
		NewIntDatum(minFrozenXID),             // relfrozenxid
		NewIntDatum(minFrozenMXID),            // relminmxid
		NewStringDatum("{}"),                  // relacl
		NewStringDatum("{}"),                  // reloptions
		NewStringDatum(""),                    // relpartbound
	}
}

// storageNameToAttCode maps a column's storage-strategy name (as recorded by
// ALTER COLUMN ... SET STORAGE: "plain"/"main"/"external"/"extended") to the
// single-char pg_attribute.attstorage code PG uses ('p'/'m'/'e'/'x', matching
// TYPSTORAGE_* in postgres/src/include/catalog/pg_type.h). Returns 0 for an
// empty or unrecognized name, signalling "no override — use the type default".
func storageNameToAttCode(name string) byte {
	switch strings.ToLower(name) {
	case "plain":
		return 'p'
	case "main":
		return 'm'
	case "external":
		return 'e'
	case "extended":
		return 'x'
	default:
		return 0
	}
}

// compressionNameToAttCode maps a column's per-column compression-method name (as
// recorded by `COMPRESSION <method>` / `ALTER COLUMN ... SET COMPRESSION`:
// "pglz"/"lz4") to the single-char pg_attribute.attcompression code PG uses
// ('p'/'l', matching TOAST_PGLZ_COMPRESSION / TOAST_LZ4_COMPRESSION in
// postgres/src/include/access/toast_compression.h). Returns 0 for an empty or
// unrecognized name, signalling "no explicit method" — encoded as '\0'
// (InvalidCompressionMethod), for which pg_dump emits no SET COMPRESSION clause.
func compressionNameToAttCode(name string) byte {
	switch strings.ToLower(name) {
	case "pglz":
		return 'p'
	case "lz4":
		return 'l'
	default:
		return 0
	}
}

// buildUserPGAttributeRow constructs a 25-column pg_attribute row for a
// user-defined column (attstattarget appended last as NULL).
func buildUserPGAttributeRow(cat catalog.Catalog, tbl *catalog.Table, col catalog.Column) Row {
	typOID := catalog.TypeNameToOID(col.Type.Name)
	// Disambiguate the single-byte "char" type (OID 18) from bpchar (1042):
	// both arrive as catalog type name "char", but only the bpchar-equivalent
	// unquoted `char` carries a length arg ([1]). A quoted `"char"` (the real
	// catalog type) has no args, so TypeNameToOID's name-only lookup wrongly
	// folds it to bpchar. Remap so pg_attribute reports atttypid=18 and the
	// column round-trips through pg_dump as "char". DU-002 slice 87.
	if typOID == catalog.OIDBpChar && col.Type.Name == "char" && len(col.Type.Args) == 0 {
		typOID = catalog.OIDChar
	}
	// SERIAL/BIGSERIAL/SMALLSERIAL are syntactic sugar: the stored column is a
	// plain integer with a nextval() default + an owned sequence. pg_dump never
	// emits "serial" — it renders the base integer type via format_type(atttypid)
	// and dumps the DEFAULT + CREATE SEQUENCE separately. goopg keeps the catalog
	// type name as the serial spelling (the INSERT auto-gen path keys on it), so
	// remap atttypid here (and the physical attrs derived from it below) to int4/
	// int8/int2 like upstream. DU-002 slice 121.
	switch strings.ToLower(col.Type.Name) {
	case "serial", "serial4":
		typOID = catalog.OIDInt4
	case "bigserial", "serial8":
		typOID = catalog.OIDInt8
	case "smallserial", "serial2":
		typOID = catalog.OIDInt2
	}
	// A column whose declared type is a user-defined enum resolves to the text
	// fallback above (TypeNameToOID knows only built-ins). Re-resolve it to the
	// enum's dynamically-allocated pg_type OID so pg_attribute.atttypid points
	// at the enum and pg_dump's format_type(atttypid, atttypmod) renders the
	// column as the enum type rather than `text`. Only the text fallback is
	// reconsidered, so built-in columns are untouched (no enum can shadow a
	// built-in name). DU-002 slice 88 (scalar), slice 89 (array): a `mood[]`
	// column resolves to the enum's auto-generated array OID (et.ArrayOID).
	enumOID := uint32(0)
	enumArrayOID := uint32(0)
	if cat != nil && typOID == catalog.OIDText {
		if et, ok := cat.LookupEnum(col.Type.Name); ok {
			if col.Type.IsArray {
				enumArrayOID = et.ArrayOID
			} else {
				typOID = et.OID
				enumOID = et.OID
			}
		}
	}
	// A column whose declared type is a user-defined DOMAIN is stored with its
	// type name already resolved to the BASE type (catalog.ResolveColumnType at
	// CREATE TABLE), with the original domain name preserved in DeclaredTypeName.
	// Re-resolve it to the domain's dynamically-allocated pg_type OID so
	// pg_attribute.atttypid points at the domain and pg_dump's format_type
	// renders the column as the domain name rather than the base type. The
	// physical attributes (attlen/attbyval/attalign/attstorage/attcollation)
	// follow the BASE type — the current typOID, since Type.Name is the resolved
	// base — because a domain stores values exactly as its base. Scalar only in
	// this slice. DU-002 slice 90.
	domainBaseOID := uint32(0)
	if cat != nil && col.DeclaredTypeName != "" && !col.Type.IsArray {
		if d, ok := cat.LookupDomain(col.DeclaredTypeName); ok {
			domainBaseOID = typOID
			typOID = d.OID
		}
	}
	// atttypmod carries the ELEMENT typmod even for array columns; compute it
	// from the base OID before remapping typOID to the array (_typename) OID.
	typmod := pgAttTypmod(typOID, col.Type.Args)
	attndims := int64(0)
	if col.Type.IsArray {
		switch {
		case enumArrayOID != 0:
			// Enum array OIDs are dynamic, so ArrayOIDForBase can't case on them.
			typOID = enumArrayOID
			attndims = 1
		default:
			if aoid := catalog.ArrayOIDForBase(typOID); aoid != 0 {
				typOID = aoid
				attndims = 1
			}
		}
	}
	attrs := userTypeAttrsForOID(typOID)
	switch {
	case enumOID != 0:
		// Enum OIDs are dynamic, so userTypeAttrsForOID can't case on them.
		// Mirror buildUserPGTypeRowForEnum's pg_type shape: a 4-byte,
		// int-aligned, plain-storage, non-collatable value (like oid).
		attrs = userTypeAttrs{TypLen: 4, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case enumArrayOID != 0:
		// An enum's array type is a standard varlena array: -1 length,
		// int-aligned (matching the 4-byte enum element), extended storage.
		attrs = userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case domainBaseOID != 0:
		// A domain inherits its base type's physical layout. DU-002 slice 90.
		attrs = userTypeAttrsForOID(domainBaseOID)
	}
	// A per-column storage override (ALTER COLUMN ... SET STORAGE) shadows the
	// type's default storage in pg_attribute.attstorage. pg_dump compares
	// attstorage against the column type's typstorage and emits an
	// `ALTER TABLE ONLY ... ALTER COLUMN ... SET STORAGE <mode>` only when the
	// two differ (pg_dump.c dumpTableSchema). attstorage echoed the type default
	// unconditionally, so a SET STORAGE was silently dropped from the dump even
	// though the executor recorded it on the catalog column. DU-002 slice 182.
	attStorageChar := attrs.TypStorage
	if c := storageNameToAttCode(col.Storage); c != 0 {
		attStorageChar = c
	}
	// A per-column compression override (COMPRESSION <method> / ALTER COLUMN ...
	// SET COMPRESSION) sets pg_attribute.attcompression to 'p' (pglz) or 'l'
	// (lz4). pg_dump re-emits a `SET COMPRESSION <method>` for either; the PG18
	// default is '\0' (encoded as "") meaning "use default_toast_compression",
	// for which pg_dump emits nothing. attcompression was hardcoded to "" so a
	// declared method was silently dropped from the dump. DU-002 slice 183.
	attCompressionStr := ""
	if c := compressionNameToAttCode(col.Compression); c != 0 {
		attCompressionStr = string(c)
	}
	// A per-column statistics target override (ALTER COLUMN ... SET STATISTICS
	// <n>) sets pg_attribute.attstattarget to the value. pg_dump emits an
	// `ALTER TABLE ONLY ... SET STATISTICS <n>` whenever attstattarget >= 0
	// (pg_dump.c dumpTableSchema); the PG18 default is NULL (encoded as -1 to
	// clients) for which pg_dump emits nothing. attstattarget was hardcoded to
	// NULL so a declared target was silently dropped from the dump. DU-002 slice 184.
	attStatTargetDatum := NullDatum
	if col.StatTarget != nil && *col.StatTarget >= 0 {
		attStatTargetDatum = NewIntDatum(int64(*col.StatTarget))
	}
	// Per-column attribute options (ALTER COLUMN ... SET (opt=value, …)) are
	// stored in pg_attribute.attoptions as a text[] array. pg_dump renders
	// `array_to_string(a.attoptions, ', ')` and emits `ALTER TABLE ONLY ...
	// ALTER COLUMN ... SET (...)` whenever the result is non-empty. goopg's
	// executor (array_to_string → parseTextArray) consumes the PG text-array
	// literal `{n_distinct=0.5,…}`; an empty list stays NULL so pg_dump skips.
	// DU-002 slice 185.
	attOptionsDatum := NullDatum
	if len(col.Options) > 0 {
		attOptionsDatum = NewStringDatum("{" + strings.Join(col.Options, ",") + "}")
	}
	// A per-column explicit collation (`COLLATE <name>`) shadows the type's
	// typcollation in pg_attribute.attcollation. pg_dump's getTableAttrs query
	// reports attcollation only when `a.attcollation <> t.typcollation`, and
	// dumpTableSchema then re-emits a `COLLATE <schema>.<name>` clause inline.
	// attcollation echoed the type collation unconditionally, so a declared
	// COLLATE was silently dropped from the dump. Only override for a collatable
	// type (typcollation != 0) and a name that resolves to a known collation OID
	// — a COLLATE on a non-collatable type is a CREATE-time error in PG, so we
	// never persist a bogus OID. DU-002 slice 188.
	attCollationOID := attrs.TypCollation
	if col.Collation != "" && attrs.TypCollation != 0 {
		if oid := collationNameToOID(col.Collation); oid != 0 {
			attCollationOID = oid
		}
	}
	return Row{
		NewIntDatum(int64(tbl.OID)),            // attrelid
		NewStringDatum(col.Name),               // attname (name)
		NewIntDatum(int64(typOID)),             // atttypid
		NewIntDatum(int64(attrs.TypLen)),       // attlen
		NewIntDatum(int64(col.Ordinal + 1)),    // attnum (1-based)
		NewIntDatum(typmod),                    // atttypmod
		NewIntDatum(attndims),                  // attndims
		NewBoolDatum(attrs.TypByVal),           // attbyval
		NewStringDatum(string(attrs.TypAlign)), // attalign
		NewStringDatum(string(attStorageChar)), // attstorage
		NewStringDatum(attCompressionStr),      // attcompression ('\0' default; 'p'/'l' override)
		NewBoolDatum(col.NotNull),              // attnotnull
		NewBoolDatum(col.DefaultExpr != nil || col.GeneratedExpr != "" || catalog.IsSerialTypeName(col.Type.Name)), // atthasdef (generated cols + SERIAL nextval defaults carry their expr in pg_attrdef too)
		NewBoolDatum(false),                  // atthasmissing
		NewStringDatum(attIdentityFor(col)),  // attidentity
		NewStringDatum(attGeneratedFor(col)), // attgenerated
		NewBoolDatum(false),                  // attisdropped
		NewBoolDatum(!col.Inherited),         // attislocal
		func() Datum {
			if col.Inherited {
				return NewIntDatum(1)
			}
			return NewIntDatum(0)
		}(), // attinhcount
		NewIntDatum(int64(attCollationOID)), // attcollation (type default; per-column COLLATE override, slice 188)
		// attacl / attoptions / attfdwoptions / attmissingval are nullable
		// varlena columns; PG18 stores NULL when unset. NullDatum signals
		// EncodeRowPG to skip the column and the bitmap helper to clear
		// its bit. attstattarget (last) is NULL by default but carries the
		// per-column SET STATISTICS override when one is set (DU-002 slice 184).
		NullDatum,          // attacl
		attOptionsDatum,    // attoptions (NULL default; text[] literal when set)
		NullDatum,          // attfdwoptions
		NullDatum,          // attmissingval
		attStatTargetDatum, // attstattarget (NULL default; integer override)
	}
}

// pgAttTypmod computes the PG-canonical pg_attribute.atttypmod from a column's
// declared type arguments (e.g. the (10,2) in numeric(10,2)). It mirrors the
// per-type typmodin functions in PostgreSQL so that catalog clients — notably
// pg_dump's getTableAttrs, which renders the column type via
// format_type(atttypid, atttypmod) — recover the exact declared type. Without
// this, every typmod-bearing column dumped as its bare base type (numeric(10,2)
// → numeric, varchar(64) → character varying), a schema-fidelity loss.
//
// VARHDRSZ (4) is added for the varlena length-prefixed types, matching
// anychar_typmodin (varchar/bpchar) and numerictypmodin in the backend; the
// no-argument / unrecognised case returns -1 ("no modifier"), as PG does.
// formatTypeOID decodes the same encoding. (DU-002 slice 48.)
func pgAttTypmod(typOID uint32, args []int64) int64 {
	switch typOID {
	case 1700: // numeric/decimal: ((precision<<16) | scale) + VARHDRSZ
		switch len(args) {
		case 1:
			return ((args[0] << 16) & 0xffffffff) + 4
		case 2:
			return ((args[0]<<16 | args[1]&0xffff) & 0xffffffff) + 4
		}
	case 1042, 1043: // character(n) / character varying(n): n + VARHDRSZ
		if len(args) >= 1 {
			return args[0] + 4
		}
	case 1560, 1562: // bit(n) / bit varying(n): the raw bit length, no VARHDRSZ
		// (anybit_typmodin stores the length directly).
		if len(args) >= 1 {
			return args[0]
		}
	}
	return -1
}

// attGeneratedFor returns the attgenerated discriminator: 'v' for a VIRTUAL
// generated column (`GENERATED ALWAYS AS (expr) VIRTUAL`, and the bare
// `GENERATED ALWAYS AS (expr)` form, whose PG18 default is VIRTUAL), 's' for an
// explicit STORED generated column, and '\0' (empty string, encoded by
// EncodeRowPG as a single zero byte) for an ordinary column. pg_dump reads this
// to choose between `GENERATED ALWAYS AS (expr)` (virtual) and `… STORED`.
// goopg materializes every generated column on write regardless of the declared
// strategy; the discriminator exists for catalog/dump fidelity only (DU-002
// slice 194).
func attGeneratedFor(col catalog.Column) string {
	if col.GeneratedExpr == "" {
		return ""
	}
	if col.GeneratedVirtual {
		return "v"
	}
	return "s"
}

// attIdentityFor returns the attidentity discriminator: 'a' for GENERATED ALWAYS
// AS IDENTITY columns, 'd' for GENERATED BY DEFAULT AS IDENTITY, '\0' (empty
// string) for ordinary columns. pg_dump reads attidentity to decide whether a
// column is an identity column and which keyword (ALWAYS / BY DEFAULT) to emit
// in its `ALTER TABLE ... ADD GENERATED ... AS IDENTITY` clause. The matching
// pg_depend deptype='i' row (synthesized in catalog.dependVirtualRows) is what
// makes pg_dump treat the backing sequence as an identity sequence rather than a
// plain OWNED BY sequence. M0110-0001 (DU-002 slice 120).
func attIdentityFor(col catalog.Column) string {
	if col.IdentityColumn {
		if col.IdentityAlways {
			return "a"
		}
		return "d"
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
		NewIntDatum(int64(idx.OID)),               // indexrelid
		NewIntDatum(int64(tableOIDForIndex(idx))), // indrelid
		NewIntDatum(natts),                        // indnatts
		NewIntDatum(natts),                        // indnkeyatts
		NewBoolDatum(idx.Unique),                  // indisunique
		NewBoolDatum(idx.NullsNotDistinct),        // indnullsnotdistinct (DU-002 slice 134)
		NewBoolDatum(idx.Primary),                 // indisprimary
		NewBoolDatum(false),                       // indisexclusion
		NewBoolDatum(true),                        // indimmediate
		NewBoolDatum(false),                       // indisclustered
		NewBoolDatum(true),                        // indisvalid
		NewBoolDatum(false),                       // indcheckxmin
		NewBoolDatum(true),                        // indisready
		NewBoolDatum(true),                        // indislive
		NewBoolDatum(false),                       // indisreplident
		NewBytesDatum(pgInt2VectorBytes(attnums)), // indkey
		NewBytesDatum(pgOIDVectorBytes(zeros32)),  // indcollation
		NewBytesDatum(pgOIDVectorBytes(zeros32)),  // indclass
		NewBytesDatum(pgInt2VectorBytes(zeros16)), // indoption
		NullDatum, // indexprs (NULL)
		NullDatum, // indpred  (NULL)
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
		NewIntDatum(int64(tableOID)), // starelid
		NewIntDatum(int64(attNum)),   // staattnum
		NewBoolDatum(false),          // stainherit
		NewStringDatum(nullFracStr),  // stanullfrac (float4 as varlena text)
		NewIntDatum(8),               // stawidth (avg col width, placeholder)
		NewStringDatum(distinctStr),  // stadistinct (float4 as varlena text)
		NewIntDatum(int64(stakind1)), // stakind1
		NewIntDatum(int64(stakind2)), // stakind2
		NewIntDatum(0),               // stakind3
		NewIntDatum(0),               // stakind4
		NewIntDatum(0),               // stakind5
		NewIntDatum(int64(staop1)),   // staop1
		NewIntDatum(0),               // staop2
		NewIntDatum(0),               // staop3
		NewIntDatum(0),               // staop4
		NewIntDatum(0),               // staop5
		NewIntDatum(0),               // stacoll1
		NewIntDatum(0),               // stacoll2
		NewIntDatum(0),               // stacoll3
		NewIntDatum(0),               // stacoll4
		NewIntDatum(0),               // stacoll5
		stanumbers1,                  // stanumbers1
		stanumbers2,                  // stanumbers2
		NullDatum,                    // stanumbers3
		NullDatum,                    // stanumbers4
		NullDatum,                    // stanumbers5
		stavalues1,                   // stavalues1
		stavalues2,                   // stavalues2
		NullDatum,                    // stavalues3
		NullDatum,                    // stavalues4
		NullDatum,                    // stavalues5
	}
}

// pgFloat4ArrayBytes builds a PG _float4 ArrayType blob from a slice of float32.
func pgFloat4ArrayBytes(vals []float32) []byte {
	const hdrSize = 24
	total := hdrSize + 4*len(vals)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)     // ndim
	binary.LittleEndian.PutUint32(buf[8:12], 0)    // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(buf[12:16], 700) // float4 OID
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(vals)))
	binary.LittleEndian.PutUint32(buf[20:24], 1) // lbound=1
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
		NewIntDatum(int64(et.OID)),                     // oid
		NewStringDatum(et.Name),                        // typname (name type)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),             // typowner
		NewIntDatum(4),                                 // typlen (enum = 4 bytes, like oid)
		NewBoolDatum(false),                            // typbyval
		NewStringDatum("e"),                            // typtype = 'e' (enum)
		NewStringDatum("E"),                            // typcategory = TYPCATEGORY_ENUM
		NewBoolDatum(false),                            // typispreferred
		NewBoolDatum(true),                             // typisdefined
		NewStringDatum(","),                            // typdelim
		NewIntDatum(0),                                 // typrelid
		NewIntDatum(0),                                 // typsubscript
		NewIntDatum(0),                                 // typelem
		NewIntDatum(int64(et.ArrayOID)),                // typarray (auto-generated `_name` array type; DU-002 slice 89)
		NewIntDatum(0),                                 // typinput
		NewIntDatum(0),                                 // typoutput
		NewIntDatum(0),                                 // typreceive
		NewIntDatum(0),                                 // typsend
		NewIntDatum(0),                                 // typmodin
		NewIntDatum(0),                                 // typmodout
		NewIntDatum(0),                                 // typanalyze
		NewStringDatum("i"),                            // typalign = 'i' (int-aligned, 4-byte)
		NewStringDatum("p"),                            // typstorage = 'p' (plain)
		NewBoolDatum(false),                            // typnotnull
		NewIntDatum(0),                                 // typbasetype
		NewIntDatum(-1),                                // typtypmod
		NewIntDatum(0),                                 // typndims
		NewIntDatum(0),                                 // typcollation
		NullDatum,                                      // typdefaultbin (NULL)
		NullDatum,                                      // typdefault (NULL)
		NullDatum,                                      // typacl (NULL)
	}
}

// buildUserPGTypeRowForEnumArray builds the pg_type row for an enum's
// auto-generated array type (`_name`, OID et.ArrayOID). PostgreSQL creates this
// alongside every enum; pg_dump's getTableAttrs LEFT JOINs pg_attribute to
// pg_type on atttypid = t.oid and passes the joined t.oid to format_type, so a
// `mood[]` column whose array type has no pg_type row joins to NULL and renders
// as a blank type. The row's typarray=0 / typelem=enumOID, combined with the
// base enum row's typarray=ArrayOID, makes pg_dump's getTypes isarray
// subquery (`(SELECT typarray FROM pg_type WHERE oid = typelem) = oid`)
// evaluate true so the array type is recognized as auto-generated and NOT
// emitted as a separate CREATE TYPE. DU-002 slice 89.
func buildUserPGTypeRowForEnumArray(et *catalog.EnumType) Row {
	return Row{
		NewIntDatum(int64(et.ArrayOID)),                // oid
		NewStringDatum("_" + et.Name),                  // typname (array type name)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),             // typowner
		NewIntDatum(-1),                                // typlen (varlena array)
		NewBoolDatum(false),                            // typbyval
		NewStringDatum("b"),                            // typtype = 'b' (base)
		NewStringDatum("A"),                            // typcategory = TYPCATEGORY_ARRAY
		NewBoolDatum(false),                            // typispreferred
		NewBoolDatum(true),                             // typisdefined
		NewStringDatum(","),                            // typdelim
		NewIntDatum(0),                                 // typrelid
		NewIntDatum(0),                                 // typsubscript
		NewIntDatum(int64(et.OID)),                     // typelem = the enum element type
		NewIntDatum(0),                                 // typarray
		NewIntDatum(0),                                 // typinput
		NewIntDatum(0),                                 // typoutput
		NewIntDatum(0),                                 // typreceive
		NewIntDatum(0),                                 // typsend
		NewIntDatum(0),                                 // typmodin
		NewIntDatum(0),                                 // typmodout
		NewIntDatum(0),                                 // typanalyze
		NewStringDatum("i"),                            // typalign = 'i' (matches 4-byte enum element)
		NewStringDatum("x"),                            // typstorage = 'x' (extended)
		NewBoolDatum(false),                            // typnotnull
		NewIntDatum(0),                                 // typbasetype
		NewIntDatum(-1),                                // typtypmod
		NewIntDatum(0),                                 // typndims
		NewIntDatum(0),                                 // typcollation
		NullDatum,                                      // typdefaultbin (NULL)
		NullDatum,                                      // typdefault (NULL)
		NullDatum,                                      // typacl (NULL)
	}
}

// buildUserPGTypeRowForComposite builds the pg_type row for a user-defined
// composite type (`CREATE TYPE x AS (...)`), typtype='c'/typcategory='C'.
// typrelid points at the implicit pg_class relation in PostgreSQL; goopg does
// not synthesize that relation yet, so it is left 0 (a follow-up slice fills it
// in along with the pg_attribute field rows). Composite types are pass-by-ref
// varlena (typlen=-1, typbyval=false, typalign='d', typstorage='x'), mirroring
// PG's record/composite layout. DU-002 slice 242.
func buildUserPGTypeRowForComposite(ct *catalog.CompositeType) Row {
	return Row{
		NewIntDatum(int64(ct.OID)),                     // oid
		NewStringDatum(ct.Name),                        // typname (name type)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),             // typowner
		NewIntDatum(-1),                                // typlen (varlena composite)
		NewBoolDatum(false),                            // typbyval
		NewStringDatum("c"),                            // typtype = 'c' (composite)
		NewStringDatum("C"),                            // typcategory = TYPCATEGORY_COMPOSITE
		NewBoolDatum(false),                            // typispreferred
		NewBoolDatum(true),                             // typisdefined
		NewStringDatum(","),                            // typdelim
		NewIntDatum(int64(ct.RelOID)),                  // typrelid (implicit pg_class relation, relkind='c')
		NewIntDatum(0),                                 // typsubscript
		NewIntDatum(0),                                 // typelem
		NewIntDatum(int64(ct.ArrayOID)),                // typarray (auto-generated `_name` array type)
		NewIntDatum(0),                                 // typinput
		NewIntDatum(0),                                 // typoutput
		NewIntDatum(0),                                 // typreceive
		NewIntDatum(0),                                 // typsend
		NewIntDatum(0),                                 // typmodin
		NewIntDatum(0),                                 // typmodout
		NewIntDatum(0),                                 // typanalyze
		NewStringDatum("d"),                            // typalign = 'd' (double-aligned, like RECORD)
		NewStringDatum("x"),                            // typstorage = 'x' (extended)
		NewBoolDatum(false),                            // typnotnull
		NewIntDatum(0),                                 // typbasetype
		NewIntDatum(-1),                                // typtypmod
		NewIntDatum(0),                                 // typndims
		NewIntDatum(0),                                 // typcollation
		NullDatum,                                      // typdefaultbin (NULL)
		NullDatum,                                      // typdefault (NULL)
		NullDatum,                                      // typacl (NULL)
	}
}

// buildUserPGTypeRowForCompositeArray builds the pg_type row for the
// auto-generated `_name` array type of a composite type (typtype='b',
// typcategory='A'), mirroring buildUserPGTypeRowForEnumArray. DU-002 slice 242.
func buildUserPGTypeRowForCompositeArray(ct *catalog.CompositeType) Row {
	return Row{
		NewIntDatum(int64(ct.ArrayOID)),                // oid
		NewStringDatum("_" + ct.Name),                  // typname (array type name)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),             // typowner
		NewIntDatum(-1),                                // typlen (varlena array)
		NewBoolDatum(false),                            // typbyval
		NewStringDatum("b"),                            // typtype = 'b' (base)
		NewStringDatum("A"),                            // typcategory = TYPCATEGORY_ARRAY
		NewBoolDatum(false),                            // typispreferred
		NewBoolDatum(true),                             // typisdefined
		NewStringDatum(","),                            // typdelim
		NewIntDatum(0),                                 // typrelid
		NewIntDatum(0),                                 // typsubscript
		NewIntDatum(int64(ct.OID)),                     // typelem = the composite element type
		NewIntDatum(0),                                 // typarray
		NewIntDatum(0),                                 // typinput
		NewIntDatum(0),                                 // typoutput
		NewIntDatum(0),                                 // typreceive
		NewIntDatum(0),                                 // typsend
		NewIntDatum(0),                                 // typmodin
		NewIntDatum(0),                                 // typmodout
		NewIntDatum(0),                                 // typanalyze
		NewStringDatum("d"),                            // typalign = 'd' (matches composite element)
		NewStringDatum("x"),                            // typstorage = 'x' (extended)
		NewBoolDatum(false),                            // typnotnull
		NewIntDatum(0),                                 // typbasetype
		NewIntDatum(-1),                                // typtypmod
		NewIntDatum(0),                                 // typndims
		NewIntDatum(0),                                 // typcollation
		NullDatum,                                      // typdefaultbin (NULL)
		NullDatum,                                      // typdefault (NULL)
		NullDatum,                                      // typacl (NULL)
	}
}

// parseCompositeFieldType splits a composite type field's column-type string (as
// recorded by the parser in catalog.CompositeField.ColType — tokens joined by a
// space, e.g. "int", "text", "numeric ( 10 , 2 )") into its base type OID and
// the PG-canonical pg_attribute.atttypmod. pg_dump's dumpCompositeType renders
// each field via format_type(atttypid, atttypmod), so a numeric(10,2) field must
// carry the encoded typmod to round-trip as `numeric(10,2)` rather than bare
// `numeric`. Unknown / arg-less names yield typmod -1, matching pgAttTypmod.
// DU-002 slice 243.
// The collapsed base type name is returned as the third value so callers can
// re-resolve a user-defined field type (enum/domain) that TypeNameToOID folded
// to the text fallback. DU-002 slice 245.
// An array suffix (`text[]`, tokenized by the parser as the three tokens
// `text [ ]`) is detected and stripped; the fourth return value reports whether
// the field is an array, and the returned OID/typmod/base describe the ELEMENT
// type so the caller can remap to the array OID (built-in via ArrayOIDForBase,
// enum via et.ArrayOID) and stamp attndims=1. DU-002 slice 246.
func parseCompositeFieldType(colType string) (uint32, int64, string, bool) {
	s := strings.TrimSpace(colType)
	// Strip any array suffix before parsing the element type. The suffix appears
	// after the optional type modifier, e.g. "numeric ( 10 , 2 ) [ ]", so the
	// first '[' marks the start of the array brackets regardless of typmod.
	isArray := false
	if i := strings.IndexByte(s, '['); i >= 0 {
		isArray = true
		s = strings.TrimSpace(s[:i])
	}
	base := s
	var args []int64
	if i := strings.IndexByte(base, '('); i >= 0 {
		inner := base[i+1:]
		base = strings.TrimSpace(base[:i])
		if j := strings.IndexByte(inner, ')'); j >= 0 {
			inner = inner[:j]
		}
		for _, part := range strings.Split(inner, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if n, err := strconv.ParseInt(part, 10, 64); err == nil {
				args = append(args, n)
			}
		}
	}
	// Collapse the parser's space-joined multi-word names ("double precision",
	// "character varying", "timestamp with time zone") to a single-spaced form
	// TypeNameToOID recognises.
	base = strings.Join(strings.Fields(base), " ")
	typOID := catalog.TypeNameToOID(base)
	return typOID, pgAttTypmod(typOID, args), base, isArray
}

// buildUserPGClassRowForComposite builds the 34-column PG18-canonical pg_class
// row for the implicit relation (relkind='c') backing a composite type. PG
// creates this relation so the field columns live in pg_attribute keyed by
// attrelid; pg_dump's getTypes subquery reads its relkind (must be 'c') to route
// the type to dumpCompositeType, and getTables includes relkind='c' rows but
// marks them DUMP_COMPONENT_NONE (never emitted as a table). A composite-type
// relation has no physical storage and no access method, so relam/relfilenode are
// 0 and relfrozenxid/relminmxid are 0 (InvalidTransactionId — PG only freezes
// relkinds with storage). DU-002 slice 243.
func buildUserPGClassRowForComposite(cat catalog.Catalog, ct *catalog.CompositeType) Row {
	return Row{
		NewIntDatum(int64(ct.RelOID)),                  // oid
		NewStringDatum(ct.Name),                        // relname (name)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // relnamespace = public
		NewIntDatum(int64(ct.OID)),                     // reltype = the composite pg_type OID
		NewIntDatum(0),                                 // reloftype
		NewIntDatum(bootstrapSuperuserOID),             // relowner
		NewIntDatum(0),                                 // relam (composite types have no access method)
		NewIntDatum(0),                                 // relfilenode (no physical storage)
		NewIntDatum(0),                                 // reltablespace
		NewIntDatum(0),                                 // relpages
		NewIntDatum(0),                                 // reltuples
		NewIntDatum(0),                                 // relallvisible
		NewIntDatum(0),                                 // relallfrozen
		NewIntDatum(0),                                 // reltoastrelid
		NewBoolDatum(false),                            // relhasindex
		NewBoolDatum(false),                            // relisshared
		NewStringDatum("p"),                            // relpersistence
		NewStringDatum("c"),                            // relkind = 'c' (composite type)
		NewIntDatum(int64(len(ct.Fields))),             // relnatts
		NewIntDatum(0),                                 // relchecks
		NewBoolDatum(false),                            // relhasrules
		NewBoolDatum(false),                            // relhastriggers
		NewBoolDatum(false),                            // relhassubclass
		NewBoolDatum(false),                            // relrowsecurity
		NewBoolDatum(false),                            // relforcerowsecurity
		NewBoolDatum(true),                             // relispopulated
		NewStringDatum("n"),                            // relreplident (no storage → REPLICA_IDENTITY_NOTHING)
		NewBoolDatum(false),                            // relispartition
		NewIntDatum(0),                                 // relrewrite
		NewIntDatum(0),                                 // relfrozenxid (InvalidTransactionId — no storage)
		NewIntDatum(0),                                 // relminmxid
		NewStringDatum("{}"),                           // relacl
		NewStringDatum("{}"),                           // reloptions
		NewStringDatum(""),                             // relpartbound
	}
}

// buildUserPGAttributeRowForCompositeField builds a 25-column PG18 pg_attribute
// row for one field of a composite type's implicit relation (attrelid =
// ct.RelOID). atttypid/atttypmod come from the field's declared type so pg_dump's
// dumpCompositeType renders `<field> <format_type(atttypid, atttypmod)>`; the
// physical attrs (attlen/attbyval/attalign/attstorage/attcollation) follow the
// resolved type. Composite fields carry no per-column overrides, so the
// override-bearing columns are their defaults. attnum is 1-based. DU-002 slice 243.
//
// A field whose declared type is a user-defined enum resolves to the text
// fallback inside parseCompositeFieldType (TypeNameToOID knows only built-ins).
// Re-resolve it to the enum's dynamically-allocated pg_type OID — mirroring the
// table-column path in buildUserPGAttributeRow — so pg_dump's
// dumpCompositeType renders the field as the enum type rather than `text`. The
// physical attrs match buildUserPGTypeRowForEnum's shape (4-byte, int-aligned,
// plain-storage, non-collatable, like oid). DU-002 slice 245.
//
// An ARRAY field (`tags text[]`, `feelings mood[]`) remaps the element OID to
// the array type's OID and stamps attndims=1, mirroring the table-column array
// path: a built-in element folds through catalog.ArrayOIDForBase (e.g. text →
// _text 1009), an enum element folds to its auto-generated array OID
// (et.ArrayOID). atttypmod carries the ELEMENT typmod (computed from the base
// OID before the remap), so `amount numeric(10,2)[]` round-trips its precision.
// The array's physical layout comes from userTypeAttrsForOID(arrayOID) for
// built-ins; an enum array gets the standard varlena-array shape (-1 length,
// int-aligned, extended storage). DU-002 slice 246.
//
// A field whose declared type is a user-defined DOMAIN re-resolves to the
// domain's pg_type OID (cat.LookupDomain on the raw field-type name) with
// physical attrs from the domain's base, so pg_dump renders the field as the
// domain name rather than `text`. Scalar only. DU-002 slice 248.
//
// A field whose declared type is itself another user-defined COMPOSITE type (a
// nested composite) re-resolves to the inner composite's pg_type OID
// (cat.LookupCompositeType) with the pass-by-ref varlena layout (-1 length,
// double-aligned, extended storage), so pg_dump renders the field as
// `public.<inner>`. Scalar only. DU-002 slice 249.
func buildUserPGAttributeRowForCompositeField(cat catalog.Catalog, ct *catalog.CompositeType, field catalog.CompositeField, attnum int) Row {
	typOID, typmod, base, isArray := parseCompositeFieldType(field.ColType)
	enumOID := uint32(0)
	enumArrayOID := uint32(0)
	if cat != nil && typOID == catalog.OIDText {
		if et, ok := cat.LookupEnum(base); ok {
			if isArray {
				enumArrayOID = et.ArrayOID
			} else {
				typOID = et.OID
				enumOID = et.OID
			}
		}
	}
	// A field whose declared type is a user-defined DOMAIN also folds to the text
	// fallback (TypeNameToOID knows only built-ins, and the parser records the raw
	// domain name in ColType — unlike table columns, composite fields are NOT
	// resolved to the base type at CREATE TYPE). Re-resolve to the domain's
	// pg_type OID so pg_dump's format_type(atttypid, atttypmod) renders the field
	// as the domain name rather than `text`; the physical attrs follow the domain's
	// BASE type (resolved the same way as buildUserPGTypeRowForDomain), since a
	// domain stores values exactly as its base. Scalar only, mirroring the
	// table-column path. DU-002 slice 248 (cf. slice 90).
	var domain *catalog.Domain
	if cat != nil && typOID == catalog.OIDText && !isArray {
		if d, ok := cat.LookupDomain(base); ok {
			domain = d
			typOID = d.OID
		}
	}
	// A field whose declared type is itself another user-defined COMPOSITE type
	// (a nested composite) also folds to the text fallback. Re-resolve to the
	// inner composite's pg_type OID so pg_dump's format_type renders the field as
	// the composite name (`public.<inner>`) rather than `text`. A composite type
	// is a pass-by-ref varlena (typlen=-1, byval=false, double-aligned, extended
	// storage), matching buildUserPGTypeRowForComposite. Scalar only, mirroring
	// the enum/domain paths. DU-002 slice 249.
	// An array of a user-defined composite type (`addr[]`) also folds to the
	// text fallback. Resolve to the inner composite's auto-generated array
	// pg_type OID (ArrayOID, synced at CREATE TYPE) so format_type renders the
	// field as `public.<inner>[]`; the physical attrs are a standard varlena
	// array over the double-aligned composite element. DU-002 slice 250.
	nestedComposite := false
	compositeArrayOID := uint32(0)
	if cat != nil && typOID == catalog.OIDText {
		if ict := cat.LookupCompositeType(base); ict != nil {
			if isArray {
				compositeArrayOID = ict.ArrayOID
			} else {
				nestedComposite = true
				typOID = ict.OID
			}
		}
	}
	attndims := int64(0)
	if isArray {
		switch {
		case enumArrayOID != 0:
			// Enum array OIDs are dynamic, so ArrayOIDForBase can't case on them.
			typOID = enumArrayOID
			attndims = 1
		case compositeArrayOID != 0:
			// Composite array OIDs are dynamic too. DU-002 slice 250.
			typOID = compositeArrayOID
			attndims = 1
		default:
			if aoid := catalog.ArrayOIDForBase(typOID); aoid != 0 {
				typOID = aoid
				attndims = 1
			}
		}
	}
	attrs := userTypeAttrsForOID(typOID)
	switch {
	case enumOID != 0:
		attrs = userTypeAttrs{TypLen: 4, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
	case enumArrayOID != 0:
		// An enum's array type is a standard varlena array: -1 length,
		// int-aligned (matching the 4-byte enum element), extended storage.
		attrs = userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	case compositeArrayOID != 0:
		// An array of a composite type is a standard varlena array: -1 length,
		// double-aligned (matching the double-aligned composite element),
		// extended storage. DU-002 slice 250.
		attrs = userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	case domain != nil:
		// A domain inherits its base type's physical layout, resolved exactly as
		// buildUserPGTypeRowForDomain does (BaseOID, else TypeNameToOID(Base.Name);
		// an enum base uses the enum's fixed 4-byte/int-aligned/plain shape).
		// DU-002 slice 248.
		if domain.BaseIsEnum {
			attrs = userTypeAttrs{TypLen: 4, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
		} else {
			baseOID := domain.BaseOID
			if baseOID == 0 {
				baseOID = catalog.TypeNameToOID(domain.Base.Name)
			}
			attrs = userTypeAttrsForOID(baseOID)
		}
	case nestedComposite:
		// A composite type is a pass-by-ref varlena: typlen=-1, not by-value,
		// double-aligned (like RECORD), extended storage. Mirrors
		// buildUserPGTypeRowForComposite. DU-002 slice 249.
		attrs = userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'd', TypStorage: 'x'}
	}
	return Row{
		NewIntDatum(int64(ct.RelOID)),            // attrelid
		NewStringDatum(field.Name),               // attname (name)
		NewIntDatum(int64(typOID)),               // atttypid
		NewIntDatum(int64(attrs.TypLen)),         // attlen
		NewIntDatum(int64(attnum)),               // attnum (1-based)
		NewIntDatum(typmod),                      // atttypmod
		NewIntDatum(attndims),                    // attndims
		NewBoolDatum(attrs.TypByVal),             // attbyval
		NewStringDatum(string(attrs.TypAlign)),   // attalign
		NewStringDatum(string(attrs.TypStorage)), // attstorage
		NewStringDatum(""),                       // attcompression (default)
		NewBoolDatum(false),                      // attnotnull
		NewBoolDatum(false),                      // atthasdef
		NewBoolDatum(false),                      // atthasmissing
		NewStringDatum(""),                       // attidentity
		NewStringDatum(""),                       // attgenerated
		NewBoolDatum(false),                      // attisdropped
		NewBoolDatum(true),                       // attislocal
		NewIntDatum(0),                           // attinhcount
		NewIntDatum(int64(attrs.TypCollation)),   // attcollation (type default)
		NullDatum,                                // attacl
		NullDatum,                                // attoptions
		NullDatum,                                // attfdwoptions
		NullDatum,                                // attmissingval
		NullDatum,                                // attstattarget
	}
}

// pgTypeCategoryForOID returns the PG18 pg_type.typcategory single-byte code for
// a built-in base type OID, used when a domain inherits its base type's category.
// It mirrors the typcategory values in postgres/src/include/catalog/pg_type.dat.
// Unknown OIDs fall back to 'U' (TYPCATEGORY_USER). DU-002 slice 90.
func pgTypeCategoryForOID(oid uint32) byte {
	switch oid {
	case catalog.OIDBool:
		return 'B'
	case catalog.OIDInt2, catalog.OIDInt4, catalog.OIDInt8, catalog.OIDFloat4, catalog.OIDFloat8, catalog.OIDNumeric, catalog.OIDOID:
		return 'N'
	case catalog.OIDChar, catalog.OIDText, catalog.OIDBpChar, catalog.OIDVarChar, catalog.OIDName:
		return 'S'
	default:
		return 'U'
	}
}

// buildUserPGTypeRowForDomain builds a 32-column pg_type Row for a user-defined
// DOMAIN type (typtype='d'). A domain stores values physically as its base type,
// so typlen/typbyval/typalign/typstorage/typcollation are inherited from the base
// (resolved via userTypeAttrsForOID); typbasetype/typtypmod record the base so
// pg_dump's dumpDomain can render `CREATE DOMAIN <name> AS format_type(typbasetype,
// typtypmod)`. typcollation matches the base's so dumpDomain's collation CASE
// (t.typcollation <> u.typcollation) yields 0 → no spurious COLLATE clause.
// typnotnull reflects the declared NOT NULL; typdefaultbin carries the rendered
// DEFAULT expression (pg_get_expr is a pass-through in goopg) so dumpDomain
// re-emits `DEFAULT <expr>` — NULL when the domain has no default. DU-002 slice 92.
func buildUserPGTypeRowForDomain(d *catalog.Domain) Row {
	baseOID := d.BaseOID
	if baseOID == 0 {
		baseOID = catalog.TypeNameToOID(d.Base.Name)
	}
	attrs := userTypeAttrsForOID(baseOID)
	typcategory := pgTypeCategoryForOID(baseOID)
	if d.BaseIsEnum {
		// Enum OIDs are dynamically allocated, so userTypeAttrsForOID /
		// pgTypeCategoryForOID can't case on them. A domain over an enum inherits
		// the enum's physical layout: 4-byte, int-aligned, plain storage, 'E'
		// category (mirrors the enum-column path in buildUserPGAttributeRow).
		// DU-002 slice 109.
		attrs = userTypeAttrs{TypLen: 4, TypByVal: false, TypAlign: 'i', TypStorage: 'p'}
		typcategory = 'E'
	}
	typmod := pgAttTypmod(baseOID, d.Base.Args)
	typdefaultbin := NullDatum
	if bin := d.DefaultBin(); bin != "" {
		typdefaultbin = NewStringDatum(bin)
	}
	return Row{
		NewIntDatum(int64(d.OID)),                      // oid
		NewStringDatum(d.Name),                         // typname (name type)
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // typnamespace = public
		NewIntDatum(bootstrapSuperuserOID),             // typowner
		NewIntDatum(int64(attrs.TypLen)),               // typlen (inherit base)
		NewBoolDatum(attrs.TypByVal),                   // typbyval (inherit base)
		NewStringDatum("d"),                            // typtype = 'd' (domain)
		NewStringDatum(string(typcategory)),            // typcategory (inherit base)
		NewBoolDatum(false),                            // typispreferred
		NewBoolDatum(true),                             // typisdefined
		NewStringDatum(","),                            // typdelim
		NewIntDatum(0),                                 // typrelid
		NewIntDatum(0),                                 // typsubscript
		NewIntDatum(0),                                 // typelem
		NewIntDatum(0),                                 // typarray (no domain array type in this slice)
		NewIntDatum(0),                                 // typinput
		NewIntDatum(0),                                 // typoutput
		NewIntDatum(0),                                 // typreceive
		NewIntDatum(0),                                 // typsend
		NewIntDatum(0),                                 // typmodin
		NewIntDatum(0),                                 // typmodout
		NewIntDatum(0),                                 // typanalyze
		NewStringDatum(string(attrs.TypAlign)),         // typalign (inherit base)
		NewStringDatum(string(attrs.TypStorage)),       // typstorage (inherit base)
		NewBoolDatum(d.NotNull),                        // typnotnull (declared NOT NULL)
		NewIntDatum(int64(baseOID)),                    // typbasetype
		NewIntDatum(typmod),                            // typtypmod (base typmod)
		NewIntDatum(0),                                 // typndims
		NewIntDatum(int64(attrs.TypCollation)),         // typcollation (inherit base)
		typdefaultbin,                                  // typdefaultbin (rendered DEFAULT expr, NULL if none)
		NullDatum,                                      // typdefault (NULL; pg_dump prefers typdefaultbin)
		NullDatum,                                      // typacl (NULL)
	}
}
