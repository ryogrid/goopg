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
