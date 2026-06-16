// Package catalog — on-disk codec for system catalog heap rows.
//
// This file provides three thin row types (PGClassRow, PGAttributeRow,
// PGTypeRow) and their binary encode/decode helpers.  The on-disk format
// matches executor.EncodeRow / executor.DecodeRowInto so a standard
// SeqScan on pg_class / pg_attribute / pg_type sees the correct values
// without special-casing the system catalogs.
//
// Format per column (identical to executor/codec.go):
//
//	1 null-flag byte (0 = present, 1 = NULL)
//	If not null:
//	  int4  → 4 bytes, big-endian uint32
//	  bool  → 1 byte  (1 = true, 0 = false)
//	  text  → 4-byte big-endian uint32 length + raw UTF-8 bytes
//
// See docs/design/0030-0001-system-catalog-heap-substrate.md (Phase 2).
package catalog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

const (
	pgNameDataLen            = 64
	pgClassFixedPartSize     = 144
	pgAttributeFixedPartSize = 100
	pgClassOffOID            = 0
	pgClassOffRelName        = 4
	pgClassOffRelNamespace   = 68
	pgClassOffRelFileNode    = 88
	pgClassOffRelIsShared    = 117
	pgClassOffRelPersistence = 118
	pgClassOffRelKind        = 119
	pgClassOffRelNAtts       = 120
	pgAttributeOffRelID      = 0
	pgAttributeOffName       = 4
	pgAttributeOffTypID      = 68
	pgAttributeOffNum        = 74
	pgAttributeOffNotNull    = 86
	pgAttributeOffIsDropped  = 91
)

// Namespace OIDs — matching upstream's bootstrap constants.
const (
	PGCatalogNamespaceOID uint32 = 11   // pg_catalog schema
	PublicNamespaceOID    uint32 = 2200 // public schema
)

// Built-in type OIDs from postgres/src/include/catalog/pg_type.h.
// Extended in M0030-0005 to cover all types that pgoTypeOIDFor handled.
const (
	OIDBool        uint32 = 16
	OIDBytea       uint32 = 17
	OIDInt8        uint32 = 20
	OIDInt2        uint32 = 21
	OIDInt4        uint32 = 23
	OIDText        uint32 = 25
	OIDOID         uint32 = 26
	OIDFloat4      uint32 = 700
	OIDFloat8      uint32 = 701
	OIDDate        uint32 = 1082
	OIDTime        uint32 = 1083
	OIDTimeTZ      uint32 = 1266
	OIDTimestamp   uint32 = 1114
	OIDTimestampTZ uint32 = 1184
	OIDBpChar      uint32 = 1042
	OIDVarChar     uint32 = 1043
	OIDNumeric     uint32 = 1700
	OIDUUID        uint32 = 2950
	OIDJSON        uint32 = 114
	OIDJsonb       uint32 = 3802
	OIDJsonpath    uint32 = 4072
	// DU-002 slice 85: refcursor is a varlena cursor-name reference (typstorage
	// 'x', no typmod), so format_type renders the bare `refcursor`.
	OIDRefcursor uint32 = 1790
	OIDInterval  uint32 = 1186
	// DU-002 slice 71: the network-address family. All are seeded in pg_type
	// (see initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs.
	// None carry a typmod, so format_type renders the bare type name.
	OIDInet     uint32 = 869
	OIDCidr     uint32 = 650
	OIDMacaddr  uint32 = 829
	OIDMacaddr8 uint32 = 774
	// DU-002 slice 72: the geometric family. All are seeded in pg_type (see
	// initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs. None
	// carry a typmod, so format_type renders the bare type name.
	OIDPoint   uint32 = 600
	OIDLseg    uint32 = 601
	OIDPath    uint32 = 602
	OIDBox     uint32 = 603
	OIDPolygon uint32 = 604
	OIDLine    uint32 = 628
	OIDCircle  uint32 = 718
	// DU-002 slice 73: the full-text-search family. Both are seeded in pg_type
	// (see initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs.
	// Neither carries a typmod, so format_type renders the bare type name.
	OIDTsvector uint32 = 3614
	OIDTsquery  uint32 = 3615
	// DU-002 slice 74: xml and money. Both are seeded in pg_type (see
	// initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs. Neither
	// carries a typmod, so format_type renders the bare type name.
	OIDXML   uint32 = 142
	OIDMoney uint32 = 790
	// DU-002 slice 75: bit and varbit (bit varying). Both are seeded in pg_type
	// (see initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs.
	// Both carry a typmod (the bit length, stored raw with no VARHDRSZ), so
	// format_type renders `bit(n)` / `bit varying(n)`.
	OIDBit    uint32 = 1560
	OIDVarbit uint32 = 1562
	// DU-002 slice 76: pg_lsn (the WAL log-sequence-number type). Seeded in
	// pg_type (see initdb/pg_type_seed_data.go) so a PG standby can resolve the
	// OID. It is an 8-byte by-value type with no typmod, so format_type renders
	// the bare type name.
	OIDPgLsn uint32 = 3220
	// DU-002 slice 77: the snapshot types. txid_snapshot is the legacy
	// (pre-PG13) txid-based snapshot type; pg_snapshot is its xid8-based
	// successor. Both are seeded in pg_type (see initdb/pg_type_seed_data.go)
	// so a PG standby can resolve the OIDs. Both are varlena with no typmod, so
	// format_type renders the bare type name.
	OIDTxidSnapshot uint32 = 2970
	OIDPgSnapshot   uint32 = 5038
	// DU-002 slice 78: xid8 (the 64-bit full transaction-id type, the element
	// of pg_snapshot). Seeded in pg_type (see initdb/pg_type_seed_data.go) so a
	// PG standby can resolve the OID. It is an 8-byte by-value type with no
	// typmod, so format_type renders the bare type name.
	OIDXid8 uint32 = 5069
	// DU-002 slice 79: the transaction/tuple identifier types. tid is the 6-byte
	// tuple identifier (block,offset); xid is the 32-bit transaction id; cid is
	// the 32-bit command id. All three are seeded in pg_type (see
	// initdb/pg_type_seed_data.go) so a PG standby can resolve the OIDs. None
	// carry a typmod, so format_type renders the bare type name.
	OIDTid uint32 = 27
	OIDXid uint32 = 28
	OIDCid uint32 = 29
	// DU-002 slice 80: the OID-reference ("reg*") family. Each is a 4-byte
	// by-value alias for oid whose I/O function resolves the referenced object's
	// name; declared as a column type they round-trip like oid. All are seeded in
	// pg_type (see initdb/pg_type_seed_data.go) so a PG standby can resolve the
	// OIDs. None carry a typmod, so format_type renders the bare type name.
	OIDRegproc       uint32 = 24
	OIDRegprocedure  uint32 = 2202
	OIDRegoper       uint32 = 2203
	OIDRegoperator   uint32 = 2204
	OIDRegclass      uint32 = 2205
	OIDRegtype       uint32 = 2206
	OIDRegconfig     uint32 = 3734
	OIDRegdictionary uint32 = 3769
	OIDRegnamespace  uint32 = 4089
	OIDRegrole       uint32 = 4096
	OIDRegcollation  uint32 = 4191
	// DU-002 slice 81: the legacy OID/int2 vector types. int2vector is a
	// space-separated list of int2 (used internally for pg_index.indkey);
	// oidvector a space-separated list of oid (pg_proc.proargtypes). Both are
	// declarable column types — format_type renders the bare names "int2vector"/
	// "oidvector" (NOT "smallint[]"/"oid[]"; those are the genuine _int2/_oid
	// array types 1005/1028). Seeded in pg_type (initdb/pg_type_seed_data.go) so a
	// PG standby can resolve the OIDs; neither carries a typmod.
	OIDInt2vector uint32 = 22
	OIDOidvector  uint32 = 30
	// DU-002 slice 82: name (the 64-byte fixed-length identifier type, used
	// internally for catalog name columns relname/attname/typname). Declarable
	// as a column type; format_type renders the bare name "name". Seeded in
	// pg_type (initdb/pg_type_seed_data.go) so a PG standby can resolve the OID.
	// It carries no typmod. ("char" — OID 18, the 1-byte internal char distinct
	// from char/bpchar — is deferred: the parser folds both quoted "char" and
	// bare char to ct.Name="char", so disambiguating the two needs a parser
	// change, not just a codec entry.)
	OIDName uint32 = 19

	// Array (_typename) OIDs for the element types goopg currently supports as
	// array columns. These mirror pg_type.typarray for int2/int4/int8/text/
	// bool/numeric/float8/date/timestamp and are the OIDs format_type renders as
	// `smallint[]`/`integer[]`/`bigint[]`/`text[]`/`boolean[]`/`numeric[]`/
	// `double precision[]`/`date[]`/`timestamp without time zone[]`. DU-002 slice
	// 62 (int2/int4/int8/text); slice 63 added bool/numeric; slice 64 added
	// float8/date/timestamp; slice 65 added float4/time/timestamptz.
	OIDArrayBool        uint32 = 1000
	OIDArrayBytea       uint32 = 1001
	OIDArrayInt2        uint32 = 1005
	OIDArrayInt4        uint32 = 1007
	OIDArrayText        uint32 = 1009
	OIDArrayInt8        uint32 = 1016
	OIDArrayNumeric     uint32 = 1231
	OIDArrayFloat8      uint32 = 1022
	OIDArrayDate        uint32 = 1182
	OIDArrayTimestamp   uint32 = 1115
	OIDArrayFloat4      uint32 = 1021
	OIDArrayTime        uint32 = 1183
	OIDArrayTimeTZ      uint32 = 1270
	OIDArrayTimestampTZ uint32 = 1185
	OIDArrayUUID        uint32 = 2951
	// DU-002 slice 68: the remaining simple scalar-backed array types.
	// _varchar/_bpchar carry the element typmod onto the array (like _numeric);
	// _oid has no typmod. format_type renders these as `character varying[]`,
	// `character[]`, and `oid[]`.
	OIDArrayVarChar uint32 = 1015
	OIDArrayBpChar  uint32 = 1014
	OIDArrayOID     uint32 = 1028
	// DU-002 slice 69: the JSON family. json/jsonb are varlena with no typmod,
	// so format_type renders these arrays as the bare `json[]` / `jsonb[]`.
	OIDArrayJSON  uint32 = 199
	OIDArrayJsonb uint32 = 3807
	// DU-002 slice 84: jsonpath. _jsonpath is varlena with no typmod, so
	// format_type renders the array as the bare `jsonpath[]`.
	OIDArrayJsonpath uint32 = 4073
	// DU-002 slice 85: refcursor. _refcursor is varlena with no typmod, so
	// format_type renders the array as the bare `refcursor[]`.
	OIDArrayRefcursor uint32 = 2201
	// DU-002 slice 70: interval. _interval carries the element typmod onto the
	// array (interval fields/precision), but a bare `interval[]` column has
	// typmod -1, so format_type renders it as the bare `interval[]`.
	OIDArrayInterval uint32 = 1187
	// DU-002 slice 71: the network-address array types. None carry a typmod, so
	// format_type renders the bare element name with a [] suffix.
	OIDArrayInet     uint32 = 1041
	OIDArrayCidr     uint32 = 651
	OIDArrayMacaddr  uint32 = 1040
	OIDArrayMacaddr8 uint32 = 775
	// DU-002 slice 72: the geometric array types. None carry a typmod, so
	// format_type renders the bare element name with a [] suffix.
	OIDArrayPoint   uint32 = 1017
	OIDArrayLseg    uint32 = 1018
	OIDArrayPath    uint32 = 1019
	OIDArrayBox     uint32 = 1020
	OIDArrayPolygon uint32 = 1027
	OIDArrayLine    uint32 = 629
	OIDArrayCircle  uint32 = 719
	// DU-002 slice 73: the full-text-search array types. Neither carries a
	// typmod, so format_type renders the bare element name with a [] suffix.
	OIDArrayTsvector uint32 = 3643
	OIDArrayTsquery  uint32 = 3645
	// DU-002 slice 74: the xml and money array types. Neither carries a typmod,
	// so format_type renders the bare element name with a [] suffix.
	OIDArrayXML   uint32 = 143
	OIDArrayMoney uint32 = 791
	// DU-002 slice 75: the bit and varbit array types. Both carry the element
	// typmod (bit length) onto the array, so format_type renders the element with
	// its typmod and re-appends [] (e.g. `bit(8)[]`, `bit varying(8)[]`).
	OIDArrayBit    uint32 = 1561
	OIDArrayVarbit uint32 = 1563
	// DU-002 slice 76: the pg_lsn array type. pg_lsn has no typmod, so
	// format_type renders the bare element name with a [] suffix.
	OIDArrayPgLsn uint32 = 3221
	// DU-002 slice 77: the snapshot array types. Neither carries a typmod, so
	// format_type renders the bare element name with a [] suffix.
	OIDArrayTxidSnapshot uint32 = 2949
	OIDArrayPgSnapshot   uint32 = 5039
	// DU-002 slice 78: the xid8 array type. xid8 has no typmod, so format_type
	// renders the bare element name with a [] suffix.
	OIDArrayXid8 uint32 = 271
	// DU-002 slice 79: the tid/xid/cid array types. None carry a typmod, so
	// format_type renders the bare element name with a [] suffix.
	OIDArrayTid uint32 = 1010
	OIDArrayXid uint32 = 1011
	OIDArrayCid uint32 = 1012
	// DU-002 slice 80: the OID-reference ("reg*") array types. None carry a
	// typmod, so format_type renders the bare element name with a [] suffix.
	OIDArrayRegproc       uint32 = 1008
	OIDArrayRegprocedure  uint32 = 2207
	OIDArrayRegoper       uint32 = 2208
	OIDArrayRegoperator   uint32 = 2209
	OIDArrayRegclass      uint32 = 2210
	OIDArrayRegtype       uint32 = 2211
	OIDArrayRegconfig     uint32 = 3735
	OIDArrayRegdictionary uint32 = 3770
	OIDArrayRegnamespace  uint32 = 4090
	OIDArrayRegrole       uint32 = 4097
	OIDArrayRegcollation  uint32 = 4192
	// DU-002 slice 81: the int2vector/oidvector array types (_int2vector 1006 /
	// _oidvector 1013). Neither carries a typmod, so format_type renders the bare
	// element name with a [] suffix ("int2vector[]"/"oidvector[]").
	OIDArrayInt2vector uint32 = 1006
	OIDArrayOidvector  uint32 = 1013
	// DU-002 slice 82: the name array type (_name 1003). No typmod, so
	// format_type renders the bare element name with a [] suffix ("name[]").
	OIDArrayName uint32 = 1003
)

// ArrayOIDForBase returns the canonical array (_typename) OID for a base scalar
// OID, or 0 when goopg has no array OID mapped for that base type. Mirrors the
// pg_type.typarray column for the four element types goopg currently supports as
// array columns (int2/int4/int8/text). DU-002 slice 62.
func ArrayOIDForBase(baseOID uint32) uint32 {
	switch baseOID {
	case OIDBool:
		return OIDArrayBool
	case OIDBytea:
		return OIDArrayBytea
	case OIDInt2:
		return OIDArrayInt2
	case OIDInt4:
		return OIDArrayInt4
	case OIDInt8:
		return OIDArrayInt8
	case OIDText:
		return OIDArrayText
	case OIDNumeric:
		return OIDArrayNumeric
	case OIDFloat8:
		return OIDArrayFloat8
	case OIDDate:
		return OIDArrayDate
	case OIDTimestamp:
		return OIDArrayTimestamp
	case OIDFloat4:
		return OIDArrayFloat4
	case OIDTime:
		return OIDArrayTime
	case OIDTimeTZ:
		return OIDArrayTimeTZ
	case OIDTimestampTZ:
		return OIDArrayTimestampTZ
	case OIDUUID:
		return OIDArrayUUID
	case OIDVarChar:
		return OIDArrayVarChar
	case OIDBpChar:
		return OIDArrayBpChar
	case OIDOID:
		return OIDArrayOID
	case OIDName:
		return OIDArrayName
	case OIDJSON:
		return OIDArrayJSON
	case OIDJsonb:
		return OIDArrayJsonb
	case OIDJsonpath:
		return OIDArrayJsonpath
	case OIDRefcursor:
		return OIDArrayRefcursor
	case OIDInterval:
		return OIDArrayInterval
	case OIDInet:
		return OIDArrayInet
	case OIDCidr:
		return OIDArrayCidr
	case OIDMacaddr:
		return OIDArrayMacaddr
	case OIDMacaddr8:
		return OIDArrayMacaddr8
	case OIDPoint:
		return OIDArrayPoint
	case OIDLseg:
		return OIDArrayLseg
	case OIDPath:
		return OIDArrayPath
	case OIDBox:
		return OIDArrayBox
	case OIDPolygon:
		return OIDArrayPolygon
	case OIDLine:
		return OIDArrayLine
	case OIDCircle:
		return OIDArrayCircle
	case OIDTsvector:
		return OIDArrayTsvector
	case OIDTsquery:
		return OIDArrayTsquery
	case OIDXML:
		return OIDArrayXML
	case OIDMoney:
		return OIDArrayMoney
	case OIDBit:
		return OIDArrayBit
	case OIDVarbit:
		return OIDArrayVarbit
	case OIDPgLsn:
		return OIDArrayPgLsn
	case OIDTxidSnapshot:
		return OIDArrayTxidSnapshot
	case OIDPgSnapshot:
		return OIDArrayPgSnapshot
	case OIDXid8:
		return OIDArrayXid8
	case OIDTid:
		return OIDArrayTid
	case OIDXid:
		return OIDArrayXid
	case OIDCid:
		return OIDArrayCid
	case OIDRegproc:
		return OIDArrayRegproc
	case OIDRegprocedure:
		return OIDArrayRegprocedure
	case OIDRegoper:
		return OIDArrayRegoper
	case OIDRegoperator:
		return OIDArrayRegoperator
	case OIDRegclass:
		return OIDArrayRegclass
	case OIDRegtype:
		return OIDArrayRegtype
	case OIDRegconfig:
		return OIDArrayRegconfig
	case OIDRegdictionary:
		return OIDArrayRegdictionary
	case OIDRegnamespace:
		return OIDArrayRegnamespace
	case OIDRegrole:
		return OIDArrayRegrole
	case OIDRegcollation:
		return OIDArrayRegcollation
	case OIDInt2vector:
		return OIDArrayInt2vector
	case OIDOidvector:
		return OIDArrayOidvector
	}
	return 0
}

// BaseOIDForArray is the inverse of ArrayOIDForBase: it maps a known array OID
// back to its element scalar OID, returning ok=false for non-array OIDs. Used by
// the heap loader to reconstruct an array column's catalog.Type on restart.
// DU-002 slice 62.
func BaseOIDForArray(oid uint32) (uint32, bool) {
	switch oid {
	case OIDArrayBool:
		return OIDBool, true
	case OIDArrayBytea:
		return OIDBytea, true
	case OIDArrayInt2:
		return OIDInt2, true
	case OIDArrayInt4:
		return OIDInt4, true
	case OIDArrayInt8:
		return OIDInt8, true
	case OIDArrayText:
		return OIDText, true
	case OIDArrayNumeric:
		return OIDNumeric, true
	case OIDArrayFloat8:
		return OIDFloat8, true
	case OIDArrayDate:
		return OIDDate, true
	case OIDArrayTimestamp:
		return OIDTimestamp, true
	case OIDArrayFloat4:
		return OIDFloat4, true
	case OIDArrayTime:
		return OIDTime, true
	case OIDArrayTimeTZ:
		return OIDTimeTZ, true
	case OIDArrayTimestampTZ:
		return OIDTimestampTZ, true
	case OIDArrayUUID:
		return OIDUUID, true
	case OIDArrayVarChar:
		return OIDVarChar, true
	case OIDArrayBpChar:
		return OIDBpChar, true
	case OIDArrayOID:
		return OIDOID, true
	case OIDArrayName:
		return OIDName, true
	case OIDArrayJSON:
		return OIDJSON, true
	case OIDArrayJsonb:
		return OIDJsonb, true
	case OIDArrayJsonpath:
		return OIDJsonpath, true
	case OIDArrayRefcursor:
		return OIDRefcursor, true
	case OIDArrayInterval:
		return OIDInterval, true
	case OIDArrayInet:
		return OIDInet, true
	case OIDArrayCidr:
		return OIDCidr, true
	case OIDArrayMacaddr:
		return OIDMacaddr, true
	case OIDArrayMacaddr8:
		return OIDMacaddr8, true
	case OIDArrayPoint:
		return OIDPoint, true
	case OIDArrayLseg:
		return OIDLseg, true
	case OIDArrayPath:
		return OIDPath, true
	case OIDArrayBox:
		return OIDBox, true
	case OIDArrayPolygon:
		return OIDPolygon, true
	case OIDArrayLine:
		return OIDLine, true
	case OIDArrayCircle:
		return OIDCircle, true
	case OIDArrayTsvector:
		return OIDTsvector, true
	case OIDArrayTsquery:
		return OIDTsquery, true
	case OIDArrayXML:
		return OIDXML, true
	case OIDArrayMoney:
		return OIDMoney, true
	case OIDArrayBit:
		return OIDBit, true
	case OIDArrayVarbit:
		return OIDVarbit, true
	case OIDArrayPgLsn:
		return OIDPgLsn, true
	case OIDArrayTxidSnapshot:
		return OIDTxidSnapshot, true
	case OIDArrayPgSnapshot:
		return OIDPgSnapshot, true
	case OIDArrayXid8:
		return OIDXid8, true
	case OIDArrayTid:
		return OIDTid, true
	case OIDArrayXid:
		return OIDXid, true
	case OIDArrayCid:
		return OIDCid, true
	case OIDArrayRegproc:
		return OIDRegproc, true
	case OIDArrayRegprocedure:
		return OIDRegprocedure, true
	case OIDArrayRegoper:
		return OIDRegoper, true
	case OIDArrayRegoperator:
		return OIDRegoperator, true
	case OIDArrayRegclass:
		return OIDRegclass, true
	case OIDArrayRegtype:
		return OIDRegtype, true
	case OIDArrayRegconfig:
		return OIDRegconfig, true
	case OIDArrayRegdictionary:
		return OIDRegdictionary, true
	case OIDArrayRegnamespace:
		return OIDRegnamespace, true
	case OIDArrayRegrole:
		return OIDRegrole, true
	case OIDArrayRegcollation:
		return OIDRegcollation, true
	case OIDArrayInt2vector:
		return OIDInt2vector, true
	case OIDArrayOidvector:
		return OIDOidvector, true
	}
	return 0, false
}

// PGClassRow is the v0 on-disk shape of one pg_class tuple.
// Only the subset of columns needed to reconstruct the in-memory
// catalog state is stored; the full upstream shape is deferred.
type PGClassRow struct {
	OID            uint32 // oid
	RelName        string // relname
	RelNamespace   uint32 // relnamespace (schema OID)
	RelKind        string // relkind: 'r'=table,'i'=index,'v'=view,'S'=seq
	RelNAtts       int32  // relnatts
	RelFileNode    uint32 // relfilenode (0 for virtual / view)
	RelPersistence string // relpersistence: 'p'=permanent
	RelIsShared    bool   // relisshared
}

// PGAttributeRow is the v0 on-disk shape of one pg_attribute tuple.
type PGAttributeRow struct {
	AttRelID     uint32 // attrelid (owning relation OID)
	AttName      string // attname
	AttTypID     uint32 // atttypid (pg_type OID)
	AttNum       int32  // attnum (1-based)
	AttNotNull   bool   // attnotnull
	AttIsDropped bool   // attisdropped
}

// PGTypeRow is the v0 on-disk shape of one pg_type tuple.
type PGTypeRow struct {
	OID          uint32 // oid
	TypName      string // typname
	TypNamespace uint32 // typnamespace
	TypLen       int32  // typlen (-1 = variable)
	TypByVal     bool   // typbyval (passed by value)
	TypType      string // typtype: 'b'=base, 'c'=composite, etc.
	TypCategory  string // typcategory
}

// PGClassColumns returns the column schema for pg_class heap rows.
// The column types must stay in sync with EncodePGClassRow /
// DecodePGClassRow so executor.DecodeRowInto reads them correctly.
func PGClassColumns() []Column {
	return []Column{
		{Name: "oid", Type: Type{Name: "int4"}, Ordinal: 0},
		{Name: "relname", Type: Type{Name: "text"}, Ordinal: 1},
		{Name: "relnamespace", Type: Type{Name: "int4"}, Ordinal: 2},
		{Name: "relkind", Type: Type{Name: "text"}, Ordinal: 3},
		{Name: "relnatts", Type: Type{Name: "int4"}, Ordinal: 4},
		{Name: "relfilenode", Type: Type{Name: "int4"}, Ordinal: 5},
		{Name: "relpersistence", Type: Type{Name: "text"}, Ordinal: 6},
		{Name: "relisshared", Type: Type{Name: "bool"}, Ordinal: 7},
	}
}

// PGAttributeColumns returns the column schema for pg_attribute heap rows.
// Matches the 25-column layout written by initdb.pgAttrColDefs. The physical
// heap encoding follows this column order, so the schema must agree with it
// for the executor's decoder to read correct values.
//
// NOTE: attstattarget is appended LAST rather than placed at its PG18-canonical
// position (#4, after atttypid). goopg's on-disk pg_attribute layout is already
// non-canonical (it omits attcacheoff and orders attlen before attnum), and the
// fixed-offset physical decoder (DecodePGAttributePhysicalRow) reads attrelid/
// attname/atttypid/attnum/attnotnull/attisdropped by hardcoded byte offset.
// Appending attstattarget as a nullable trailing column (always NULL, exactly
// like attacl/attoptions/attfdwoptions/attmissingval) keeps every existing byte
// offset valid and grows the null bitmap 3→4 bytes within the same MAXALIGN(8)
// boundary (t_hoff stays 32). SELECT resolves columns by name, so the trailing
// position is transparent to queries such as pg_dump's getTableAttrs.
func PGAttributeColumns() []Column {
	cols := []Column{
		{Name: "attrelid", Type: Type{Name: "oid"}},
		{Name: "attname", Type: Type{Name: "name"}},
		{Name: "atttypid", Type: Type{Name: "oid"}},
		{Name: "attlen", Type: Type{Name: "int2"}},
		{Name: "attnum", Type: Type{Name: "int2"}},
		{Name: "atttypmod", Type: Type{Name: "int4"}},
		{Name: "attndims", Type: Type{Name: "int2"}},
		{Name: "attbyval", Type: Type{Name: "bool"}},
		{Name: "attalign", Type: Type{Name: "char"}},
		{Name: "attstorage", Type: Type{Name: "char"}},
		{Name: "attcompression", Type: Type{Name: "char"}},
		{Name: "attnotnull", Type: Type{Name: "bool"}},
		{Name: "atthasdef", Type: Type{Name: "bool"}},
		{Name: "atthasmissing", Type: Type{Name: "bool"}},
		{Name: "attidentity", Type: Type{Name: "char"}},
		{Name: "attgenerated", Type: Type{Name: "char"}},
		{Name: "attisdropped", Type: Type{Name: "bool"}},
		{Name: "attislocal", Type: Type{Name: "bool"}},
		{Name: "attinhcount", Type: Type{Name: "int2"}},
		{Name: "attcollation", Type: Type{Name: "oid"}},
		{Name: "attacl", Type: Type{Name: "text"}},
		{Name: "attoptions", Type: Type{Name: "text"}},
		{Name: "attfdwoptions", Type: Type{Name: "text"}},
		{Name: "attmissingval", Type: Type{Name: "text"}},
		{Name: "attstattarget", Type: Type{Name: "int2"}},
	}
	for i := range cols {
		cols[i].Ordinal = i
	}
	return cols
}

// PGTypeColumns returns the column schema for pg_type heap rows.
func PGTypeColumns() []Column {
	// Full 32-column PG18-canonical schema matching pgTypeColDefs() in initdb.
	// Expanded from 7 to 32 columns (M0097-0146) so SeqScan can correctly
	// decode the physical rows written by bootstrapPgTypeTuples and
	// syncEnumTypeToCatalogHeap. Column order and types match the
	// FormData_pg_type struct (postgres/src/include/catalog/pg_type.h).
	return []Column{
		{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
		{Name: "typname", Type: Type{Name: "name"}, Ordinal: 1},
		{Name: "typnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
		{Name: "typowner", Type: Type{Name: "oid"}, Ordinal: 3},
		{Name: "typlen", Type: Type{Name: "int2"}, Ordinal: 4},
		{Name: "typbyval", Type: Type{Name: "bool"}, Ordinal: 5},
		{Name: "typtype", Type: Type{Name: "char"}, Ordinal: 6},
		{Name: "typcategory", Type: Type{Name: "char"}, Ordinal: 7},
		{Name: "typispreferred", Type: Type{Name: "bool"}, Ordinal: 8},
		{Name: "typisdefined", Type: Type{Name: "bool"}, Ordinal: 9},
		{Name: "typdelim", Type: Type{Name: "char"}, Ordinal: 10},
		{Name: "typrelid", Type: Type{Name: "oid"}, Ordinal: 11},
		{Name: "typsubscript", Type: Type{Name: "regproc"}, Ordinal: 12},
		{Name: "typelem", Type: Type{Name: "oid"}, Ordinal: 13},
		{Name: "typarray", Type: Type{Name: "oid"}, Ordinal: 14},
		{Name: "typinput", Type: Type{Name: "regproc"}, Ordinal: 15},
		{Name: "typoutput", Type: Type{Name: "regproc"}, Ordinal: 16},
		{Name: "typreceive", Type: Type{Name: "regproc"}, Ordinal: 17},
		{Name: "typsend", Type: Type{Name: "regproc"}, Ordinal: 18},
		{Name: "typmodin", Type: Type{Name: "regproc"}, Ordinal: 19},
		{Name: "typmodout", Type: Type{Name: "regproc"}, Ordinal: 20},
		{Name: "typanalyze", Type: Type{Name: "regproc"}, Ordinal: 21},
		{Name: "typalign", Type: Type{Name: "char"}, Ordinal: 22},
		{Name: "typstorage", Type: Type{Name: "char"}, Ordinal: 23},
		{Name: "typnotnull", Type: Type{Name: "bool"}, Ordinal: 24},
		{Name: "typbasetype", Type: Type{Name: "oid"}, Ordinal: 25},
		{Name: "typtypmod", Type: Type{Name: "int4"}, Ordinal: 26},
		{Name: "typndims", Type: Type{Name: "int4"}, Ordinal: 27},
		{Name: "typcollation", Type: Type{Name: "oid"}, Ordinal: 28},
		{Name: "typdefaultbin", Type: Type{Name: "pg_node_tree"}, Ordinal: 29},
		{Name: "typdefault", Type: Type{Name: "text"}, Ordinal: 30},
		{Name: "typacl", Type: Type{Name: "aclitem[]"}, Ordinal: 31},
	}
}

// --- Encode functions ---

// EncodePGClassRow serialises r into the executor-compatible binary format.
func EncodePGClassRow(r PGClassRow) []byte {
	var b []byte
	b = appendInt4Col(b, int32(r.OID))
	b = appendVarlenCol(b, r.RelName)
	b = appendInt4Col(b, int32(r.RelNamespace))
	b = appendVarlenCol(b, r.RelKind)
	b = appendInt4Col(b, r.RelNAtts)
	b = appendInt4Col(b, int32(r.RelFileNode))
	b = appendVarlenCol(b, r.RelPersistence)
	b = appendBoolCol(b, r.RelIsShared)
	return b
}

// EncodePGAttributeRow serialises r into the executor-compatible format.
func EncodePGAttributeRow(r PGAttributeRow) []byte {
	var b []byte
	b = appendInt4Col(b, int32(r.AttRelID))
	b = appendVarlenCol(b, r.AttName)
	b = appendInt4Col(b, int32(r.AttTypID))
	b = appendInt4Col(b, r.AttNum)
	b = appendBoolCol(b, r.AttNotNull)
	b = appendBoolCol(b, r.AttIsDropped)
	return b
}

// EncodePGTypeRow serialises r into the executor-compatible format.
func EncodePGTypeRow(r PGTypeRow) []byte {
	var b []byte
	b = appendInt4Col(b, int32(r.OID))
	b = appendVarlenCol(b, r.TypName)
	b = appendInt4Col(b, int32(r.TypNamespace))
	b = appendInt4Col(b, r.TypLen)
	b = appendBoolCol(b, r.TypByVal)
	b = appendVarlenCol(b, r.TypType)
	b = appendVarlenCol(b, r.TypCategory)
	return b
}

// --- Decode functions ---

// DecodePGClassRow parses the binary data produced by EncodePGClassRow.
func DecodePGClassRow(data []byte) (PGClassRow, error) {
	var r PGClassRow
	var off int
	var err error

	var v int32
	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.oid: %w", err)
	}
	r.OID = uint32(v)

	r.RelName, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relname: %w", err)
	}

	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relnamespace: %w", err)
	}
	r.RelNamespace = uint32(v)

	r.RelKind, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relkind: %w", err)
	}

	r.RelNAtts, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relnatts: %w", err)
	}

	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relfilenode: %w", err)
	}
	r.RelFileNode = uint32(v)

	r.RelPersistence, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relpersistence: %w", err)
	}

	r.RelIsShared, _, err = nextBool(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_class.relisshared: %w", err)
	}

	return r, nil
}

// DecodePGClassPhysicalRow parses the fixed-layout PostgreSQL heap tuple data
// for pg_class. Only the non-null fixed fields needed by loadUserTablesFromHeap
// are decoded.
func DecodePGClassPhysicalRow(data []byte) (PGClassRow, error) {
	var r PGClassRow
	if len(data) < pgClassFixedPartSize {
		return r, fmt.Errorf("pg_class physical row too short: len=%d", len(data))
	}
	relName := decodePGName(data[pgClassOffRelName : pgClassOffRelName+pgNameDataLen])
	if relName == "" {
		return r, fmt.Errorf("pg_class.relname: empty")
	}
	relIsShared, err := decodePGBool(data[pgClassOffRelIsShared], "pg_class.relisshared")
	if err != nil {
		return r, err
	}
	relPersistence := string([]byte{data[pgClassOffRelPersistence]})
	if relPersistence != "p" && relPersistence != "u" && relPersistence != "t" {
		return r, fmt.Errorf("pg_class.relpersistence: invalid %q", relPersistence)
	}
	relKind := string([]byte{data[pgClassOffRelKind]})
	switch relKind {
	case "r", "i", "S", "t", "v", "m", "c", "f", "p", "I":
	default:
		return r, fmt.Errorf("pg_class.relkind: invalid %q", relKind)
	}
	relNAtts := int32(int16(binary.LittleEndian.Uint16(data[pgClassOffRelNAtts : pgClassOffRelNAtts+2])))
	if relNAtts < 0 {
		return r, fmt.Errorf("pg_class.relnatts: invalid %d", relNAtts)
	}
	r = PGClassRow{
		OID:            binary.LittleEndian.Uint32(data[pgClassOffOID : pgClassOffOID+4]),
		RelName:        relName,
		RelNamespace:   binary.LittleEndian.Uint32(data[pgClassOffRelNamespace : pgClassOffRelNamespace+4]),
		RelKind:        relKind,
		RelNAtts:       relNAtts,
		RelFileNode:    binary.LittleEndian.Uint32(data[pgClassOffRelFileNode : pgClassOffRelFileNode+4]),
		RelPersistence: relPersistence,
		RelIsShared:    relIsShared,
	}
	return r, nil
}

// DecodePGAttributeRow parses the binary data produced by EncodePGAttributeRow.
func DecodePGAttributeRow(data []byte) (PGAttributeRow, error) {
	var r PGAttributeRow
	var off int
	var err error

	var v int32
	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.attrelid: %w", err)
	}
	r.AttRelID = uint32(v)

	r.AttName, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.attname: %w", err)
	}

	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.atttypid: %w", err)
	}
	r.AttTypID = uint32(v)

	r.AttNum, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.attnum: %w", err)
	}

	r.AttNotNull, off, err = nextBool(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.attnotnull: %w", err)
	}

	r.AttIsDropped, _, err = nextBool(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_attribute.attisdropped: %w", err)
	}

	return r, nil
}

// DecodePGAttributePhysicalRow parses the fixed-layout PostgreSQL heap tuple
// data for pg_attribute. Only the fixed fields needed to recover user columns
// are decoded.
func DecodePGAttributePhysicalRow(data []byte) (PGAttributeRow, error) {
	var r PGAttributeRow
	if len(data) < pgAttributeFixedPartSize {
		return r, fmt.Errorf("pg_attribute physical row too short: len=%d", len(data))
	}
	attName := decodePGName(data[pgAttributeOffName : pgAttributeOffName+pgNameDataLen])
	if attName == "" {
		return r, fmt.Errorf("pg_attribute.attname: empty")
	}
	attNotNull, err := decodePGBool(data[pgAttributeOffNotNull], "pg_attribute.attnotnull")
	if err != nil {
		return r, err
	}
	attIsDropped, err := decodePGBool(data[pgAttributeOffIsDropped], "pg_attribute.attisdropped")
	if err != nil {
		return r, err
	}
	attNum := int32(int16(binary.LittleEndian.Uint16(data[pgAttributeOffNum : pgAttributeOffNum+2])))
	if attNum == 0 {
		return r, fmt.Errorf("pg_attribute.attnum: invalid 0")
	}
	r = PGAttributeRow{
		AttRelID:     binary.LittleEndian.Uint32(data[pgAttributeOffRelID : pgAttributeOffRelID+4]),
		AttName:      attName,
		AttTypID:     binary.LittleEndian.Uint32(data[pgAttributeOffTypID : pgAttributeOffTypID+4]),
		AttNum:       attNum,
		AttNotNull:   attNotNull,
		AttIsDropped: attIsDropped,
	}
	if !r.AttIsDropped && r.AttTypID == 0 {
		return r, fmt.Errorf("pg_attribute.atttypid: invalid 0")
	}
	return r, nil
}

// DecodePGTypeRow parses the binary data produced by EncodePGTypeRow.
func DecodePGTypeRow(data []byte) (PGTypeRow, error) {
	var r PGTypeRow
	var off int
	var err error

	var v int32
	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.oid: %w", err)
	}
	r.OID = uint32(v)

	r.TypName, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typname: %w", err)
	}

	v, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typnamespace: %w", err)
	}
	r.TypNamespace = uint32(v)

	r.TypLen, off, err = nextInt4(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typlen: %w", err)
	}

	r.TypByVal, off, err = nextBool(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typbyval: %w", err)
	}

	r.TypType, off, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typtype: %w", err)
	}

	r.TypCategory, _, err = nextVarlen(data, off)
	if err != nil {
		return r, fmt.Errorf("pg_type.typcategory: %w", err)
	}

	return r, nil
}

// DecodePGTypePhysicalRow parses the fixed-layout PostgreSQL heap tuple data
// for pg_type. Only the fields present in PGTypeRow are decoded.
func DecodePGTypePhysicalRow(data []byte) (PGTypeRow, error) {
	const pgTypeMinSize = 81 // to reach typcategory at offset 80
	var r PGTypeRow
	if len(data) < pgTypeMinSize {
		return r, fmt.Errorf("pg_type physical row too short: len=%d", len(data))
	}
	r.OID = binary.LittleEndian.Uint32(data[0:4])
	// typname: 64-byte NameData at offset 4, null-terminated
	end := bytes.IndexByte(data[4:68], 0)
	if end < 0 {
		end = 64
	}
	r.TypName = string(data[4 : 4+end])
	r.TypNamespace = binary.LittleEndian.Uint32(data[68:72])
	// typowner at 72 (4 bytes): skip
	r.TypLen = int32(int16(binary.LittleEndian.Uint16(data[76:78])))
	r.TypByVal = data[78] != 0
	r.TypType = string(data[79:80])
	r.TypCategory = string(data[80:81])
	return r, nil
}

// --- Encoding helpers ---

// appendInt4Col appends a not-null int4 column: 1 null byte (0) + 4 big-endian bytes.
func appendInt4Col(b []byte, v int32) []byte {
	b = append(b, 0) // not null
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(v))
	return append(b, tmp[:]...)
}

// appendBoolCol appends a not-null bool column: 1 null byte (0) + 1 value byte.
func appendBoolCol(b []byte, v bool) []byte {
	b = append(b, 0) // not null
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}

// appendVarlenCol appends a not-null text column:
// 1 null byte (0) + 4-byte big-endian length + raw UTF-8 bytes.
func appendVarlenCol(b []byte, s string) []byte {
	b = append(b, 0) // not null
	raw := []byte(s)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(len(raw)))
	b = append(b, tmp[:]...)
	return append(b, raw...)
}

// --- Decoding helpers (return value, new-offset, error) ---

func nextInt4(data []byte, off int) (int32, int, error) {
	if off >= len(data) {
		return 0, off, fmt.Errorf("truncated at offset %d", off)
	}
	nullByte := data[off]
	off++
	if nullByte != 0 {
		return 0, off, fmt.Errorf("unexpected null flag %d", nullByte)
	}
	if off+4 > len(data) {
		return 0, off, fmt.Errorf("truncated int4 at offset %d", off)
	}
	v := int32(binary.BigEndian.Uint32(data[off : off+4]))
	return v, off + 4, nil
}

func nextBool(data []byte, off int) (bool, int, error) {
	if off >= len(data) {
		return false, off, fmt.Errorf("truncated at offset %d", off)
	}
	nullByte := data[off]
	off++
	if nullByte != 0 {
		return false, off, fmt.Errorf("unexpected null flag %d", nullByte)
	}
	if off >= len(data) {
		return false, off, fmt.Errorf("truncated bool at offset %d", off)
	}
	return data[off] != 0, off + 1, nil
}

func nextVarlen(data []byte, off int) (string, int, error) {
	if off >= len(data) {
		return "", off, fmt.Errorf("truncated at offset %d", off)
	}
	nullByte := data[off]
	off++
	if nullByte != 0 {
		return "", off, fmt.Errorf("unexpected null flag %d", nullByte)
	}
	if off+4 > len(data) {
		return "", off, fmt.Errorf("truncated varlen length at offset %d", off)
	}
	sz := int(binary.BigEndian.Uint32(data[off : off+4]))
	off += 4
	if off+sz > len(data) {
		return "", off, fmt.Errorf("truncated varlen value at offset %d (len=%d)", off, sz)
	}
	s := string(data[off : off+sz])
	return s, off + sz, nil
}

func decodePGName(raw []byte) string {
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		end = len(raw)
	}
	return string(raw[:end])
}

func decodePGBool(v byte, field string) (bool, error) {
	switch v {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("%s: invalid bool %d", field, v)
	}
}

// PGIndexRow holds the fields from pg_index needed for catalog recovery.
type PGIndexRow struct {
	IndexRelid   uint32  // indexrelid
	IndRelid     uint32  // indrelid (owning table OID)
	IndNAtts     int16   // indnatts
	IndKey       []int16 // attnum values for each indexed column
	IndIsUnique  bool    // indisunique
	IndIsPrimary bool    // indisprimary
}

const (
	// pgIndexFixedSize is the byte length of the fixed-width prefix of a
	// pg_index physical heap tuple up to (and including) the padding byte
	// before indkey. Layout: indexrelid[4] + indrelid[4] + indnatts[2] +
	// indnkeyatts[2] + 11 bool fields[11] + 1 pad = 24 bytes.
	pgIndexFixedSize       = 24
	pgIndexOffIndexRelid   = 0
	pgIndexOffIndRelid     = 4
	pgIndexOffIndNAtts     = 8
	pgIndexOffIndIsUnique  = 12
	pgIndexOffIndIsPrimary = 14
	// indkey int2vector starts at offset 24 after the 1-byte alignment pad.
	pgIndexOffIndKey = 24
)

// DecodePGIndexPhysicalRow decodes the PG18 physical on-disk format of a
// pg_index tuple. Only the fields needed for catalog recovery are extracted.
// The int2vector indkey blob is decoded to extract attnum values.
func DecodePGIndexPhysicalRow(data []byte) (PGIndexRow, error) {
	var r PGIndexRow
	if len(data) < pgIndexFixedSize {
		return r, fmt.Errorf("pg_index physical row too short: len=%d", len(data))
	}
	r.IndexRelid = binary.LittleEndian.Uint32(data[pgIndexOffIndexRelid : pgIndexOffIndexRelid+4])
	r.IndRelid = binary.LittleEndian.Uint32(data[pgIndexOffIndRelid : pgIndexOffIndRelid+4])
	r.IndNAtts = int16(binary.LittleEndian.Uint16(data[pgIndexOffIndNAtts : pgIndexOffIndNAtts+2]))
	r.IndIsUnique = data[pgIndexOffIndIsUnique] != 0
	r.IndIsPrimary = data[pgIndexOffIndIsPrimary] != 0

	// Decode indkey int2vector (ArrayType varlena) at fixed offset 24.
	// int2VectorBytes layout: vl_len_[4] + ndim[4] + dataoff[4] +
	// elemtype[4] + dims[4] + lbounds[4] = 24-byte header, then n*2 data.
	const vectorHdrSize = 24
	if len(data) < pgIndexOffIndKey+vectorHdrSize {
		return r, fmt.Errorf("pg_index: indkey varlena header truncated")
	}
	blob := data[pgIndexOffIndKey:]
	n := int(binary.LittleEndian.Uint32(blob[16:20])) // dims[0]
	if n < 0 || n > 1024 {
		return r, fmt.Errorf("pg_index: indkey element count out of range: %d", n)
	}
	needed := vectorHdrSize + 2*n
	if len(blob) < needed {
		return r, fmt.Errorf("pg_index: indkey data truncated (need %d got %d)", needed, len(blob))
	}
	r.IndKey = make([]int16, n)
	for i := range r.IndKey {
		r.IndKey[i] = int16(binary.LittleEndian.Uint16(blob[vectorHdrSize+2*i : vectorHdrSize+2*i+2]))
	}
	return r, nil
}

// PGStatisticRow holds the fields from pg_statistic needed for in-memory
// planner statistics reconstruction.
type PGStatisticRow struct {
	StaRelid    uint32    // starelid — owning relation OID
	StaAttNum   int16     // staattnum — column number (1-based)
	StaNullFrac float32   // stanullfrac
	StaWidth    int32     // stawidth (avg column width in bytes)
	StaDistinct float32   // stadistinct (<0 → fraction; >0 → count)
	StaKind1    int16     // stakind1 (1=MCV, 2=histogram, 0=empty)
	StaKind2    int16     // stakind2
	MCVFreqs    []float32 // stanumbers1 when stakind1==STATISTIC_KIND_MCV
	MCVValues   []string  // stavalues1 decoded as text
	HistBounds  []string  // stavalues2 decoded as text when stakind2==STATISTIC_KIND_HISTOGRAM
}

// pgStatisticPhysicalFixed is the byte length of the fixed-size prefix of a
// pg_statistic physical tuple. Layout (with C-struct alignment):
//
//	starelid[4] staattnum[2] stainherit[1] pad[1] stanullfrac[4] stawidth[4]
//	stadistinct[4] stakind1[2] stakind2[2] stakind3[2] stakind4[2] stakind5[2]
//	pad[2] staop1..5[20] stacoll1..5[20] = 72 bytes
const pgStatisticPhysicalFixed = 72

const (
	pgStatOffStaRelid    = 0
	pgStatOffStaAttNum   = 4
	pgStatOffStaNullFrac = 8 // after bool + 1 pad → aligned to 4
	pgStatOffStaWidth    = 12
	pgStatOffStaDistinct = 16
	pgStatOffStaKind1    = 20
	pgStatOffStaKind2    = 22
)

// DecodePGStatisticPhysicalRow decodes the PG18 physical on-disk format of a
// pg_statistic tuple. The nullable varlena columns (stanumbers1-5,
// stavalues1-5) are decoded using the null bitmap from the heap tuple.
//
// bitmap is the heap tuple null bitmap (may be nil when HEAP_HASNULL is not
// set). Column numbering is 1-based (matching pg_statistic attnum).
func DecodePGStatisticPhysicalRow(data []byte, bitmap []byte) (PGStatisticRow, error) {
	var r PGStatisticRow
	if len(data) < pgStatisticPhysicalFixed {
		return r, fmt.Errorf("pg_statistic physical row too short: len=%d", len(data))
	}
	r.StaRelid = binary.LittleEndian.Uint32(data[pgStatOffStaRelid : pgStatOffStaRelid+4])
	r.StaAttNum = int16(binary.LittleEndian.Uint16(data[pgStatOffStaAttNum : pgStatOffStaAttNum+2]))
	// float4 stored as IEEE-754 little-endian
	r.StaNullFrac = math.Float32frombits(binary.LittleEndian.Uint32(data[pgStatOffStaNullFrac : pgStatOffStaNullFrac+4]))
	r.StaWidth = int32(binary.LittleEndian.Uint32(data[pgStatOffStaWidth : pgStatOffStaWidth+4]))
	r.StaDistinct = math.Float32frombits(binary.LittleEndian.Uint32(data[pgStatOffStaDistinct : pgStatOffStaDistinct+4]))
	r.StaKind1 = int16(binary.LittleEndian.Uint16(data[pgStatOffStaKind1 : pgStatOffStaKind1+2]))
	r.StaKind2 = int16(binary.LittleEndian.Uint16(data[pgStatOffStaKind2 : pgStatOffStaKind2+2]))

	// Varlena columns follow the fixed part. Columns 22-31 (stanumbers1-5,
	// stavalues1-5) are nullable. Each may be NULL (bit clear in bitmap)
	// or present (varlena bytes in data).
	//
	// isNull returns true when column col (1-based) is marked NULL in the
	// null bitmap. If bitmap is nil all columns are non-null.
	isNull := func(col int) bool {
		if len(bitmap) == 0 {
			return false
		}
		idx := col - 1 // 0-based
		return (bitmap[idx/8]>>(uint(idx)%8))&1 == 0
	}

	// Advance through varlena columns 22-31 in order.
	// We only care about stanumbers1 (col 22) and stavalues1-2 (cols 27, 28).
	off := pgStatisticPhysicalFixed

	readVarlena := func(col int) ([]byte, error) {
		if isNull(col) {
			return nil, nil
		}
		// Align to 4 bytes (oidvector/anyarray/_float4 all use 'i' alignment).
		off = (off + 3) &^ 3
		if off+4 > len(data) {
			return nil, fmt.Errorf("pg_statistic col %d: varlena header truncated", col)
		}
		sz := int(binary.LittleEndian.Uint32(data[off:off+4]) >> 2)
		if off+sz > len(data) {
			return nil, fmt.Errorf("pg_statistic col %d: varlena body truncated", col)
		}
		blob := data[off : off+sz]
		off += sz
		return blob, nil
	}

	// stanumbers1-5 (cols 22-26)
	for col := 22; col <= 26; col++ {
		blob, err := readVarlena(col)
		if err != nil {
			return r, err
		}
		if col == 22 && blob != nil {
			r.MCVFreqs = decodeFloat4Array(blob)
		}
	}
	// stavalues1-5 (cols 27-31)
	for col := 27; col <= 31; col++ {
		blob, err := readVarlena(col)
		if err != nil {
			return r, err
		}
		if blob == nil {
			continue
		}
		switch col {
		case 27:
			r.MCVValues = decodeTextArray(blob)
		case 28:
			if r.StaKind2 == 2 { // STATISTIC_KIND_HISTOGRAM
				r.HistBounds = decodeTextArray(blob)
			}
		}
	}
	return r, nil
}

// decodeFloat4Array decodes a PG ArrayType blob containing float4 elements.
// Returns nil for malformed blobs.
func decodeFloat4Array(blob []byte) []float32 {
	const hdrSize = 24
	if len(blob) < hdrSize {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(blob[16:20])) // dims[0]
	if n <= 0 || len(blob) < hdrSize+4*n {
		return nil
	}
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[hdrSize+4*i : hdrSize+4*i+4]))
	}
	return out
}

// decodeTextArray decodes a PG ArrayType blob with text (OID 25) elements.
// Each element is a varlena with 4-byte little-endian header (len << 2).
// Returns nil for malformed blobs.
func decodeTextArray(blob []byte) []string {
	const hdrSize = 24
	if len(blob) < hdrSize {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(blob[16:20])) // dims[0]
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	off := hdrSize
	for i := 0; i < n; i++ {
		// Each text element in an array has a 4-byte varlena header.
		if off+4 > len(blob) {
			break
		}
		sz := int(binary.LittleEndian.Uint32(blob[off:off+4]) >> 2)
		if sz < 4 || off+sz > len(blob) {
			break
		}
		out = append(out, string(blob[off+4:off+sz]))
		off += sz
	}
	return out
}

// TypeNameToOID maps a goopg type name string to its canonical pg_type OID.
// Used by DDL-sync (operators_ddl.go) when writing pg_attribute rows, by
// initdb seeding (catalog_seed.go), and by pgoutput.go for column OID
// resolution (M0030-0005). Returns OIDText as a safe fallback for unknown
// types.
func TypeNameToOID(typName string) uint32 {
	switch strings.ToLower(typName) {
	case "int4", "int", "integer":
		return OIDInt4
	case "int8", "bigint":
		return OIDInt8
	case "int2", "smallint":
		return OIDInt2
	case "text":
		return OIDText
	case "varchar", "character varying":
		return OIDVarChar
	case "char", "character", "bpchar":
		return OIDBpChar
	case "bool", "boolean":
		return OIDBool
	case "bytea":
		return OIDBytea
	case "float4", "real":
		return OIDFloat4
	case "float8", "double precision":
		return OIDFloat8
	case "date":
		return OIDDate
	case "time", "time without time zone":
		return OIDTime
	case "timetz", "time with time zone":
		return OIDTimeTZ
	case "timestamp", "timestamp without time zone":
		return OIDTimestamp
	case "timestamptz", "timestamp with time zone":
		return OIDTimestampTZ
	case "numeric", "decimal":
		return OIDNumeric
	case "uuid":
		return OIDUUID
	case "oid":
		return OIDOID
	case "json":
		return OIDJSON
	case "jsonb":
		return OIDJsonb
	case "jsonpath":
		return OIDJsonpath
	case "refcursor":
		return OIDRefcursor
	case "interval":
		return OIDInterval
	case "inet":
		return OIDInet
	case "cidr":
		return OIDCidr
	case "macaddr":
		return OIDMacaddr
	case "macaddr8":
		return OIDMacaddr8
	case "point":
		return OIDPoint
	case "lseg":
		return OIDLseg
	case "path":
		return OIDPath
	case "box":
		return OIDBox
	case "polygon":
		return OIDPolygon
	case "line":
		return OIDLine
	case "circle":
		return OIDCircle
	case "tsvector":
		return OIDTsvector
	case "tsquery":
		return OIDTsquery
	case "xml":
		return OIDXML
	case "money":
		return OIDMoney
	case "bit":
		return OIDBit
	case "varbit", "bit varying":
		return OIDVarbit
	case "pg_lsn":
		return OIDPgLsn
	case "txid_snapshot":
		return OIDTxidSnapshot
	case "pg_snapshot":
		return OIDPgSnapshot
	case "xid8":
		return OIDXid8
	case "tid":
		return OIDTid
	case "xid":
		return OIDXid
	case "cid":
		return OIDCid
	case "regproc":
		return OIDRegproc
	case "regprocedure":
		return OIDRegprocedure
	case "regoper":
		return OIDRegoper
	case "regoperator":
		return OIDRegoperator
	case "regclass":
		return OIDRegclass
	case "regtype":
		return OIDRegtype
	case "regconfig":
		return OIDRegconfig
	case "regdictionary":
		return OIDRegdictionary
	case "regnamespace":
		return OIDRegnamespace
	case "regrole":
		return OIDRegrole
	case "regcollation":
		return OIDRegcollation
	case "int2vector":
		return OIDInt2vector
	case "oidvector":
		return OIDOidvector
	case "name":
		return OIDName
	default:
		return OIDText // safe fallback
	}
}

// OIDToTypeName is the inverse of TypeNameToOID: maps a canonical pg_type
// OID to the goopg type name string used in catalog.Type. Used by
// loadUserTablesFromHeap (M0030-0003) when reconstructing column types
// from pg_attribute rows. Extended in M0030-0005 to cover all OIDs that
// TypeNameToOID produces. Returns "text" for unknown OIDs.
func OIDToTypeName(oid uint32) string {
	switch oid {
	case OIDInt4:
		return "int4"
	case OIDInt8:
		return "int8"
	case OIDInt2:
		return "int2"
	case OIDText:
		return "text"
	case OIDVarChar:
		return "varchar"
	case OIDBpChar:
		return "bpchar"
	case OIDBool:
		return "bool"
	case OIDBytea:
		return "bytea"
	case OIDFloat4:
		return "float4"
	case OIDFloat8:
		return "float8"
	case OIDDate:
		return "date"
	case OIDTime:
		return "time"
	case OIDTimeTZ:
		return "timetz"
	case OIDTimestamp:
		return "timestamp"
	case OIDTimestampTZ:
		return "timestamptz"
	case OIDNumeric:
		return "numeric"
	case OIDUUID:
		return "uuid"
	case OIDOID:
		return "oid"
	case OIDJSON:
		return "json"
	case OIDJsonb:
		return "jsonb"
	case OIDJsonpath:
		return "jsonpath"
	case OIDRefcursor:
		return "refcursor"
	case OIDInterval:
		return "interval"
	case OIDInet:
		return "inet"
	case OIDCidr:
		return "cidr"
	case OIDMacaddr:
		return "macaddr"
	case OIDMacaddr8:
		return "macaddr8"
	case OIDPoint:
		return "point"
	case OIDLseg:
		return "lseg"
	case OIDPath:
		return "path"
	case OIDBox:
		return "box"
	case OIDPolygon:
		return "polygon"
	case OIDLine:
		return "line"
	case OIDCircle:
		return "circle"
	case OIDTsvector:
		return "tsvector"
	case OIDTsquery:
		return "tsquery"
	case OIDXML:
		return "xml"
	case OIDMoney:
		return "money"
	case OIDBit:
		return "bit"
	case OIDVarbit:
		return "varbit"
	case OIDPgLsn:
		return "pg_lsn"
	case OIDTxidSnapshot:
		return "txid_snapshot"
	case OIDPgSnapshot:
		return "pg_snapshot"
	case OIDXid8:
		return "xid8"
	case OIDTid:
		return "tid"
	case OIDXid:
		return "xid"
	case OIDCid:
		return "cid"
	case OIDRegproc:
		return "regproc"
	case OIDRegprocedure:
		return "regprocedure"
	case OIDRegoper:
		return "regoper"
	case OIDRegoperator:
		return "regoperator"
	case OIDRegclass:
		return "regclass"
	case OIDRegtype:
		return "regtype"
	case OIDRegconfig:
		return "regconfig"
	case OIDRegdictionary:
		return "regdictionary"
	case OIDRegnamespace:
		return "regnamespace"
	case OIDRegrole:
		return "regrole"
	case OIDRegcollation:
		return "regcollation"
	case OIDInt2vector:
		return "int2vector"
	case OIDOidvector:
		return "oidvector"
	case OIDName:
		return "name"
	default:
		return "text"
	}
}
