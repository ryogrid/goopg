package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S23 — "the cheap tail": RM_LOGICALMSG (21), RM_REPLORIGIN (19),
// RM_GENERIC (20) and RM_COMMIT_TS (18). goopg emits none of them; a real PG
// can emit all four, and until S16 raised the decoder's structural bound the
// first one in a crash tail made every LATER record invisible.
//
// Three of the four are no-ops with a reason, so the guard that matters is
// symmetric: the DEFINED opcodes must be recognised (a start must not refuse),
// and an UNDEFINED opcode in the same rmgr must still error (the rmgr must not
// become a silent sink). RM_GENERIC is the one with real work.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S23.

// applyPGRecordApplied is applyPGRecordErr's twin for the no-op arms: it also
// returns the `applied` flag, which is the whole observable behaviour of a
// recognised record that touches no page.
func applyPGRecordApplied(t *testing.T, mgr *storage.Manager, framed []byte, endLSN uint64) (bool, error) {
	t.Helper()
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	return ApplyRecord(mgr, Record{EndLSN: endLSN, XLog: dec.XLog, Payload: dec.Payload})
}

func TestApplyRecordPGCheapTailNoOps(t *testing.T) {
	tests := []struct {
		name string
		rmid Rmgr
		info uint8
		body []byte
	}{
		// logicalmsg_redo (message.c:83-97) has an empty body. The payload is
		// an xl_logical_message; nothing in it reaches a page.
		{"logical message", RmgrLogicalMessage, xlogLogicalMessage, []byte("prefix\x00payload")},
		// replorigin_redo mutates only the in-shmem replication_states array.
		{"replorigin set", RmgrReplicationOrigin, xlogReplOriginSet, make([]byte, 16)},
		{"replorigin drop", RmgrReplicationOrigin, xlogReplOriginDrop, make([]byte, 4)},
		// commit_ts_redo's two opcodes maintain the pg_commit_ts SLRU extent;
		// with track_commit_timestamp off (the default, asserted by the
		// absence of a pg_control here) there is nothing to maintain.
		{"commit_ts zeropage", RmgrCommitTs, xlogCommitTsZeroPage, make([]byte, 8)},
		{"commit_ts truncate", RmgrCommitTs, xlogCommitTsTruncate, make([]byte, 12)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
			defer mgr.Close()

			body, err := assembleXLogRecord(tc.body, nil)
			if err != nil {
				t.Fatalf("assembleXLogRecord: %v", err)
			}
			applied, err := applyPGRecordApplied(t, mgr, framePGAssembled(tc.rmid, tc.info, 0, body), 400)
			if err != nil {
				t.Fatalf("ApplyRecord: %v, want a recognised no-op", err)
			}
			if applied {
				t.Fatalf("applied = true, want false — the record touches no page")
			}
		})
	}
}

// The other half of the contract: an opcode upstream does not define must still
// be refused, so recognising the rmgr does not turn it into a silent sink. Each
// value below is one step past the rmgr's last real opcode.
func TestApplyRecordPGCheapTailRefusesUnknownOpcode(t *testing.T) {
	tests := []struct {
		name string
		rmid Rmgr
		info uint8
	}{
		{"logical message", RmgrLogicalMessage, 0x10},
		{"replorigin", RmgrReplicationOrigin, 0x20},
		{"commit_ts", RmgrCommitTs, 0x20},
		// RM_GENERIC has no opcode space at all: XLogInsert(RM_GENERIC_ID, 0).
		{"generic", RmgrGeneric, 0x10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
			defer mgr.Close()

			body, err := assembleXLogRecord(make([]byte, 8), nil)
			if err != nil {
				t.Fatalf("assembleXLogRecord: %v", err)
			}
			err = applyPGRecordErr(t, mgr, framePGAssembled(tc.rmid, tc.info, 0, body), 400)
			if err == nil || !strings.Contains(err.Error(), "unsupported xlog record") {
				t.Fatalf("ApplyRecord err = %v, want an unsupported-record refusal", err)
			}
		})
	}
}

// With track_commit_timestamp ON in the crashed cluster's pg_control, skipping
// silently would leave pg_commit_ts segments that do not match the XID range
// the cluster believes is tracked. Refuse instead, naming the GUC.
func TestApplyRecordPGCommitTsRefusesWhenTracked(t *testing.T) {
	dir := t.TempDir()
	writeTrackedCommitTsControlFile(t, dir)

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	body, err := assembleXLogRecord(make([]byte, 8), nil)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrCommitTs, xlogCommitTsZeroPage, 0, body), 400)
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("ApplyRecord err = %v, want ErrUnsupportedRecord", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("track_commit_timestamp")) {
		t.Fatalf("error %q does not name the GUC an operator has to change", err)
	}
}

// writeTrackedCommitTsControlFile writes a minimal valid pg_control whose only
// non-zero field is track_commit_timestamp (offset 200), CRC32C over [0:292].
func writeTrackedCommitTsControlFile(t *testing.T, dir string) {
	t.Helper()
	const (
		pgControlFileSize  = 8192
		pgControlCRCOffset = 292
		trackCommitTSOff   = 200
	)
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, pgControlFileSize)
	buf[trackCommitTSOff] = 1
	crc := crc32.Checksum(buf[:pgControlCRCOffset], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(buf[pgControlCRCOffset:], crc)
	if err := os.WriteFile(filepath.Join(dir, "global", "pg_control"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildGenericPG assembles an RM_GENERIC_ID record: no main data, one block
// reference whose data run is the (offset, length, bytes) delta triples
// GenericXLogFinish produces (generic_xlog.c:341-400).
func buildGenericPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, chunks [][2]any) []byte {
	t.Helper()
	var delta []byte
	for _, c := range chunks {
		off := c[0].(int)
		payload := c[1].([]byte)
		hdr := make([]byte, 4)
		binary.LittleEndian.PutUint16(hdr[0:2], uint16(off))
		binary.LittleEndian.PutUint16(hdr[2:4], uint16(len(payload)))
		delta = append(delta, hdr...)
		delta = append(delta, payload...)
	}
	body, err := assembleXLogRecord(nil, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: delta}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrGeneric, xlogGenericInfo, 0, body)
}

// RM_GENERIC is the only missing rmgr implementable correctly with zero
// access-method knowledge: the record is an opaque byte diff. This asserts all
// three parts of generic_redo — the delta lands, the pd_lower..pd_upper hole is
// ZEROED afterwards (GenericXLogFinish diffs a page whose hole is already zero,
// so leaving stale bytes there produces a page that differs from the primary's
// byte for byte), and pd_lsn advances.
func TestApplyRecordReplaysPGGenericDelta(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 5501, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	// Dirty the free-space hole, the way a pre-image page legitimately can be:
	// the bytes there are whatever an earlier tuple left behind.
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	hdr := storage.MustHeader(page)
	lower, upper := int(hdr.Lower()), int(hdr.Upper())
	if upper-lower < 8 {
		t.Fatalf("seeded page has no usable hole (pd_lower=%d pd_upper=%d)", lower, upper)
	}
	for i := lower; i < upper; i++ {
		page[i] = 0xAB
	}
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	// A delta that rewrites two disjoint runs inside the tuple area, exactly
	// as applyPageRedo would.
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	framed := buildGenericPG(t, rel, 0, [][2]any{
		{upper, want[:2]},
		{upper + 2, want[2:]},
	})
	if err := applyPGRecordErr(t, mgr, framed, 700); err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[upper:upper+4], want) {
		t.Errorf("delta bytes = % x, want % x", got[upper:upper+4], want)
	}
	for i := lower; i < upper; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d of the pd_lower..pd_upper hole = 0x%02X, want 0 — "+
				"generic_redo zeroes the hole after applying the delta", i, got[i])
		}
	}
	if lsn := storage.MustHeader(got).LSN(); lsn != storage.LSN(700) {
		t.Errorf("pd_lsn = %d, want 700", lsn)
	}
}

// A generic record naming a block past the end of the fork is BLK_NOTFOUND
// upstream: skip, do not extend, do not error (the S21f contract, which this
// arm inherits by routing through the same RBM_NORMAL helper).
func TestApplyRecordPGGenericSkipsAbsentPage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 5502, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	framed := buildGenericPG(t, rel, 9, [][2]any{{100, []byte{0x01, 0x02}}})
	if err := applyPGRecordErr(t, mgr, framed, 700); err != nil {
		t.Fatalf("ApplyRecord on an absent block: %v, want a silent skip", err)
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if nblocks != 1 {
		t.Fatalf("nblocks = %d, want 1 — the fork must not have been extended", nblocks)
	}
}

// A delta chunk reaching past the end of the page is a corrupt record, not a
// page to smash: upstream trusts its own writer, goopg cannot (the record may
// come from any PG build).
func TestApplyRecordPGGenericRefusesOutOfBoundsDelta(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 5503, Fork: storage.MainFork}
	seedHeapTuplePG(t, mgr, rel, 0, 42, "row", 100)

	framed := buildGenericPG(t, rel, 0, [][2]any{{storage.BlockSize - 2, []byte{1, 2, 3, 4}}})
	err := applyPGRecordErr(t, mgr, framed, 700)
	if err == nil {
		t.Fatal("ApplyRecord accepted a delta chunk running past the end of the page")
	}
}
