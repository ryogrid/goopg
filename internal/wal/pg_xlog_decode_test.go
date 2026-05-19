package wal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestDecodeRecordXLogDetailedPreservesPGHeapInsert(t *testing.T) {
	recordBytes, wantMain, wantBlockData := encodeTestPGHeapInsertRecord(t)
	decoded, err := decodeRecordXLogDetailed(recordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Payload != nil {
		t.Fatalf("native payload = %v, want nil for PostgreSQL physical record", decoded.Payload)
	}
	if decoded.XLog == nil {
		t.Fatal("decoded.XLog = nil, want structured XLog record")
	}
	if decoded.XLog.Header.Rmid != RmgrHeap {
		t.Fatalf("rmid = %d, want %d", decoded.XLog.Header.Rmid, RmgrHeap)
	}
	if decoded.XLog.Header.Info != xlogHeapInsert {
		t.Fatalf("info = 0x%02x, want 0x%02x", decoded.XLog.Header.Info, xlogHeapInsert)
	}
	if decoded.XLog.Header.XID != 42 {
		t.Fatalf("xid = %d, want 42", decoded.XLog.Header.XID)
	}
	if !bytes.Equal(decoded.XLog.MainData, wantMain) {
		t.Fatalf("main data = %v, want %v", decoded.XLog.MainData, wantMain)
	}
	if len(decoded.XLog.Blocks) != 1 {
		t.Fatalf("block refs = %d, want 1", len(decoded.XLog.Blocks))
	}
	blk := decoded.XLog.Blocks[0]
	if blk.Rel.DBOid != 123 || blk.Rel.RelOid != 456 || blk.Rel.Fork != storage.MainFork {
		t.Fatalf("block rel = %+v, want db=123 rel=456 fork=%d", blk.Rel, storage.MainFork)
	}
	if blk.Block != 7 {
		t.Fatalf("block no = %d, want 7", blk.Block)
	}
	if !bytes.Equal(blk.Data, wantBlockData) {
		t.Fatalf("block data = %v, want %v", blk.Data, wantBlockData)
	}
}

func TestDecodeRecordXLogRejectsPGPhysicalViaLegacyPayloadHelper(t *testing.T) {
	recordBytes, _, _ := encodeTestPGHeapInsertRecord(t)
	payload, _, err := decodeRecordXLog(recordBytes)
	if err == nil {
		t.Fatalf("decodeRecordXLog payload = %v, want error for PostgreSQL physical record", payload)
	}
	if payload != nil {
		t.Fatalf("payload = %v, want nil", payload)
	}
}

func TestReadAllPageAwareKeepsPGRecordStructured(t *testing.T) {
	recordBytes, wantMain, wantBlockData := encodeTestPGHeapInsertRecord(t)
	stream := append(buildTestLongPageHeader(t), recordBytes...)
	records, err := readAllPageAware(stream, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Payload != nil {
		t.Fatalf("payload = %v, want nil for PostgreSQL physical record", records[0].Payload)
	}
	if records[0].XLog == nil {
		t.Fatal("records[0].XLog = nil, want structured XLog record")
	}
	if !bytes.Equal(records[0].XLog.MainData, wantMain) {
		t.Fatalf("main data = %v, want %v", records[0].XLog.MainData, wantMain)
	}
	if len(records[0].XLog.Blocks) != 1 || !bytes.Equal(records[0].XLog.Blocks[0].Data, wantBlockData) {
		t.Fatalf("block refs = %+v, want one block with data %v", records[0].XLog.Blocks, wantBlockData)
	}
	if records[0].StartLSN != uint64(SizeOfXLogLongPHD)+1 {
		t.Fatalf("start lsn = %d, want %d", records[0].StartLSN, uint64(SizeOfXLogLongPHD)+1)
	}
	if records[0].EndLSN != uint64(SizeOfXLogLongPHD)+uint64(maxAlignXLog(len(recordBytes))) {
		t.Fatalf("end lsn = %d, want %d", records[0].EndLSN, uint64(SizeOfXLogLongPHD)+uint64(maxAlignXLog(len(recordBytes))))
	}
}

func TestDecodeRecordXLogDetailedPreservesPGBlockImageRecord(t *testing.T) {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	copy(page[storage.SizeOfPageHeaderData:], []byte("img"))
	fragments := make([]byte, 0, len(page)+64)
	fragments = append(fragments, 0)
	fragments = append(fragments, bkpBlockHasImage|byte(storage.MainFork))
	fragments = append(fragments, 0, 0)
	var imageHeader [sizeOfXLogRecordBlockImageHeader]byte
	binary.LittleEndian.PutUint16(imageHeader[0:2], uint16(len(page)))
	binary.LittleEndian.PutUint16(imageHeader[2:4], 0)
	imageHeader[4] = bkpImageApply
	fragments = append(fragments, imageHeader[:]...)
	var relLocator [sizeOfRelFileLocator]byte
	binary.LittleEndian.PutUint32(relLocator[0:4], pgDefaultTableSpaceOID)
	binary.LittleEndian.PutUint32(relLocator[4:8], 123)
	binary.LittleEndian.PutUint32(relLocator[8:12], 456)
	fragments = append(fragments, relLocator[:]...)
	var blkNo [4]byte
	binary.LittleEndian.PutUint32(blkNo[:], 7)
	fragments = append(fragments, blkNo[:]...)
	fragments = append(fragments, page...)

	recordBytes := make([]byte, maxAlignXLog(SizeOfXLogRecord+len(fragments)))
	header := XLogRecord{TotLen: uint32(SizeOfXLogRecord + len(fragments)), Rmid: RmgrHeap, Info: xlogHeapInsert}
	if err := EncodeXLogRecordHeader(recordBytes[:SizeOfXLogRecord], header, fragments); err != nil {
		t.Fatal(err)
	}
	copy(recordBytes[SizeOfXLogRecord:], fragments)

	decoded, err := decodeRecordXLogDetailed(recordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.XLog == nil || len(decoded.XLog.Blocks) != 1 {
		t.Fatalf("decoded block refs = %+v, want one block image ref", decoded.XLog)
	}
	blk := decoded.XLog.Blocks[0]
	if !blk.HasImage || !blk.ImageApply {
		t.Fatalf("block image flags = has=%v apply=%v, want both true", blk.HasImage, blk.ImageApply)
	}
	if blk.Rel.DBOid != 123 || blk.Rel.RelOid != 456 || blk.Block != 7 {
		t.Fatalf("block locator = %+v block=%d, want db=123 rel=456 block=7", blk.Rel, blk.Block)
	}
	if !bytes.Equal(blk.Image, page) {
		t.Fatalf("decoded image mismatch")
	}
}

func encodeTestPGHeapInsertRecord(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	mainData := make([]byte, 4)
	binary.LittleEndian.PutUint16(mainData[0:2], 1)
	mainData[2] = 0x01
	blockData := testXLogHeapInsertTupleData(storage.DefaultHeapTupleHoff, []byte("val"))
	fragments := make([]byte, 0, 64)
	fragments = append(fragments, 0)
	fragments = append(fragments, bkpBlockHasData|byte(storage.MainFork))
	var dataLen [2]byte
	binary.LittleEndian.PutUint16(dataLen[:], uint16(len(blockData)))
	fragments = append(fragments, dataLen[:]...)
	var relLocator [sizeOfRelFileLocator]byte
	binary.LittleEndian.PutUint32(relLocator[0:4], pgDefaultTableSpaceOID)
	binary.LittleEndian.PutUint32(relLocator[4:8], 123)
	binary.LittleEndian.PutUint32(relLocator[8:12], 456)
	fragments = append(fragments, relLocator[:]...)
	var blkNo [4]byte
	binary.LittleEndian.PutUint32(blkNo[:], 7)
	fragments = append(fragments, blkNo[:]...)
	fragments = append(fragments, xlrBlockIDDataShort, byte(len(mainData)))
	fragments = append(fragments, blockData...)
	fragments = append(fragments, mainData...)

	recordBytes := make([]byte, maxAlignXLog(SizeOfXLogRecord+len(fragments)))
	header := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(fragments)),
		XID:    42,
		Rmid:   RmgrHeap,
		Info:   xlogHeapInsert,
	}
	if err := EncodeXLogRecordHeader(recordBytes[:SizeOfXLogRecord], header, fragments); err != nil {
		t.Fatal(err)
	}
	copy(recordBytes[SizeOfXLogRecord:], fragments)
	return recordBytes, mainData, blockData
}

func buildTestLongPageHeader(t *testing.T) []byte {
	t.Helper()
	buf := make([]byte, SizeOfXLogLongPHD)
	header := XLogLongPageHeader{
		Std: XLogPageHeader{
			Magic:    XLOGPageMagic,
			TLI:      1,
			PageAddr: 0,
			RemLen:   0,
		},
		SysID:      1,
		SegSize:    uint32(DefaultSegmentSize),
		XLogBlcksz: XLOGBlockSize,
	}
	if err := EncodeXLogLongPageHeader(buf, header); err != nil {
		t.Fatal(err)
	}
	return buf
}
