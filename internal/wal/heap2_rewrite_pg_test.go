package wal

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2 part 7: XLOG_HEAP2_REWRITE's loud refusal ------------------
//
// The one RM_HEAP2 opcode S21a-2 deliberately does NOT implement. A VACUUM FULL
// or CLUSTER of a table whose pre-rewrite row versions a logical replication
// slot may still need emits it (rewriteheap.c:894, reached only via
// logical_rewrite_log_mapping — wal_level=logical plus a slot), and its redo
// writes no relation page: it truncates a pg_logical/mappings/ file to the
// record's offset, rewrites the mapping tail, and fsyncs
// (heap_xlog_logical_rewrite, rewriteheap.c:1073-1160).
//
// goopg has no pg_logical/mappings consumer, so both alternatives to refusing
// are dishonest: replaying it maintains a file nothing reads, and no-opping it
// leaves a slot on the resulting cluster decoding the rewritten table against
// mappings that stop mid-rewrite, with nothing reporting an error. These guards
// pin the refusal AND its wording, because the wording is the whole point — an
// operator has to learn which feature the cluster used, not just that recovery
// failed.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2.

// buildRewriteMappingPG assembles a real xl_heap_rewrite_mapping record.
//
// The struct is {TransactionId mapped_xid; Oid mapped_db; Oid mapped_rel;
// off_t offset; uint32 num_mappings; XLogRecPtr start_lsn} — 40 bytes with C
// alignment (off_t and XLogRecPtr are 8-byte aligned, so there is 4 bytes of
// padding before each). Upstream appends num_mappings
// LogicalRewriteMappingData entries as further main data; the mapping payload
// is irrelevant here because the record is refused before anything reads it,
// so this builds the header alone.
func buildRewriteMappingPG(t *testing.T, xid uint32, db, rel uint32, offset uint64, startLSN uint64) []byte {
	t.Helper()
	mainData := make([]byte, 40)
	binary.LittleEndian.PutUint32(mainData[0:4], xid)
	binary.LittleEndian.PutUint32(mainData[4:8], db)
	binary.LittleEndian.PutUint32(mainData[8:12], rel)
	binary.LittleEndian.PutUint64(mainData[16:24], offset)
	binary.LittleEndian.PutUint32(mainData[24:28], 0) // num_mappings
	binary.LittleEndian.PutUint64(mainData[32:40], startLSN)

	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrHeap2, xlogHeap2Rewrite, xid, body)
}

// TestReplayRefusesHeap2Rewrite is the dispatch-level guard: rmid 9 / info 0x00
// must be refused, classified as ErrUnsupportedRecord (so the reader does not
// mistake it for end-of-WAL and append over the records behind it — format.go,
// M0131-S16.2), and its message must name the feature that produced it.
func TestReplayRefusesHeap2Rewrite(t *testing.T) {
	rec := Record{XLog: &XLogDecodedRecord{
		Header: XLogRecord{Rmid: RmgrHeap2, Info: xlogHeap2Rewrite},
	}}
	applied, err := replayDecodedXLogRecord(nil, rec)
	if err == nil {
		t.Fatalf("replay err = nil (applied=%v), want refusal", applied)
	}
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("replay err = %v, want ErrUnsupportedRecord", err)
	}
	if applied {
		t.Fatal("applied = true alongside error")
	}
	// The wording is load-bearing: it is the only place the operator is told
	// WHY the cluster cannot be started.
	for _, want := range []string{"logical", "VACUUM FULL", "CLUSTER"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal message %q does not mention %q", err.Error(), want)
		}
	}
}

// TestReplayRefusesHeap2RewriteFramedRecord drives a real-shaped record through
// the same encode→decode→ApplyRecord path the other PG-format guards use. The
// point is that the refusal is what a genuine PG rewrite record hits — not a
// decode error raised earlier by the 40-byte main-data-only, block-reference-free
// body, which would make the dispatch arm above unreachable in practice.
func TestReplayRefusesHeap2RewriteFramedRecord(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	framed := buildRewriteMappingPG(t, 4242, 5, 16400, 8192, 0x0100000000)
	err := applyPGRecordErr(t, mgr, framed, 900)
	if err == nil {
		t.Fatal("replay accepted an XLOG_HEAP2_REWRITE record")
	}
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("replay err = %v, want ErrUnsupportedRecord", err)
	}
	if !strings.Contains(err.Error(), "rmid=9") {
		t.Fatalf("refusal message %q does not identify the record (rmid=9)", err.Error())
	}
}

// TestReplayHeap2RewriteRefusalDoesNotSwallowSiblings is the counterweight: the
// new 0x00 arm must not have been written in a way that captures the rest of
// RM_HEAP2's opcode space. XLOG_HEAP2_NEW_CID (0x70) is the nearest neighbour
// in kind — also logical-decoding-only — and it is a RECOGNISED no-op, not a
// refusal (heapam_xlog.c:1244-1252). If the two ever collapse into one arm, a
// wal_level=logical PG tail stops starting on goopg for a record that has no
// physical effect at all.
func TestReplayHeap2RewriteRefusalDoesNotSwallowSiblings(t *testing.T) {
	rec := Record{XLog: &XLogDecodedRecord{
		Header: XLogRecord{Rmid: RmgrHeap2, Info: xlogHeap2NewCid},
	}}
	applied, err := replayDecodedXLogRecord(nil, rec)
	if err != nil {
		t.Fatalf("XLOG_HEAP2_NEW_CID replay err = %v, want nil (recognised no-op)", err)
	}
	if applied {
		t.Fatal("applied = true, want false (logical-decoding-only record)")
	}
}
