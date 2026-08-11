package wal

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S16: an unrecognised WAL record is NOT end-of-WAL.
//
// Before this slice `readAllPageAware` collapsed two situations into one
// outcome — a torn/zero/CRC-failed tail (bytes that were never durable) and a
// decodable-but-unhandled record (bytes that ARE durable and DO mean
// something) — and both stopped the walk, logged a slog.Warn, and reported
// success. Every record behind the second case was dropped, `ReplayRecords`
// reported SUCCESS, and `detectWritePos` then appended over the survivors,
// making the loss permanent on the first write. A single
// `pg_logical_emit_message` (rmid 21) or one `commit_ts` record (rmid 18) in a
// PG crash tail was enough.
//
// Design: docs/design/0131-0013-wal-reader-fail-closed.md.

// encodeTestPGRecordRmid builds a minimal but structurally valid PG-frame
// record — a short main-data chunk and no block references — under an
// arbitrary resource manager. Its CRC is computed by the real encoder, so the
// bytes are indistinguishable from a record PostgreSQL itself wrote.
func encodeTestPGRecordRmid(t *testing.T, rmid Rmgr, info uint8, main []byte) []byte {
	t.Helper()
	fragments := make([]byte, 0, 2+len(main))
	fragments = append(fragments, xlrBlockIDDataShort, byte(len(main)))
	fragments = append(fragments, main...)

	rec := make([]byte, maxAlignXLog(SizeOfXLogRecord+len(fragments)))
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(fragments)),
		XID:    77,
		Rmid:   rmid,
		Info:   info,
	}
	if err := EncodeXLogRecordHeader(rec[:SizeOfXLogRecord], h, fragments); err != nil {
		t.Fatal(err)
	}
	copy(rec[SizeOfXLogRecord:], fragments)
	return rec
}

// TestReadAllPageAwareKeepsRecordsAfterPGOnlyRmgr is the S16.1 guard: rmids
// 16..21 are real PG 18 resource managers (rmgrlist.h:28-49) that goopg never
// emits but a PG-authored pg_wal routinely contains. They must decode — and
// be refused later by the replay dispatcher — rather than fail header
// validation, which the reader reads as end-of-WAL.
//
// Pre-fix behaviour on this exact stream: 1 record, no error.
func TestReadAllPageAwareKeepsRecordsAfterPGOnlyRmgr(t *testing.T) {
	for _, rmid := range []Rmgr{RmgrSPGist, RmgrBrin, RmgrCommitTs, RmgrReplicationOrigin, RmgrGeneric, RmgrLogicalMessage} {
		first, _, _ := encodeTestPGHeapInsertRecord(t)
		last, _, _ := encodeTestPGHeapInsertRecord(t)
		middle := encodeTestPGRecordRmid(t, rmid, 0x00, []byte{1, 2, 3, 4})

		stream := buildTestLongPageHeader(t)
		stream = append(stream, first...)
		stream = append(stream, middle...)
		stream = append(stream, last...)

		records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
		if err != nil {
			t.Fatalf("rmid=%d: readAllPageAware err = %v, want nil", rmid, err)
		}
		if len(records) != 3 {
			t.Fatalf("rmid=%d: records = %d, want 3 (a PG-only rmgr is not end-of-WAL)", rmid, len(records))
		}
		if got := records[1].XLog.Header.Rmid; got != rmid {
			t.Fatalf("records[1].Rmid = %d, want %d", got, rmid)
		}
	}
}

// TestReplayRefusesPGOnlyRmgr is the other half of S16.1: decoding such a
// record must not mean applying it. The replay dispatcher's default arm
// refuses, and that error already rides ReplayRecords → initdb.Open → a
// non-zero exit (design §"The caller chain, end to end"). Fail-closed: goopg
// says "I cannot replay this", never "replayed, all good".
func TestReplayRefusesPGOnlyRmgr(t *testing.T) {
	for _, rmid := range []Rmgr{RmgrSPGist, RmgrBrin, RmgrCommitTs, RmgrReplicationOrigin, RmgrGeneric, RmgrLogicalMessage} {
		rec := encodeTestPGRecordRmid(t, rmid, 0x00, []byte{9})
		decoded, err := decodeRecordXLogDetailed(rec)
		if err != nil {
			t.Fatalf("rmid=%d: decode err = %v, want nil", rmid, err)
		}
		// The default arm refuses before touching storage, so a nil manager
		// is safe here and keeps the guard a pure unit test.
		applied, rerr := replayDecodedXLogRecord(nil, Record{XLog: decoded.XLog})
		if rerr == nil {
			t.Fatalf("rmid=%d: replay err = nil (applied=%v), want refusal", rmid, applied)
		}
		if applied {
			t.Fatalf("rmid=%d: applied = true alongside error", rmid)
		}
	}
}

// TestReadAllPageAwareErrorsOnCRCValidUnknownRmid covers the residual
// header-decode branch after S16.1: rmids in (RM_MAX_BUILTIN_ID, 128) are a
// genuine "emitted by a newer producer" signal. goopg cannot decode them, but
// a valid xl_crc proves the bytes were durable, so swallowing them as a tail
// would be data loss. S16.2 requires the stop reason to reach the caller.
func TestReadAllPageAwareErrorsOnCRCValidUnknownRmid(t *testing.T) {
	first, _, _ := encodeTestPGHeapInsertRecord(t)
	middle := encodeTestPGRecordRmid(t, 99, 0x00, []byte{1, 2, 3, 4})
	last, _, _ := encodeTestPGHeapInsertRecord(t)

	stream := buildTestLongPageHeader(t)
	stream = append(stream, first...)
	stream = append(stream, middle...)
	stream = append(stream, last...)

	records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("readAllPageAware err = %v, want ErrUnsupportedRecord", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want the 1 record decoded before the refusal", len(records))
	}
}

// TestReadAllPageAwareTornTailIsStillEndOfWAL is the counterweight regression
// guard for S16.2: the fail-closed change must NOT turn goopg's own crash tail
// into a refused start. A record whose bytes were half-written by a SIGKILL
// fails its CRC, was never durable, and remains end-of-WAL — replay keeps
// everything before it and returns no error.
func TestReadAllPageAwareTornTailIsStillEndOfWAL(t *testing.T) {
	first, _, _ := encodeTestPGHeapInsertRecord(t)
	torn := encodeTestPGRecordRmid(t, RmgrHeap, xlogHeapInsert, []byte{1, 2, 3, 4})
	// Corrupt the header the way a partially-flushed record does: plant
	// nonzero padding (header validation fails) and scramble a payload byte
	// so the CRC cannot rescue it either.
	torn[18] = 0xFF
	torn[SizeOfXLogRecord] ^= 0xFF

	stream := buildTestLongPageHeader(t)
	stream = append(stream, first...)
	stream = append(stream, torn...)

	records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatalf("readAllPageAware err = %v, want nil (torn tail is end-of-WAL)", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

// TestReadAllPageAwareZeroTailIsStillEndOfWAL: the preallocated zero-fill tail
// of a segment must keep terminating the walk silently — S16.2 deleted the
// `<= segSize` guards, not the isPreallocatedTail ones.
func TestReadAllPageAwareZeroTailIsStillEndOfWAL(t *testing.T) {
	first, _, _ := encodeTestPGHeapInsertRecord(t)
	stream := buildTestLongPageHeader(t)
	stream = append(stream, first...)
	stream = append(stream, make([]byte, 4096)...)

	records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatalf("readAllPageAware err = %v, want nil", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

// TestReadAllPageAwareErrorsOnCompressedImage is the S16.5 guard.
// pg_xlog_decode.go already refused compressed backup-block images, but the
// error was absorbed by the `<= segSize` break and reported as a clean
// end-of-WAL — the worst possible outcome, since wal_compression is on by
// default in plenty of real clusters and every record behind the first
// compressed image was dropped. It must now reach the caller.
func TestReadAllPageAwareErrorsOnCompressedImage(t *testing.T) {
	first, _, _ := encodeTestPGHeapInsertRecord(t)
	compressed := encodeTestPGCompressedImageRecord(t)

	stream := buildTestLongPageHeader(t)
	stream = append(stream, first...)
	stream = append(stream, compressed...)

	records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("readAllPageAware err = %v, want ErrUnsupportedRecord", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

// TestDecodeBlockRefCarriesUserTablespace guards the latent bug S16 exposed:
// goopg emits block references for pg_tblspc-resident relations with the real
// tablespace OID in the locator (xlog_assemble.go:130 writes b.Rel.TblOid), but
// the decoder rejected any OID that was not 1663/1664 — and the reader then
// reported that rejection as a clean end-of-WAL, dropping every record behind
// the first user-tablespace relation on goopg's OWN restart path. The OID must
// come back as TblOid so storage.relDir routes to
// pg_tblspc/<oid>/<version>/<db> (smgr.go:624-636), mirroring
// decodeXLogSmgrCreate's identical mapping for xl_smgr_create.
func TestDecodeBlockRefCarriesUserTablespace(t *testing.T) {
	for _, tc := range []struct {
		spcOID uint32
		want   uint32
	}{
		{pgDefaultTableSpaceOID, 0},
		{pgGlobalTableSpaceOID, 0},
		{16407, 16407},
	} {
		rec := encodeTestPGBlockRefRecord(t, tc.spcOID)
		decoded, err := decodeRecordXLogDetailed(rec)
		if err != nil {
			t.Fatalf("spcOid=%d: decode err = %v", tc.spcOID, err)
		}
		if len(decoded.XLog.Blocks) != 1 {
			t.Fatalf("spcOid=%d: blocks = %d, want 1", tc.spcOID, len(decoded.XLog.Blocks))
		}
		if got := decoded.XLog.Blocks[0].Rel.TblOid; got != tc.want {
			t.Fatalf("spcOid=%d: TblOid = %d, want %d", tc.spcOID, got, tc.want)
		}
	}
}

// encodeTestPGBlockRefRecord builds a data-less block reference under the given
// tablespace OID.
func encodeTestPGBlockRefRecord(t *testing.T, spcOID uint32) []byte {
	t.Helper()
	fragments := make([]byte, 0, 32)
	fragments = append(fragments, 0)                      // block id 0
	fragments = append(fragments, byte(storage.MainFork)) // fork_flags, no data/image
	fragments = append(fragments, 0, 0)                   // data_length = 0
	var relLocator [sizeOfRelFileLocator]byte
	binary.LittleEndian.PutUint32(relLocator[0:4], spcOID)
	binary.LittleEndian.PutUint32(relLocator[4:8], 5)
	binary.LittleEndian.PutUint32(relLocator[8:12], 16408)
	fragments = append(fragments, relLocator[:]...)
	var blkNo [4]byte
	binary.LittleEndian.PutUint32(blkNo[:], 3)
	fragments = append(fragments, blkNo[:]...)

	rec := make([]byte, maxAlignXLog(SizeOfXLogRecord+len(fragments)))
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(fragments)),
		XID:    79,
		Rmid:   RmgrHeap,
		Info:   xlogHeapInsert,
	}
	if err := EncodeXLogRecordHeader(rec[:SizeOfXLogRecord], h, fragments); err != nil {
		t.Fatal(err)
	}
	copy(rec[SizeOfXLogRecord:], fragments)
	return rec
}

// encodeTestPGCompressedImageRecord builds a record carrying one block
// reference whose backup image is flagged compressed (BKPIMAGE_COMPRESS_*).
// The decoder rejects it at the block-image header, before any payload is
// consumed, so the image body is a placeholder.
func encodeTestPGCompressedImageRecord(t *testing.T) []byte {
	t.Helper()
	const imgLen = 16
	fragments := make([]byte, 0, 64)
	fragments = append(fragments, 0)                                                  // block id 0
	fragments = append(fragments, bkpBlockHasImage|byte(storage.MainFork))            // fork_flags
	fragments = append(fragments, 0, 0)                                               // data_length = 0
	var img [sizeOfXLogRecordBlockImageHeader]byte                                    //
	binary.LittleEndian.PutUint16(img[0:2], imgLen)                                   // length
	binary.LittleEndian.PutUint16(img[2:4], 0)                                        // hole offset
	img[4] = bkpImageApply | 0x04                                                     // apply + one BKPIMAGE_COMPRESS_* method bit (mask bkpImageCompressMS)
	fragments = append(fragments, img[:]...)                                          //
	fragments = append(fragments, make([]byte, sizeOfXLogRecordBlockCompressHead)...) // hole length
	var relLocator [sizeOfRelFileLocator]byte
	binary.LittleEndian.PutUint32(relLocator[0:4], pgDefaultTableSpaceOID)
	binary.LittleEndian.PutUint32(relLocator[4:8], 123)
	binary.LittleEndian.PutUint32(relLocator[8:12], 456)
	fragments = append(fragments, relLocator[:]...)
	var blkNo [4]byte
	binary.LittleEndian.PutUint32(blkNo[:], 7)
	fragments = append(fragments, blkNo[:]...)
	fragments = append(fragments, make([]byte, imgLen)...) // image payload placeholder

	rec := make([]byte, maxAlignXLog(SizeOfXLogRecord+len(fragments)))
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(fragments)),
		XID:    78,
		Rmid:   RmgrHeap,
		Info:   xlogHeapInsert,
	}
	if err := EncodeXLogRecordHeader(rec[:SizeOfXLogRecord], h, fragments); err != nil {
		t.Fatal(err)
	}
	copy(rec[SizeOfXLogRecord:], fragments)
	return rec
}

// --- M0131-S16.3 / S16.4: the REPLAY-side half of "fail closed" -------------
//
// S16.1/S16.2 stopped the reader from silently truncating the stream. These
// two guards stop the *dispatcher* from silently under-applying a record it
// did hand to redo. Both failure modes reported applied=true, which is worse
// than an error: the caller has no way to tell a real replay from a skipped
// one.

// btreeRecordWithBlocks builds a decoded (not encoded) btree record under an
// opcode goopg has no native redo for, with one block reference per entry in
// withImage. Working at the decoded level keeps the guard focused on the
// dispatch decision rather than on frame encoding, which S16.1's guards
// already cover.
func btreeRecordWithBlocks(info uint8, withImage ...bool) Record {
	blocks := make([]XLogBlockRef, 0, len(withImage))
	for i, img := range withImage {
		blocks = append(blocks, XLogBlockRef{
			ID:         byte(i),
			Rel:        storage.RelFileNode{DBOid: 5, RelOid: uint32(16400 + i)},
			Block:      storage.BlockNumber(i),
			HasImage:   img,
			ImageApply: img,
			Data:       []byte{1, 2, 3},
		})
	}
	return Record{XLog: &XLogDecodedRecord{
		Header: XLogRecord{Rmid: RmgrBtree, Info: info},
		Blocks: blocks,
	}}
}

// TestReplayRefusesBtreeFallbackWithoutFullPageImages is the S16.3 guard.
//
// xlogBtreeDedup (0xD0) is a real PG opcode with no named arm in goopg, so it
// lands on the btree `default:` arm, whose only replay strategy is restoring
// every mutated page from its FPI. PG emits an FPI only on a page's FIRST
// touch after a checkpoint — so the second dedup on a page carries block DATA
// and no image. Before S16.3 that block was `continue`d past and the record
// reported applied=true: an index mutation silently dropped on a PG crash
// tail. Every block must carry an applicable image or the record is refused.
func TestReplayRefusesBtreeFallbackWithoutFullPageImages(t *testing.T) {
	const xlogBtreeDedup = 0xD0 // XLOG_BTREE_DEDUP (nbtxlog.h)

	cases := []struct {
		name   string
		blocks []bool
	}{
		{"no blocks at all", nil},
		{"single block without image", []bool{false}},
		{"second of two blocks without image", []bool{true, false}},
		{"first of two blocks without image", []bool{false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := btreeRecordWithBlocks(xlogBtreeDedup, tc.blocks...)
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
		})
	}

	// The counterweight: an all-image record still satisfies the
	// precondition, so S16.3 does not refuse the case the fallback exists
	// for. (The apply itself needs a storage manager and is covered by the
	// btree FPI replay tests.)
	rec := btreeRecordWithBlocks(xlogBtreeDedup, true, true)
	if err := requireFullPageImages(rec, rec.XLog); err != nil {
		t.Fatalf("all-image record refused: %v", err)
	}
}

// TestReplayRmgrXLogOpcodeCoverage is the S16.4 guard: RM_XLOG_ID's opcode
// space is enumerated, not collapsed into one silent no-op.
//
// The dangerous member is XLOG_NEXTOID. Upstream xlog_redo sets nextOid
// EXACTLY from it (xlog.c:8292-8308); dropping one lets goopg re-issue OIDs a
// crashed PG had already allocated after the last checkpoint. Before S16.4 it
// was a no-op indistinguishable from XLOG_SWITCH.
func TestReplayRmgrXLogOpcodeCoverage(t *testing.T) {
	benign := []struct {
		name string
		info uint8
	}{
		{"XLOG_CHECKPOINT_SHUTDOWN", xlogXLogCheckpointShutdown},
		{"XLOG_CHECKPOINT_ONLINE", xlogXLogCheckpointOnline},
		{"XLOG_NOOP", xlogXLogNoop},
		{"XLOG_SWITCH", xlogXLogSwitch},
		{"XLOG_BACKUP_END", xlogXLogBackupEnd},
		{"XLOG_RESTORE_POINT", xlogXLogRestorePoint},
		{"XLOG_FPW_CHANGE", xlogXLogFPWChange},
		{"XLOG_END_OF_RECOVERY", xlogXLogEndOfRecovery},
		{"XLOG_OVERWRITE_CONTRECORD", xlogXLogOverwriteContrecord},
		{"XLOG_CHECKPOINT_REDO", xlogXLogCheckpointRedo},
		// Not a PG opcode: goopg's own empty-payload marker
		// (classifyXLogRecord, format.go:151-153). Refusing it would make
		// goopg refuse its own WAL — the exact self-inflicted hazard this
		// slice was flagged for. PG defines nothing at 0xF0, so keeping it
		// benign costs no real-PG coverage.
		{"goopg xlogInfoDefault (empty payload)", xlogInfoDefault},
	}
	for _, tc := range benign {
		t.Run(tc.name, func(t *testing.T) {
			rec := Record{XLog: &XLogDecodedRecord{
				Header: XLogRecord{Rmid: RmgrXLog, Info: tc.info},
			}}
			applied, err := replayDecodedXLogRecord(nil, rec)
			if err != nil {
				t.Fatalf("replay err = %v, want nil (enumerated benign no-op)", err)
			}
			if applied {
				t.Fatal("applied = true, want false (physical no-op)")
			}
		})
	}

	refused := []struct {
		name string
		info uint8
	}{
		// The whole point of S16.4: NEXTOID is not benign. Real work is
		// M0131-S21a; until then a refused start beats a rewound OID counter.
		{"XLOG_NEXTOID", xlogXLogNextOid},
		// The one opcode value PG 18 leaves undefined that goopg does not
		// claim either — a newer producer, or corruption that survived the
		// CRC check by construction. (0xF0, the other free slot, is goopg's
		// own empty-payload marker and is in the benign set above.)
		{"undefined opcode 0xC0", 0xC0},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			rec := Record{XLog: &XLogDecodedRecord{
				Header: XLogRecord{Rmid: RmgrXLog, Info: tc.info},
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
		})
	}
}

// TestReplayAppliesXLogFPIForHint pins the third S16.4 change: XLOG_FPI_FOR_HINT
// used to fall into the blanket `default:` no-op and its page was DROPPED.
// Upstream replays it on the same arm as XLOG_FPI (xlog.c:8748). goopg never
// emits FOR_HINT, so this only ever fires on a real-PG crash tail — which is
// exactly the path M0131 exists to make work.
func TestReplayAppliesXLogFPIForHint(t *testing.T) {
	rec := Record{XLog: &XLogDecodedRecord{
		Header: XLogRecord{Rmid: RmgrXLog, Info: xlogXLogFPIForHint},
	}}
	// No blocks and no manager: the arm must still report that it OWNS the
	// opcode (applied=true, no error) rather than falling through to the
	// no-op set. Upstream tolerates a FOR_HINT block with no image, so an
	// empty block list is not an error here (unlike the S16.3 btree arm).
	applied, err := replayDecodedXLogRecord(nil, rec)
	if err != nil {
		t.Fatalf("replay err = %v, want nil", err)
	}
	if !applied {
		t.Fatal("applied = false: XLOG_FPI_FOR_HINT fell through to the no-op set again")
	}
}
