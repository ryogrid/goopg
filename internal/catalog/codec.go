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
	"encoding/binary"
	"fmt"
	"strings"
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
	OIDTimestamp   uint32 = 1114
	OIDTimestampTZ uint32 = 1184
	OIDBpChar      uint32 = 1042
	OIDVarChar     uint32 = 1043
	OIDNumeric     uint32 = 1700
)

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
func PGAttributeColumns() []Column {
	return []Column{
		{Name: "attrelid", Type: Type{Name: "int4"}, Ordinal: 0},
		{Name: "attname", Type: Type{Name: "text"}, Ordinal: 1},
		{Name: "atttypid", Type: Type{Name: "int4"}, Ordinal: 2},
		{Name: "attnum", Type: Type{Name: "int4"}, Ordinal: 3},
		{Name: "attnotnull", Type: Type{Name: "bool"}, Ordinal: 4},
		{Name: "attisdropped", Type: Type{Name: "bool"}, Ordinal: 5},
	}
}

// PGTypeColumns returns the column schema for pg_type heap rows.
func PGTypeColumns() []Column {
	return []Column{
		{Name: "oid", Type: Type{Name: "int4"}, Ordinal: 0},
		{Name: "typname", Type: Type{Name: "text"}, Ordinal: 1},
		{Name: "typnamespace", Type: Type{Name: "int4"}, Ordinal: 2},
		{Name: "typlen", Type: Type{Name: "int4"}, Ordinal: 3},
		{Name: "typbyval", Type: Type{Name: "bool"}, Ordinal: 4},
		{Name: "typtype", Type: Type{Name: "text"}, Ordinal: 5},
		{Name: "typcategory", Type: Type{Name: "text"}, Ordinal: 6},
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
	case "timestamp", "timestamp without time zone":
		return OIDTimestamp
	case "timestamptz", "timestamp with time zone":
		return OIDTimestampTZ
	case "numeric", "decimal":
		return OIDNumeric
	case "oid":
		return OIDOID
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
	case OIDTimestamp:
		return "timestamp"
	case OIDTimestampTZ:
		return "timestamptz"
	case OIDNumeric:
		return "numeric"
	case OIDOID:
		return "oid"
	default:
		return "text"
	}
}
