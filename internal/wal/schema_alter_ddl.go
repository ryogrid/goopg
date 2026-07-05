package wal

import (
	"encoding/binary"
	"fmt"
)

// ALTER SCHEMA RENAME TO / OWNER TO WAL persistence (DU-002 slice 440 resume
// point (3), M0110-0001). CREATE/DROP SCHEMA (kinds 34/35) were already
// WAL-logged; goopg had no RenameSchema/schema-owner mechanism at all until
// this change, so these two new record kinds close that gap. Mirrors the
// ALTER STATISTICS RENAME/OWNER pair (internal/wal/statistics_ddl.go) exactly:
// no per-schema page-level state exists, so physical WAL replay treats these
// as opaque bytes and a dedicated logical recovery pass
// (replaySchemaDDLRecords, internal/initdb/schema_ddl_recovery.go) applies
// them to the catalog after physical replay finishes.
const (
	// RecordKindAlterSchemaRename records an `ALTER SCHEMA name RENAME TO
	// newname` event. Format: kind(1) | nameLen(2) | name | newNameLen(2) |
	// newName.
	RecordKindAlterSchemaRename byte = 100

	// RecordKindAlterSchemaOwner records an `ALTER SCHEMA name OWNER TO ...`
	// event. Format: kind(1) | ownerOID(4) | nameLen(2) | name.
	RecordKindAlterSchemaOwner byte = 101
)

// EncodeAlterSchemaRename encodes an ALTER SCHEMA ... RENAME TO event. See
// RecordKindAlterSchemaRename for the wire format.
func EncodeAlterSchemaRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newName))
	out[0] = RecordKindAlterSchemaRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterSchemaRename decodes a RecordKindAlterSchemaRename payload.
func DecodeAlterSchemaRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: alter-schema-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterSchemaRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-schema-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-schema-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", fmt.Errorf("wal: alter-schema-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, newName, nil
}

// EncodeAlterSchemaOwner encodes an ALTER SCHEMA ... OWNER TO event. Format:
// kind(1) | ownerOID(4) | nameLen(2) | name.
func EncodeAlterSchemaOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterSchemaOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterSchemaOwner decodes a RecordKindAlterSchemaOwner payload.
func DecodeAlterSchemaOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-schema-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterSchemaOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-schema-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-schema-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	name = string(payload[7 : 7+nameLen])
	return name, ownerOID, nil
}
