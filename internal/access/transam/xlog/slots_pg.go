// PG-compatible binary encoding for replication slot state files.
//
// PostgreSQL stores each replication slot as a 200-byte binary file at
// pg_replslot/<name>/state (ReplicationSlotOnDisk, slot.c:SaveSlotToPath).
// goopg writes the same layout so a PG standby can read slots created by
// a goopg primary without modification.
//
// Layout of the 200-byte file (little-endian):
//
//	[0:4]   magic    uint32  (0x1051CA1)
//	[4:8]   checksum uint32  (CRC32C over bytes [8:200])
//	[8:12]  version  uint32  (5)
//	[12:16] length   uint32  (184 = sizeof ReplicationSlotPersistentData)
//	[16:200] slotdata (ReplicationSlotPersistentData)
//
// goopg appends a 64-byte NameData extension at offset 200 for the database
// name of logical slots; PG reads exactly 200 bytes and ignores the rest.
package xlog

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// PG replication slot binary file constants.
// Reference: postgres/src/backend/replication/slot.c
const (
	slotMagic   uint32 = 0x1051CA1 // SLOT_MAGIC
	slotVersion uint32 = 5         // SLOT_VERSION

	// slotOnDiskSize = sizeof(ReplicationSlotOnDisk)
	slotOnDiskSize = 200
	// slotChecksumFrom = offsetof(version) = ReplicationSlotOnDiskNotChecksummedSize
	slotChecksumFrom = 8
	// slotV2Size = sizeof(ReplicationSlotPersistentData) = ReplicationSlotOnDiskV2Size
	slotV2Size uint32 = 184

	// Offsets in the 200-byte image (verified via C offsetof probe).
	slotOffMagic    = 0
	slotOffChecksum = 4
	slotOffVersion  = 8
	slotOffLength   = 12
	slotOffData     = 16 // start of ReplicationSlotPersistentData

	// Absolute offsets within ReplicationSlotPersistentData.
	slotOffName          = slotOffData + 0   // NameData[64]
	slotOffDatabase      = slotOffData + 64  // Oid uint32
	slotOffPersistency   = slotOffData + 68  // int32 (RS_PERSISTENT=0)
	slotOffXmin          = slotOffData + 72  // uint32 (TransactionId)
	slotOffCatalogXmin   = slotOffData + 76  // uint32 (TransactionId)
	slotOffRestartLSN    = slotOffData + 80  // uint64 (XLogRecPtr)
	slotOffInvalidated   = slotOffData + 88  // int32 (RS_INVAL_NONE=0, RS_INVAL_WAL_REMOVED=1)
	// [slotOffData+92 : slotOffData+96] — 4 bytes alignment padding
	slotOffConfirmedFlush = slotOffData + 96  // uint64 (XLogRecPtr)
	slotOffTwoPhaseAt     = slotOffData + 104 // uint64 (XLogRecPtr)
	slotOffTwoPhase       = slotOffData + 112 // uint8 (bool)
	slotOffPlugin         = slotOffData + 113 // NameData[64]
	slotOffSynced         = slotOffData + 177 // char
	slotOffFailover       = slotOffData + 178 // uint8 (bool)
	// [slotOffData+179 : slotOffData+184] — 5 bytes trailing padding

	// goopg extension: 64-byte NameData for the database name appended after
	// the 200-byte PG struct. Physical slots omit this section entirely.
	slotExtOffset  = slotOnDiskSize // 200
	slotExtNameLen = 64
	slotExtSize    = slotExtNameLen // 64

	// RS_INVAL_WAL_REMOVED — the only invalidation cause goopg raises.
	rsInvalWALRemoved = 1
)

var pgSlotCRCTable = crc32.MakeTable(crc32.Castagnoli)

// marshalSlotBinary encodes a Slot into the PG-compatible binary state file.
// Physical slots produce exactly 200 bytes; logical slots with a Database name
// produce 264 bytes (200 + 64-byte goopg extension).
func marshalSlotBinary(slot *Slot) []byte {
	size := slotOnDiskSize
	if slot.Database != "" {
		size += slotExtSize
	}
	buf := make([]byte, size) // zero-initialised

	// Fixed header.
	binary.LittleEndian.PutUint32(buf[slotOffMagic:], slotMagic)
	// checksum filled below after slotdata is populated
	binary.LittleEndian.PutUint32(buf[slotOffVersion:], slotVersion)
	binary.LittleEndian.PutUint32(buf[slotOffLength:], slotV2Size)

	// slotdata fields.
	putNameData(buf[slotOffName:], slot.Name)
	binary.LittleEndian.PutUint32(buf[slotOffDatabase:], slot.DatabaseOID)
	// slotdata.persistency = RS_PERSISTENT = 0 (already zero)
	// slotdata.xmin = 0 (already zero)
	binary.LittleEndian.PutUint32(buf[slotOffCatalogXmin:], uint32(slot.CatalogXmin))
	binary.LittleEndian.PutUint64(buf[slotOffRestartLSN:], slot.RestartLSN)
	if slot.Invalidated {
		binary.LittleEndian.PutUint32(buf[slotOffInvalidated:], rsInvalWALRemoved)
	}
	binary.LittleEndian.PutUint64(buf[slotOffConfirmedFlush:], slot.ConfirmedFlushLSN)
	// slotdata.two_phase_at = 0, two_phase = false, synced = 0, failover = false (all zero)
	putNameData(buf[slotOffPlugin:], slot.Plugin)

	// CRC32C over the checksummed region [8:200].
	crc := crc32.Checksum(buf[slotChecksumFrom:slotOnDiskSize], pgSlotCRCTable)
	binary.LittleEndian.PutUint32(buf[slotOffChecksum:], crc)

	// goopg extension: database name for logical slots.
	if slot.Database != "" {
		putNameData(buf[slotExtOffset:], slot.Database)
	}

	return buf
}

// unmarshalSlotBinary decodes a PG binary slot state file.
// dirName is used as a fallback slot name if slotdata.name is empty.
func unmarshalSlotBinary(data []byte, dirName string) (*Slot, error) {
	if len(data) < slotOnDiskSize {
		return nil, fmt.Errorf("binary state too short (%d bytes, need %d)",
			len(data), slotOnDiskSize)
	}

	magic := binary.LittleEndian.Uint32(data[slotOffMagic:])
	if magic != slotMagic {
		return nil, fmt.Errorf("bad magic 0x%x (want 0x%x)", magic, slotMagic)
	}

	ver := binary.LittleEndian.Uint32(data[slotOffVersion:])
	if ver != slotVersion {
		return nil, fmt.Errorf("unsupported version %d (want %d)", ver, slotVersion)
	}

	length := binary.LittleEndian.Uint32(data[slotOffLength:])
	if length != slotV2Size {
		return nil, fmt.Errorf("unexpected length %d (want %d)", length, slotV2Size)
	}

	// Verify CRC32C over the checksummed region [8:200].
	stored := binary.LittleEndian.Uint32(data[slotOffChecksum:])
	computed := crc32.Checksum(data[slotChecksumFrom:slotOnDiskSize], pgSlotCRCTable)
	if stored != computed {
		return nil, fmt.Errorf("CRC mismatch stored=0x%08x computed=0x%08x",
			stored, computed)
	}

	name := getNameData(data[slotOffName : slotOffName+64])
	if name == "" {
		name = dirName
	}
	dbOID := binary.LittleEndian.Uint32(data[slotOffDatabase:])
	catalogXmin := binary.LittleEndian.Uint32(data[slotOffCatalogXmin:])
	restartLSN := binary.LittleEndian.Uint64(data[slotOffRestartLSN:])
	invalidatedVal := binary.LittleEndian.Uint32(data[slotOffInvalidated:])
	confirmedFlush := binary.LittleEndian.Uint64(data[slotOffConfirmedFlush:])
	plugin := getNameData(data[slotOffPlugin : slotOffPlugin+64])

	// Derive kind: logical slots always have a plugin name set.
	kind := SlotPhysical
	if plugin != "" {
		kind = SlotLogical
	}

	// Read goopg extension (database name) if present.
	var dbName string
	if len(data) >= slotOnDiskSize+slotExtSize {
		dbName = getNameData(data[slotExtOffset : slotExtOffset+slotExtNameLen])
	}

	return &Slot{
		Name:              name,
		Kind:              kind,
		RestartLSN:        restartLSN,
		ConfirmedFlushLSN: confirmedFlush,
		Invalidated:       invalidatedVal != 0,
		Plugin:            plugin,
		Database:          dbName,
		DatabaseOID:       dbOID,
		CatalogXmin:       uint64(catalogXmin),
	}, nil
}

// putNameData copies s into a 64-byte null-padded NameData region of dst.
// dst must have at least 64 bytes available; caller ensures this.
func putNameData(dst []byte, s string) {
	b := []byte(s)
	if len(b) >= 64 {
		b = b[:63] // leave room for null terminator
	}
	copy(dst[:64], b)
	// remaining bytes stay zero (buffer was zeroed by make)
}

// getNameData reads a null-terminated string from a 64-byte NameData slice.
func getNameData(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
