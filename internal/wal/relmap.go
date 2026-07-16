package wal

// B0.4 (docs/design/wal-pg-identical-stream/02a §5): the pg_filenode.map
// writer + XLOG_RELMAP_UPDATE record. The on-disk RelMapFile format is
// normative from postgres/src/backend/utils/cache/relmapper.c:73-95:
//
//	RelMapFile = { int32 magic = 0x592717; int32 num_mappings (<= 64);
//	               RelMapping mappings[64] { Oid mapoid; RelFileNumber mapfilenumber; };
//	               pg_crc32c crc }            -- 524 bytes, host-endian (LE here)
//
// The WAL record is xl_relmap_update{ Oid dbid; Oid tsid; int32 nbytes;
// char data[] } (relmapper.h:27-33, opcode XLOG_RELMAP_UPDATE = 0x00,
// RM_RELMAP_ID = 7) carrying the whole new file image; redo rewrites the
// target pg_filenode.map. Upstream's redo only length-checks and RECOMPUTES
// the CRC; goopg additionally verifies the image CRC as local hardening.

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
)

const (
	// RelMapFileSize is sizeof(RelMapFile): 4 + 4 + 64*8 + 4.
	RelMapFileSize = 524
	relMapMagic    = 0x592717 // RELMAPPER_FILEMAGIC
	relMapMaxMaps  = 64
	relMapCRCOff   = 520

	// xlogRelmapUpdate is XLOG_RELMAP_UPDATE (utils/relmapper.h).
	xlogRelmapUpdate uint8 = 0x00
	// minSizeOfRelmapUpdate is offsetof(xl_relmap_update, data):
	// dbid(4) + tsid(4) + nbytes(4).
	minSizeOfRelmapUpdate = 12
)

// RelMapping is one catalog OID → relfilenumber entry.
type RelMapping struct {
	Oid        uint32
	FileNumber uint32
}

// EncodeRelMapFile renders the 524-byte pg_filenode.map image for mappings
// (order preserved; PG's file is unordered). Panics if more than 64
// mappings are supplied — the format is fixed-size.
func EncodeRelMapFile(mappings []RelMapping) []byte {
	if len(mappings) > relMapMaxMaps {
		panic(fmt.Sprintf("wal: relmap: %d mappings > %d", len(mappings), relMapMaxMaps))
	}
	out := make([]byte, RelMapFileSize)
	le := binary.LittleEndian
	le.PutUint32(out[0:4], relMapMagic)
	le.PutUint32(out[4:8], uint32(len(mappings)))
	for i, m := range mappings {
		off := 8 + i*8
		le.PutUint32(out[off:off+4], m.Oid)
		le.PutUint32(out[off+4:off+8], m.FileNumber)
	}
	crc := crc32.Checksum(out[:relMapCRCOff], xlogCRC32CTable)
	le.PutUint32(out[relMapCRCOff:], crc)
	return out
}

// ValidateRelMapFile checks a pg_filenode.map image's size, magic,
// mapping count, and CRC. Returns the decoded mappings.
func ValidateRelMapFile(image []byte) ([]RelMapping, error) {
	if len(image) != RelMapFileSize {
		return nil, fmt.Errorf("wal: relmap image is %d bytes, want %d", len(image), RelMapFileSize)
	}
	le := binary.LittleEndian
	if got := le.Uint32(image[0:4]); got != relMapMagic {
		return nil, fmt.Errorf("wal: relmap magic %#x, want %#x", got, uint32(relMapMagic))
	}
	n := le.Uint32(image[4:8])
	if n > relMapMaxMaps {
		return nil, fmt.Errorf("wal: relmap num_mappings %d > %d", n, relMapMaxMaps)
	}
	wantCRC := le.Uint32(image[relMapCRCOff:])
	if got := crc32.Checksum(image[:relMapCRCOff], xlogCRC32CTable); got != wantCRC {
		return nil, fmt.Errorf("wal: relmap CRC mismatch: got %#x, want %#x", got, wantCRC)
	}
	out := make([]RelMapping, n)
	for i := range out {
		off := 8 + i*8
		out[i] = RelMapping{Oid: le.Uint32(image[off : off+4]), FileNumber: le.Uint32(image[off+4 : off+8])}
	}
	return out, nil
}

// EncodeRelmapUpdatePG builds a PostgreSQL RM_RELMAP XLOG_RELMAP_UPDATE
// record carrying the full new pg_filenode.map image for (dbid, tsid).
// dbid = 0 targets the shared map in global/. Framed for the
// assembled-record Append path; xl_xid = 0 (upstream logs relmap updates
// inside the owning xact, but goopg's emitter — CREATE DATABASE — is
// non-transactional file scaffolding).
func EncodeRelmapUpdatePG(dbid, tsid uint32, image []byte) ([]byte, error) {
	if _, err := ValidateRelMapFile(image); err != nil {
		return nil, err
	}
	mainData := make([]byte, minSizeOfRelmapUpdate+len(image))
	le := binary.LittleEndian
	le.PutUint32(mainData[0:4], dbid)
	le.PutUint32(mainData[4:8], tsid)
	le.PutUint32(mainData[8:12], uint32(len(image)))
	copy(mainData[minSizeOfRelmapUpdate:], image)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrRelMap, xlogRelmapUpdate, 0, body), nil
}

// replayDecodedXLogRelmapUpdate applies XLOG_RELMAP_UPDATE: validate the
// image (length + magic + CRC — the goopg hardening on top of upstream's
// length-only check) and atomically rewrite the target pg_filenode.map
// (write temp + fsync + rename, matching write_relmap_file). Idempotent by
// construction (whole-file replace).
func replayDecodedXLogRelmapUpdate(dataDir string, mainData []byte) error {
	if len(mainData) < minSizeOfRelmapUpdate {
		return fmt.Errorf("wal: xl_relmap_update main-data %d bytes < %d", len(mainData), minSizeOfRelmapUpdate)
	}
	le := binary.LittleEndian
	dbid := le.Uint32(mainData[0:4])
	nbytes := int(le.Uint32(mainData[8:12]))
	if nbytes != RelMapFileSize || minSizeOfRelmapUpdate+nbytes > len(mainData) {
		return fmt.Errorf("wal: xl_relmap_update nbytes %d, want %d", nbytes, RelMapFileSize)
	}
	image := mainData[minSizeOfRelmapUpdate : minSizeOfRelmapUpdate+nbytes]
	if _, err := ValidateRelMapFile(image); err != nil {
		return err
	}
	var dir string
	if dbid == 0 {
		dir = filepath.Join(dataDir, "global")
	} else {
		dir = filepath.Join(dataDir, "base", strconv.FormatUint(uint64(dbid), 10))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(dir, "pg_filenode.map")
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(image); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
