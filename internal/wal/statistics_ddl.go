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
// ALTER STATISTICS (RENAME/OWNER TO/SET SCHEMA) is not yet WAL-logged; that is
// tracked as a separate follow-up in the deferral ledger, matching how ALTER
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
