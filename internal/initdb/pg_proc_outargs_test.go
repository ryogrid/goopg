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

// TestPgProcReplicationSRFOutArgsMatchPgProcDat pins the OUT-args metadata
// for the five SRFs backing the remaining replication views against
// postgres/src/include/catalog/pg_proc.dat. These rows land in base/1/1255
// via bootstrapPgProcTuples and must match PG18's proallargtypes/proargnames
// exactly so the standby's build_function_result_tupdesc_d resolves the
// view column types correctly. (batched-27)
//
// | OID  | Function                     | Cite (pg_proc.dat)       |
// |------|------------------------------|--------------------------|
// | 3099 | pg_stat_get_wal_senders      | :5659-5667               |
// | 6118 | pg_stat_get_subscription     | :5695-5702               |
// | 6169 | pg_stat_get_replication_slot | :5675-5681               |
// | 6248 | pg_stat_get_recovery_prefetch| :6027-6033               |
// | 3781 | pg_get_replication_slots     | :11464-11472             |
func TestPgProcReplicationSRFOutArgsMatchPgProcDat(t *testing.T) {
	type srfWant struct {
		oid      uint32
		name     string
		retSet   bool
		volatile byte
		parallel byte
		strict   bool
		argTypes []uint32 // pronargs IN args (proargtypes oidvector)
		allTypes []uint32 // proallargtypes
		modes    []byte   // proargmodes
		names    []string // proargnames
	}
	wants := []srfWant{
		{
			oid: 3099, name: "pg_stat_get_wal_senders",
			retSet: true, volatile: 's', parallel: 'r', strict: false,
			argTypes: []uint32{},
			allTypes: []uint32{23, 25, 3220, 3220, 3220, 3220, 1186, 1186, 1186, 23, 25, 1184},
			modes:    []byte{'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o'},
			names:    []string{"pid", "state", "sent_lsn", "write_lsn", "flush_lsn", "replay_lsn", "write_lag", "flush_lag", "replay_lag", "sync_priority", "sync_state", "reply_time"},
		},
		{
			oid: 6118, name: "pg_stat_get_subscription",
			retSet: true, volatile: 's', parallel: 'r', strict: false,
			argTypes: []uint32{26},
			allTypes: []uint32{26, 26, 26, 23, 23, 3220, 1184, 1184, 3220, 1184, 25},
			modes:    []byte{'i', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o'},
			names:    []string{"subid", "subid", "relid", "pid", "leader_pid", "received_lsn", "last_msg_send_time", "last_msg_receipt_time", "latest_end_lsn", "latest_end_time", "worker_type"},
		},
		{
			oid: 6169, name: "pg_stat_get_replication_slot",
			retSet: false, volatile: 's', parallel: 'r', strict: true,
			argTypes: []uint32{25},
			allTypes: []uint32{25, 25, 20, 20, 20, 20, 20, 20, 20, 20, 1184},
			modes:    []byte{'i', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o'},
			names:    []string{"slot_name", "slot_name", "spill_txns", "spill_count", "spill_bytes", "stream_txns", "stream_count", "stream_bytes", "total_txns", "total_bytes", "stats_reset"},
		},
		{
			oid: 6248, name: "pg_stat_get_recovery_prefetch",
			retSet: true, volatile: 'v', parallel: 's', strict: true,
			argTypes: []uint32{},
			allTypes: []uint32{1184, 20, 20, 20, 20, 20, 20, 23, 23, 23},
			modes:    []byte{'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o'},
			names:    []string{"stats_reset", "prefetch", "hit", "skip_init", "skip_new", "skip_fpw", "skip_rep", "wal_distance", "block_distance", "io_depth"},
		},
		{
			oid: 3781, name: "pg_get_replication_slots",
			retSet: true, volatile: 's', parallel: 's', strict: false,
			argTypes: []uint32{},
			allTypes: []uint32{19, 19, 25, 26, 16, 16, 23, 28, 28, 3220, 3220, 25, 20, 16, 3220, 1184, 16, 25, 16, 16},
			modes:    []byte{'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o', 'o'},
			names:    []string{"slot_name", "plugin", "slot_type", "datoid", "temporary", "active", "active_pid", "xmin", "catalog_xmin", "restart_lsn", "confirmed_flush_lsn", "wal_status", "safe_wal_size", "two_phase", "two_phase_at", "inactive_since", "conflicting", "invalidation_reason", "failover", "synced"},
		},
	}

	byOID := make(map[uint32]pgProcEntry)
	for _, e := range pgProcInitialEntries() {
		byOID[e.OID] = e
	}

	for _, w := range wants {
		t.Run(w.name, func(t *testing.T) {
			got, ok := byOID[w.oid]
			if !ok {
				t.Fatalf("OID %d (%s) missing from pgProcInitialEntries", w.oid, w.name)
			}
			if got.Name != w.name {
				t.Errorf("name=%q, want %q", got.Name, w.name)
			}
			if got.RetType != 2249 {
				t.Errorf("rettype=%d, want 2249 (record)", got.RetType)
			}
			if got.RetSet != w.retSet {
				t.Errorf("retset=%v, want %v", got.RetSet, w.retSet)
			}
			gotVol := got.Volatile
			if gotVol == 0 {
				gotVol = 'v'
			}
			if gotVol != w.volatile {
				t.Errorf("volatile=%q, want %q", gotVol, w.volatile)
			}
			gotPar := got.Parallel
			if gotPar == 0 {
				gotPar = 's'
			}
			if gotPar != w.parallel {
				t.Errorf("parallel=%q, want %q", gotPar, w.parallel)
			}
			// NotStrict=true means proisstrict=false (not strict); strict=false means same thing.
			// Mismatch when NotStrict and strict point the same direction.
			if got.NotStrict == w.strict {
				t.Errorf("notstrict=%v (strict=%v), want strict=%v", got.NotStrict, !got.NotStrict, w.strict)
			}
			// proargtypes (IN args only)
			if len(got.ArgTypes) != len(w.argTypes) {
				t.Errorf("ArgTypes len=%d, want %d", len(got.ArgTypes), len(w.argTypes))
			} else {
				for i, a := range w.argTypes {
					if got.ArgTypes[i] != a {
						t.Errorf("ArgTypes[%d]=%d, want %d", i, got.ArgTypes[i], a)
					}
				}
			}
			// proallargtypes
			if len(got.AllArgTypes) != len(w.allTypes) {
				t.Errorf("AllArgTypes len=%d, want %d", len(got.AllArgTypes), len(w.allTypes))
			} else {
				for i, a := range w.allTypes {
					if got.AllArgTypes[i] != a {
						t.Errorf("AllArgTypes[%d]=%d, want %d", i, got.AllArgTypes[i], a)
					}
				}
			}
			// proargmodes
			if len(got.ArgModes) != len(w.modes) {
				t.Errorf("ArgModes len=%d, want %d", len(got.ArgModes), len(w.modes))
			} else {
				for i, m := range w.modes {
					if got.ArgModes[i] != m {
						t.Errorf("ArgModes[%d]=%q, want %q", i, got.ArgModes[i], m)
					}
				}
			}
			// proargnames
			if len(got.ArgNames) != len(w.names) {
				t.Errorf("ArgNames len=%d, want %d", len(got.ArgNames), len(w.names))
			} else {
				for i, n := range w.names {
					if got.ArgNames[i] != n {
						t.Errorf("ArgNames[%d]=%q, want %q", i, got.ArgNames[i], n)
					}
				}
			}
		})
	}
}
