package xlog

// B1.3b (docs/design/wal-pg-identical-stream/02c §3): sequence counter
// changes journal as PG's XLOG_SEQ_LOG (RM_SEQ_ID=15, info 0x00) — replacing
// the retired goopg-private RecordKindSequenceState(65)/DropSequence(66).
//
// Wire format (postgres/src/backend/commands/sequence.c:414-420):
//   - one block reference (ID 0, REGBUF_WILL_INIT, no image) naming the
//     sequence relation's block 0;
//   - main data = xl_seq_rec{RelFileLocator locator} (12 bytes: spcOid,
//     dbOid, relNumber — sequence.h:48) followed by the raw on-page heap
//     tuple (FormData_pg_sequence_data = last_value int8, log_cnt int8,
//     is_called bool).
//
// seq_redo (sequence.c:1892) REBUILDS a fresh 1-tuple page from the logged
// tuple (PageInit with a 4-byte sequence_magic special area, PageAddItem at
// FirstOffsetNumber) — it never needs a full-page image, so the emit side
// pairs the record with a record-covered dirty mark, not an FPI.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// xlogSeqLog is XLOG_SEQ_LOG (postgres/src/include/commands/sequence.h:63).
const xlogSeqLog uint8 = 0x00

// seqMagic is SEQ_MAGIC (sequence.c:63), stored in the sequence page's
// 4-byte special area.
const seqMagic uint32 = 0x1717

// sizeOfXlSeqRec is sizeof(xl_seq_rec): RelFileLocator = 3 × Oid.
const sizeOfXlSeqRec = 12

// EncodeSeqLogPG builds the XLOG_SEQ_LOG record for one sequence-page
// rewrite. tupleBytes is the full on-page heap tuple (header + data) exactly
// as PageAddItem stores it.
func EncodeSeqLogPG(rel storage.RelFileNode, tupleBytes []byte) ([]byte, error) {
	mainData := make([]byte, sizeOfXlSeqRec+len(tupleBytes))
	le := binary.LittleEndian
	le.PutUint32(mainData[0:4], 1663) // spcOid = pg_default
	le.PutUint32(mainData[4:8], rel.DBOid)
	le.PutUint32(mainData[8:12], rel.RelOid)
	copy(mainData[sizeOfXlSeqRec:], tupleBytes)
	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: 0, WillInit: true,
	}})
	if err != nil {
		return nil, err
	}
	return framePGAssembled(RmgrSeq, xlogSeqLog, 0, body), nil
}

// BuildSequencePage assembles the canonical 1-tuple sequence page
// (fill_seq_fork_with_data / seq_redo shape): PageInit with a MAXALIGN'd
// 4-byte special area carrying SEQ_MAGIC, and the tuple at offset 1.
func BuildSequencePage(tupleBytes []byte) (storage.Page, error) {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	const specialSize = 8 // MAXALIGN(sizeof(sequence_magic)=4)
	special := storage.BlockSize - specialSize
	h := storage.MustHeader(page)
	h.SetSpecial(uint16(special))
	binary.LittleEndian.PutUint32(page[special:], seqMagic)
	// Place the single tuple by hand: goopg's PageAddItemRaw computes the
	// item offset from BlockSize and would overwrite the special area (heap
	// pages have none, so it never mattered before B1.3b).
	itemLen := len(tupleBytes)
	aligned := (itemLen + 7) &^ 7
	upper := special - aligned
	copy(page[upper:], tupleBytes)
	lower := storage.SizeOfPageHeaderData
	lp := uint32(uint16(upper)&0x7FFF) |
		(uint32(uint8(storage.ItemIDNormal)&0x3) << 15) |
		(uint32(uint16(itemLen)&0x7FFF) << 17)
	binary.LittleEndian.PutUint32(page[lower:lower+4], lp)
	h.SetLower(uint16(lower + 4))
	h.SetUpper(uint16(upper))
	return page, nil
}

// replayDecodedXLogSeqLog applies XLOG_SEQ_LOG: rebuild the 1-tuple page
// from the logged tuple and write it as block 0 of the sequence relation
// (mirrors seq_redo). The locator comes from xl_seq_rec in the main data.
func replayDecodedXLogSeqLog(mgr *storage.Manager, mainData []byte) error {
	if len(mainData) < sizeOfXlSeqRec+1 {
		return fmt.Errorf("wal: seq-log main data too short (%d bytes)", len(mainData))
	}
	le := binary.LittleEndian
	rel := storage.RelFileNode{
		DBOid:  le.Uint32(mainData[4:8]),
		RelOid: le.Uint32(mainData[8:12]),
		Fork:   storage.MainFork,
	}
	page, err := BuildSequencePage(mainData[sizeOfXlSeqRec:])
	if err != nil {
		return err
	}
	// WriteBlock extends the file as needed (block 0 of a fresh relation).
	if err := mgr.WriteBlock(rel, 0, page); err != nil {
		return fmt.Errorf("seq-log replay: write: %w", err)
	}
	return nil
}
