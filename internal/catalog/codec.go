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
// Matches the 24-column PG18 canonical layout written by initdb.pgAttrColDefs.
// The physical heap encoding follows this column order, so the schema must
// agree with it for the executor's decoder to read correct values.
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
	IndexRelid  uint32  // indexrelid
	IndRelid    uint32  // indrelid (owning table OID)
	IndNAtts    int16   // indnatts
	IndKey      []int16 // attnum values for each indexed column
	IndIsUnique bool    // indisunique
	IndIsPrimary bool   // indisprimary
}

const (
	// pgIndexFixedSize is the byte length of the fixed-width prefix of a
	// pg_index physical heap tuple up to (and including) the padding byte
	// before indkey. Layout: indexrelid[4] + indrelid[4] + indnatts[2] +
	// indnkeyatts[2] + 11 bool fields[11] + 1 pad = 24 bytes.
	pgIndexFixedSize        = 24
	pgIndexOffIndexRelid    = 0
	pgIndexOffIndRelid      = 4
	pgIndexOffIndNAtts      = 8
	pgIndexOffIndIsUnique   = 12
	pgIndexOffIndIsPrimary  = 14
	// indkey int2vector starts at offset 24 after the 1-byte alignment pad.
	pgIndexOffIndKey        = 24
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
//   starelid[4] staattnum[2] stainherit[1] pad[1] stanullfrac[4] stawidth[4]
//   stadistinct[4] stakind1[2] stakind2[2] stakind3[2] stakind4[2] stakind5[2]
//   pad[2] staop1..5[20] stacoll1..5[20] = 72 bytes
const pgStatisticPhysicalFixed = 72

const (
	pgStatOffStaRelid    = 0
	pgStatOffStaAttNum   = 4
	pgStatOffStaNullFrac = 8  // after bool + 1 pad → aligned to 4
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
