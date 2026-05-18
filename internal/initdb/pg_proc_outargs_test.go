package initdb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestOidArrayBytesShapeMatchesPGConstructArray pins the on-disk layout of
// oidArrayBytes against PG's construct_array output for an oid[] payload
// (24-byte header + N×4-byte little-endian OIDs; ndim=1, dataoffset=0,
// elemtype=26, lbound=1). Step 3dk pg_proc.proallargtypes consumer relies on
// this exact shape — drift here would crash PG's deconstruct_array on the
// next standby boot once the view-rewrite path resolves OID 3317's OUT-args.
func TestOidArrayBytesShapeMatchesPGConstructArray(t *testing.T) {
	got := oidArrayBytes([]uint32{23, 25, 3220})
	le := binary.LittleEndian
	if want := uint32((24 + 12) << 2); le.Uint32(got[0:4]) != want {
		t.Errorf("vl_len_: got %#x, want %#x", le.Uint32(got[0:4]), want)
	}
	if le.Uint32(got[4:8]) != 1 {
		t.Errorf("ndim: got %d, want 1", le.Uint32(got[4:8]))
	}
	if le.Uint32(got[8:12]) != 0 {
		t.Errorf("dataoffset: got %d, want 0", le.Uint32(got[8:12]))
	}
	if le.Uint32(got[12:16]) != 26 {
		t.Errorf("elemtype: got %d, want 26 (oid)", le.Uint32(got[12:16]))
	}
	if le.Uint32(got[16:20]) != 3 {
		t.Errorf("dim[0]: got %d, want 3", le.Uint32(got[16:20]))
	}
	if le.Uint32(got[20:24]) != 1 {
		t.Errorf("lbound[0]: got %d, want 1", le.Uint32(got[20:24]))
	}
	if le.Uint32(got[24:28]) != 23 {
		t.Errorf("oids[0]: got %d, want 23", le.Uint32(got[24:28]))
	}
	if le.Uint32(got[28:32]) != 25 {
		t.Errorf("oids[1]: got %d, want 25", le.Uint32(got[28:32]))
	}
	if le.Uint32(got[32:36]) != 3220 {
		t.Errorf("oids[2]: got %d, want 3220", le.Uint32(got[32:36]))
	}
	if len(got) != 36 {
		t.Errorf("total: got %d, want 36", len(got))
	}
}

// TestCharArrayBytesShapeMatchesPGConstructArray pins charArrayBytes (used
// for pg_proc.proargmodes) against PG's construct_array output for an
// 8-bit char[] payload (typalign='c', packed elements, no inter-element
// padding).
func TestCharArrayBytesShapeMatchesPGConstructArray(t *testing.T) {
	got := charArrayBytes([]byte{'o', 'i', 'b'})
	le := binary.LittleEndian
	if want := uint32((24 + 3) << 2); le.Uint32(got[0:4]) != want {
		t.Errorf("vl_len_: got %#x, want %#x", le.Uint32(got[0:4]), want)
	}
	if le.Uint32(got[12:16]) != 18 {
		t.Errorf("elemtype: got %d, want 18 (char)", le.Uint32(got[12:16]))
	}
	if le.Uint32(got[16:20]) != 3 {
		t.Errorf("dim[0]: got %d, want 3", le.Uint32(got[16:20]))
	}
	if le.Uint32(got[20:24]) != 1 {
		t.Errorf("lbound[0]: got %d, want 1", le.Uint32(got[20:24]))
	}
	if !bytes.Equal(got[24:27], []byte{'o', 'i', 'b'}) {
		t.Errorf("chars: got % x, want o i b", got[24:27])
	}
	if len(got) != 27 {
		t.Errorf("total: got %d, want 27", len(got))
	}
}

// TestTextArrayBytesShapeMatchesPGConstructArray pins textArrayBytes
// (proargnames consumer) against PG's array_seek walker for typalign='i'
// text elements: 24-byte header, then each element prefixed by a 4-byte
// SET_VARSIZE_4B header carrying (4+len(s))<<2, with 4-byte alignment
// between consecutive elements (header end → next element starts at
// (off+3) &^ 3).
func TestTextArrayBytesShapeMatchesPGConstructArray(t *testing.T) {
	// "a" (1 byte) + "bb" (2 bytes) + "ccc" (3 bytes).
	// Offsets: 24 → header+"a" = 5 bytes → next aligned to 28 (24+4).
	// 28 → header+"bb" = 6 bytes → 34 → next aligned to 36.
	// 36 → header+"ccc" = 7 bytes → 43. total = 43.
	got := textArrayBytes([]string{"a", "bb", "ccc"})
	le := binary.LittleEndian
	if le.Uint32(got[4:8]) != 1 {
		t.Errorf("ndim: got %d, want 1", le.Uint32(got[4:8]))
	}
	if le.Uint32(got[12:16]) != 25 {
		t.Errorf("elemtype: got %d, want 25 (text)", le.Uint32(got[12:16]))
	}
	if le.Uint32(got[16:20]) != 3 {
		t.Errorf("dim[0]: got %d, want 3", le.Uint32(got[16:20]))
	}
	if le.Uint32(got[20:24]) != 1 {
		t.Errorf("lbound[0]: got %d, want 1", le.Uint32(got[20:24]))
	}
	// Element 0 at offset 24: header (5<<2), payload "a".
	if le.Uint32(got[24:28]) != 5<<2 {
		t.Errorf("elem0 header: got %#x, want %#x", le.Uint32(got[24:28]), 5<<2)
	}
	if got[28] != 'a' {
		t.Errorf("elem0 payload: got %q, want 'a'", got[28])
	}
	// Element 1 at aligned offset 32: 24+8 (after padding 29→32).
	if le.Uint32(got[32:36]) != 6<<2 {
		t.Errorf("elem1 header: got %#x, want %#x", le.Uint32(got[32:36]), 6<<2)
	}
	if !bytes.Equal(got[36:38], []byte("bb")) {
		t.Errorf("elem1 payload: got %q, want 'bb'", got[36:38])
	}
	// Element 2 at aligned offset 40: 38+2 padded → 40.
	if le.Uint32(got[40:44]) != 7<<2 {
		t.Errorf("elem2 header: got %#x, want %#x", le.Uint32(got[40:44]), 7<<2)
	}
	if !bytes.Equal(got[44:47], []byte("ccc")) {
		t.Errorf("elem2 payload: got %q, want 'ccc'", got[44:47])
	}
	if len(got) != 47 {
		t.Errorf("total: got %d, want 47", len(got))
	}
	// Top-level varlena size: 47 bytes encoded as (47<<2).
	if want := uint32(47 << 2); le.Uint32(got[0:4]) != want {
		t.Errorf("vl_len_: got %#x, want %#x", le.Uint32(got[0:4]), want)
	}
}

// TestPgProcRowStatGetWalReceiverOutArgsMatchPgProcDat pins the OUT-args
// metadata on the OID 3317 pg_proc entry against
// `postgres/src/include/catalog/pg_proc.dat:5671-5673`. This is the metadata
// PG's build_function_result_tupdesc_d() consults to resolve `s.<col>`
// references in the future pg_stat_wal_receiver view rewrite rule (Step
// 3dl); without it the view query would fail to type-check.
func TestPgProcRowStatGetWalReceiverOutArgsMatchPgProcDat(t *testing.T) {
	var got pgProcEntry
	for _, e := range pgProcInitialEntries() {
		if e.OID == 3317 {
			got = e
			break
		}
	}
	if got.OID != 3317 {
		t.Fatalf("OID 3317 missing")
	}
	wantTypes := []uint32{
		23, 25, 3220, 23, 3220, 3220, 23, 1184,
		1184, 3220, 1184, 25, 25, 23, 25,
	}
	if len(got.AllArgTypes) != len(wantTypes) {
		t.Fatalf("AllArgTypes len: got %d, want %d", len(got.AllArgTypes), len(wantTypes))
	}
	for i, w := range wantTypes {
		if got.AllArgTypes[i] != w {
			t.Errorf("AllArgTypes[%d]: got %d, want %d", i, got.AllArgTypes[i], w)
		}
	}
	if len(got.ArgModes) != 15 {
		t.Fatalf("ArgModes len: got %d, want 15", len(got.ArgModes))
	}
	for i, m := range got.ArgModes {
		if m != 'o' {
			t.Errorf("ArgModes[%d]: got %q, want 'o'", i, m)
		}
	}
	wantNames := []string{
		"pid", "status", "receive_start_lsn",
		"receive_start_tli", "written_lsn", "flushed_lsn",
		"received_tli", "last_msg_send_time",
		"last_msg_receipt_time", "latest_end_lsn",
		"latest_end_time", "slot_name", "sender_host",
		"sender_port", "conninfo",
	}
	if len(got.ArgNames) != len(wantNames) {
		t.Fatalf("ArgNames len: got %d, want %d", len(got.ArgNames), len(wantNames))
	}
	for i, w := range wantNames {
		if got.ArgNames[i] != w {
			t.Errorf("ArgNames[%d]: got %q, want %q", i, got.ArgNames[i], w)
		}
	}
}
