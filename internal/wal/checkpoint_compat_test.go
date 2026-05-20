package wal

import (
	"encoding/binary"
	"testing"
)

// TestEncodeCheckpointCompatNextOidEmbedded verifies that EncodeCheckpointCompat
// embeds the nextOid parameter at the correct offset (32) within the 88-byte
// CheckPoint struct. M0106-0013: nextOid was previously hardcoded to 16384;
// it is now a caller-supplied parameter so checkpoint records carry the live
// OID counter.
func TestEncodeCheckpointCompatNextOidEmbedded(t *testing.T) {
	const (
		redoLSN uint64 = 0x01000028
		tli     uint32 = 1
		nextXid uint64 = 42
		nextOid uint32 = 99999 // well above FirstNormalObjectId=16384
	)
	payload := EncodeCheckpointCompat(redoLSN, tli, nextXid, nextOid)
	if len(payload) != 88 {
		t.Fatalf("payload length: got %d want 88", len(payload))
	}
	le := binary.LittleEndian

	// offset 32: nextOid (Oid = uint32)
	gotOid := le.Uint32(payload[32:36])
	if gotOid != nextOid {
		t.Errorf("nextOid at offset 32: got %d want %d", gotOid, nextOid)
	}

	// offset 24: nextXid (FullTransactionId = uint64) — unchanged
	gotXid := le.Uint64(payload[24:32])
	if gotXid != nextXid {
		t.Errorf("nextXid at offset 24: got %d want %d", gotXid, nextXid)
	}
}

// TestEncodeCheckpointCompatNextOidFloor verifies that EncodeCheckpointCompat
// clamps a zero nextOid to FirstNormalObjectId (16384) — callers that have
// not wired NextOIDFn will pass 0 and the record must still carry a valid
// bootstrap value rather than 0 (which would confuse pg_waldump and PG
// recovery on the attached standby).
func TestEncodeCheckpointCompatNextOidFloor(t *testing.T) {
	const firstNormalOID = uint32(16384)
	payload := EncodeCheckpointCompat(0x01000028, 1, 3, 0 /* zero → clamped */)
	le := binary.LittleEndian
	got := le.Uint32(payload[32:36])
	if got != firstNormalOID {
		t.Errorf("nextOid clamped: got %d want %d (FirstNormalObjectId)", got, firstNormalOID)
	}
}
