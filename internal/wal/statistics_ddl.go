package wal

import (
	"encoding/binary"
	"fmt"
)

// CREATE/DROP STATISTICS WAL persistence (DU-002 restart-persistence
// follow-up to slice 441/M0119-0004). Extended-statistics objects
// (pg_statistic_ext) were never WAL-logged at all — a `CREATE STATISTICS`
// registered the object in catalog.InMemory.statisticsObjs only, so it
// silently vanished on restart and any subsequent `ALTER STATISTICS`
// mutation had nothing durable to attach to. Mirrors the CREATE/DROP
// COLLATION pair (RecordKindCreateCollation/RecordKindDropCollation): no
// per-object page-level state exists, so physical WAL replay treats these as
// opaque bytes and a dedicated logical recovery pass (replayStatisticsDDLRecords,
// internal/initdb) applies them to the catalog after physical replay finishes.
//
// ALTER STATISTICS (RENAME/OWNER TO/SET SCHEMA) is now WAL-logged too
// (RecordKindAlterStatisticsRename/Owner/SetSchema below), closing resume
// point (1) of the slice-441/445 ledger rows — mirroring how ALTER
// COLLATION's own three forms were added incrementally after CREATE/DROP
// COLLATION landed.
const (
	// RecordKindCreateStatistics records a `CREATE STATISTICS name (...) ON
	// ... FROM table` event. The OID is carried so recovery re-registers the
	// object identically to the live server; TableOID re-resolves the
	// `stxrelid` (pg_dump's dumpStatisticsExt reports it schema-qualified, but
	// the catalog only needs the OID to answer FROM-table queries).
	// Format:
	//   kind(1) | oid(4) | tableOID(4) | ownerOID(4) | hasExprFlag(1) |
	//   nameLen(2) | name | schemaLen(2) | schema |
	//   kindsCount(2) | (elemLen(2) | elem)* |
	//   columnsCount(2) | (elemLen(2) | elem)* |
	//   exprsCount(2) | (elemLen(2) | elem)*
	RecordKindCreateStatistics byte = 95

	// RecordKindDropStatistics records a `DROP STATISTICS name` event.
	// Counterpart to RecordKindCreateStatistics.
	// Format: kind(1) | nameLen(2) | name | schemaLen(2) | schema.
	RecordKindDropStatistics byte = 96

	// RecordKindAlterStatisticsRename records an `ALTER STATISTICS name RENAME
	// TO newname` event (DU-002 restart-persistence follow-up to slice
	// 441/445, resume point (1)). `name` is the (possibly schema-qualified)
	// key execAlterStatistics already resolves via RenameStatisticsObject.
	// Format: kind(1) | nameLen(2) | name | newNameLen(2) | newName.
	RecordKindAlterStatisticsRename byte = 97

	// RecordKindAlterStatisticsOwner records an `ALTER STATISTICS name OWNER
	// TO ...` event. Mirrors RecordKindAlterCollationOwner.
	// Format: kind(1) | ownerOID(4) | nameLen(2) | name.
	RecordKindAlterStatisticsOwner byte = 98

	// RecordKindAlterStatisticsSetSchema records an `ALTER STATISTICS name SET
	// SCHEMA newschema` event. Mirrors RecordKindAlterCollationSetSchema.
	// Format: kind(1) | nameLen(2) | name | newSchemaLen(2) | newSchema.
	RecordKindAlterStatisticsSetSchema byte = 99
)

func encodeStatisticsStringSlice(out []byte, off int, elems []string) int {
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(elems)))
	off += 2
	for _, e := range elems {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(e)))
		off += 2
		copy(out[off:], e)
		off += len(e)
	}
	return off
}

func statisticsStringSliceSize(elems []string) int {
	total := 2
	for _, e := range elems {
		total += 2 + len(e)
	}
	return total
}

func decodeStatisticsStringSlice(payload []byte, off int) ([]string, int, error) {
	if len(payload) < off+2 {
		return nil, off, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+2)
	}
	count := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	var elems []string
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return nil, off, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return nil, off, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+l)
		}
		elems = append(elems, string(payload[off:off+l]))
		off += l
	}
	return elems, off, nil
}

// EncodeCreateStatistics encodes a CREATE STATISTICS event. See
// RecordKindCreateStatistics for the wire format.
func EncodeCreateStatistics(name, schema string, oid, tableOID, ownerOID uint32, kinds, columns, exprs []string, hasExpr bool) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	total := 18 + len(name) + len(schema) +
		statisticsStringSliceSize(kinds) + statisticsStringSliceSize(columns) + statisticsStringSliceSize(exprs)
	out := make([]byte, total)
	out[0] = RecordKindCreateStatistics
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], tableOID)
	binary.LittleEndian.PutUint32(out[9:13], ownerOID)
	if hasExpr {
		out[13] = 1
	}
	off := 14
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	off = encodeStatisticsStringSlice(out, off, kinds)
	off = encodeStatisticsStringSlice(out, off, columns)
	_ = encodeStatisticsStringSlice(out, off, exprs)
	return out
}

// DecodeCreateStatistics decodes a RecordKindCreateStatistics payload.
func DecodeCreateStatistics(payload []byte) (name, schema string, oid, tableOID, ownerOID uint32, kinds, columns, exprs []string, hasExpr bool, err error) {
	if len(payload) < 14 {
		return "", "", 0, 0, 0, nil, nil, nil, false, fmt.Errorf("wal: create-statistics payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateStatistics {
		return "", "", 0, 0, 0, nil, nil, nil, false, fmt.Errorf("wal: record kind %d is not create-statistics", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	tableOID = binary.LittleEndian.Uint32(payload[5:9])
	ownerOID = binary.LittleEndian.Uint32(payload[9:13])
	hasExpr = payload[13] != 0
	off := 14
	if len(payload) < off+2 {
		return "", "", 0, 0, 0, nil, nil, nil, false, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+2)
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+nameLen+2 {
		return "", "", 0, 0, 0, nil, nil, nil, false, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", 0, 0, 0, nil, nil, nil, false, fmt.Errorf("wal: create-statistics payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	if kinds, off, err = decodeStatisticsStringSlice(payload, off); err != nil {
		return "", "", 0, 0, 0, nil, nil, nil, false, err
	}
	if columns, off, err = decodeStatisticsStringSlice(payload, off); err != nil {
		return "", "", 0, 0, 0, nil, nil, nil, false, err
	}
	if exprs, _, err = decodeStatisticsStringSlice(payload, off); err != nil {
		return "", "", 0, 0, 0, nil, nil, nil, false, err
	}
	return name, schema, oid, tableOID, ownerOID, kinds, columns, exprs, hasExpr, nil
}

// EncodeDropStatistics encodes a DROP STATISTICS event. Format: kind(1) |
// nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeDropStatistics(name, schema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(schema))
	out[0] = RecordKindDropStatistics
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeDropStatistics decodes a RecordKindDropStatistics payload.
func DecodeDropStatistics(payload []byte) (name, schema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-statistics payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropStatistics {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-statistics", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: drop-statistics payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", fmt.Errorf("wal: drop-statistics payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, nil
}

// EncodeAlterStatisticsRename encodes an ALTER STATISTICS ... RENAME TO event
// (DU-002 restart-persistence follow-up to slice 441/445, resume point (1)).
// Format: kind(1) | nameLen(2) | name(nameLen bytes) | newNameLen(2) |
// newName(newNameLen bytes).
func EncodeAlterStatisticsRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newName))
	out[0] = RecordKindAlterStatisticsRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterStatisticsRename decodes a RecordKindAlterStatisticsRename payload.
func DecodeAlterStatisticsRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: alter-statistics-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterStatisticsRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-statistics-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-statistics-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", fmt.Errorf("wal: alter-statistics-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, newName, nil
}

// EncodeAlterStatisticsOwner encodes an ALTER STATISTICS ... OWNER TO event.
// Format: kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes).
func EncodeAlterStatisticsOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterStatisticsOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterStatisticsOwner decodes a RecordKindAlterStatisticsOwner payload.
func DecodeAlterStatisticsOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-statistics-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterStatisticsOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-statistics-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-statistics-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	name = string(payload[7 : 7+nameLen])
	return name, ownerOID, nil
}

// EncodeAlterStatisticsSetSchema encodes an ALTER STATISTICS ... SET SCHEMA
// event. Format: kind(1) | nameLen(2) | name(nameLen bytes) |
// newSchemaLen(2) | newSchema(newSchemaLen bytes).
func EncodeAlterStatisticsSetSchema(name, newSchema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newSchema))
	out[0] = RecordKindAlterStatisticsSetSchema
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newSchema)))
	off += 2
	copy(out[off:], newSchema)
	return out
}

// DecodeAlterStatisticsSetSchema decodes a RecordKindAlterStatisticsSetSchema
// payload.
func DecodeAlterStatisticsSetSchema(payload []byte) (name, newSchema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: alter-statistics-set-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterStatisticsSetSchema {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-statistics-set-schema", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-statistics-set-schema payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newSchemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newSchemaLen {
		return "", "", fmt.Errorf("wal: alter-statistics-set-schema payload truncated (need %d bytes)", off+newSchemaLen)
	}
	newSchema = string(payload[off : off+newSchemaLen])
	return name, newSchema, nil
}
