package postmaster

import (
	"encoding/binary"
	"testing"
)

// Wire-decode helpers shared by the extended-protocol and SET ROLE tests.
// They used to live in replication_test.go, which moved to
// internal/replication in the backup/replication split (and is now
// walsender_wire_test.go there); decodeDataRow has four
// consumers that stayed behind (prepare_execute_test.go,
// dispatch_extended_types_test.go, extended_set_role_test.go,
// set_local_role_test.go), so it lives here now instead.

// decodeDataRow parses a DataRow payload into per-column byte slices.
// nil indicates a NULL column. Format:
//
//	int16 ncolumns | { int32 length | bytes[length] | length=-1 means NULL } * ncolumns
func decodeDataRow(t *testing.T, payload []byte) [][]byte {
	t.Helper()
	if len(payload) < 2 {
		t.Fatalf("DataRow payload too short: %d", len(payload))
	}
	n := binary.BigEndian.Uint16(payload[:2])
	out := make([][]byte, n)
	off := 2
	for i := 0; i < int(n); i++ {
		if off+4 > len(payload) {
			t.Fatalf("DataRow truncated at column %d", i)
		}
		length := int32(binary.BigEndian.Uint32(payload[off : off+4]))
		off += 4
		if length == -1 {
			out[i] = nil
			continue
		}
		if off+int(length) > len(payload) {
			t.Fatalf("DataRow value at column %d truncated", i)
		}
		out[i] = make([]byte, length)
		copy(out[i], payload[off:off+int(length)])
		off += int(length)
	}
	return out
}
