// WAL → Decoder classifier for the M0008 logical-decoding
// pipeline. Walks records (typically through a *RecordIterator)
// and dispatches them into a *Decoder by xid.
//
// Per-record xid extraction:
//
//   - HeapInsert: xmin parsed from the encoded heap-tuple bytes
//     (the on-disk tuple header carries xmin at offset 0).
//     This is the inserting xact.
//   - HeapHotUpdate / HeapUpdate: xmin parsed from the new-tuple
//     bytes — the updating xact stamps xmin on the new tuple
//     (the same xact also stamps xmax on the old slot).
//   - HeapDelete: the encoded record carries xmax — the xact
//     that stamped the tuple as deleted.
//   - XactCommit / XactAbort: xid in the payload.
//   - HeapVacuum / BtreeInsert / BtreeSplit / PageImage /
//     Checkpoint: skipped — not user-data transactional events.
//
// See docs/design/0008-0001-logical-decoding-pipeline.md.
package wal

import (
	"github.com/goopg/goopg/internal/storage"
)

// Classify dispatches a single WAL record into the decoder.
// Records that aren't user-data transactional events (vacuum,
// btree, page image, checkpoint) are silently skipped — the
// decoder doesn't care about them and the apply worker won't
// see them.
//
// Returns the first error from the decoder's plugin path; the
// caller (typically a long-lived classifier loop) decides
// whether to disconnect the slot or keep going.
func Classify(d *Decoder, r Record) error {
	if d == nil {
		return nil
	}
	if len(r.Payload) == 0 {
		// PG-format records (a block ref is present, so parseXLogRecordData
		// leaves Payload nil) dispatch on the decoded XLog header. HeapInsert is
		// the first record flipped to PG form (A2); other PG-format records
		// (checkpoint, FPI, …) are not user-data changes and skip.
		return classifyDecodedXLog(d, r)
	}
	switch r.Payload[0] {
	case RecordKindHeapInsert:
		rel, blk, slot, tuple, err := DecodeHeapInsert(r.Payload)
		if err != nil {
			return err
		}
		xid, ok := xminFromTuple(tuple)
		if !ok {
			// Truncated tuple body: nothing useful for the
			// decoder. Don't fail the whole stream — log via
			// the no-op return; recovery already validated
			// the record's CRC, so this would be an internal
			// invariant break, not corruption.
			return nil
		}
		d.ApplyChange(xid, Change{
			Kind:     ChangeInsert,
			LSN:      r.EndLSN,
			Rel:      rel,
			Block:    blk,
			LineSlot: slot,
			NewTuple: tuple,
		})
		return nil
	case RecordKindHeapHotUpdate:
		rel, blk, oldSlot, _, tuple, err := DecodeHeapHotUpdate(r.Payload)
		if err != nil {
			return err
		}
		// The updating xact's xid lives at offset 0 of the
		// new tuple body (xmin). xmax in the record header is
		// the same value, but xminFromTuple keeps the extract
		// path identical to HeapInsert above.
		xid, ok := xminFromTuple(tuple)
		if !ok {
			return nil
		}
		d.ApplyChange(xid, Change{
			Kind:     ChangeUpdate,
			LSN:      r.EndLSN,
			Rel:      rel,
			Block:    blk,
			LineSlot: oldSlot,
			NewTuple: tuple,
			// OldTuple intentionally empty — HOT-update
			// records do not carry the pre-image. Under
			// REPLICA IDENTITY DEFAULT, pgoutput's
			// writeUpdate emits 'U' relOid 'N' newTuple
			// directly (no K/O block), matching upstream's
			// logicalrep_write_update.
		})
		return nil
	case RecordKindHeapUpdate:
		p, err := DecodeHeapUpdate(r.Payload)
		if err != nil {
			return err
		}
		xid, ok := xminFromTuple(p.Tuple)
		if !ok {
			return nil
		}
		d.ApplyChange(xid, Change{
			Kind:     ChangeUpdate,
			LSN:      r.EndLSN,
			Rel:      p.Rel,
			Block:    p.NewBlk,
			LineSlot: p.NewLineSlot,
			NewTuple: p.Tuple,
		})
		return nil
	case RecordKindHeapDelete:
		rel, blk, slot, xmax, oldTuple, err := DecodeHeapDelete(r.Payload)
		if err != nil {
			return err
		}
		if xmax == storage.InvalidTransactionID {
			return nil
		}
		d.ApplyChange(xmax, Change{
			Kind:     ChangeDelete,
			LSN:      r.EndLSN,
			Rel:      rel,
			Block:    blk,
			LineSlot: slot,
			OldTuple: oldTuple, // nil for legacy records without the extension
			// FULL replica identity / pre-delete tuple
		})
		return nil
	case RecordKindXactCommit:
		xid, err := DecodeXactMarker(r.Payload)
		if err != nil {
			return err
		}
		return d.ApplyCommit(xid, r.EndLSN)
	case RecordKindXactAbort:
		xid, err := DecodeXactMarker(r.Payload)
		if err != nil {
			return err
		}
		d.ApplyAbort(xid)
		return nil
	}
	// Other kinds (HeapVacuum, BtreeInsert, BtreeSplit,
	// PageImage, Checkpoint) aren't user-data transactional
	// events — skip silently so the decoder loop stays simple.
	return nil
}

// classifyDecodedXLog dispatches a PG-format record (no native Payload) into the
// decoder using its decoded XLog header + block refs. Currently only heap-insert
// is flipped to PG form; the inserting xid is the record header's xl_xid (== the
// tuple's t_xmin), and the tuple is reconstructed from block 0 exactly as
// recovery does. Other rmgrs / opcodes are not user-data changes and skip.
func classifyDecodedXLog(d *Decoder, r Record) error {
	if r.XLog == nil {
		return nil
	}
	h := r.XLog.Header
	if h.Rmid != RmgrHeap {
		return nil
	}
	block, ok := xlogBlockRefByID(r.XLog, 0)
	if !ok {
		return nil
	}
	switch h.Info & xlogHeapOpMask {
	case xlogHeapInsert:
		offnum, err := decodeXLogHeapInsertMainData(r.XLog.MainData)
		if err != nil {
			return err
		}
		xid := storage.TransactionID(h.XID)
		tuple, err := decodeXLogHeapInsertTuple(block, xid, offnum)
		if err != nil {
			return err
		}
		d.ApplyChange(xid, Change{
			Kind:     ChangeInsert,
			LSN:      r.EndLSN,
			Rel:      block.Rel,
			Block:    block.Block,
			LineSlot: offnum,
			NewTuple: tuple,
		})
	case xlogHeapDelete:
		xmax, offnum, _, flags, err := decodeXLogHeapDeleteMainData(r.XLog.MainData)
		if err != nil {
			return err
		}
		if storage.TransactionID(xmax) == storage.InvalidTransactionID {
			return nil
		}
		var oldTuple []byte
		if flags&xlhDeleteContainsOldTuple != 0 && len(r.XLog.MainData) > sizeOfXLogHeapDeleteData {
			oldTuple, err = reconstructMarshaledTupleFromHeader(r.XLog.MainData[sizeOfXLogHeapDeleteData:])
			if err != nil {
				return err
			}
		}
		d.ApplyChange(storage.TransactionID(xmax), Change{
			Kind:     ChangeDelete,
			LSN:      r.EndLSN,
			Rel:      block.Rel,
			Block:    block.Block,
			LineSlot: offnum,
			OldTuple: oldTuple,
		})
	case xlogHeapHotUpdate:
		_, oldOffnum, _, _, _, newOffnum, err := decodeXLogHeapUpdateMainData(r.XLog.MainData)
		if err != nil {
			return err
		}
		xid := storage.TransactionID(h.XID)
		tuple, err := decodeXLogHeapInsertTuple(block, xid, newOffnum)
		if err != nil {
			return err
		}
		d.ApplyChange(xid, Change{
			Kind:     ChangeUpdate,
			LSN:      r.EndLSN,
			Rel:      block.Rel,
			Block:    block.Block,
			LineSlot: oldOffnum,
			NewTuple: tuple,
			// OldTuple empty — HOT update under REPLICA IDENTITY DEFAULT, matching
			// the native RecordKindHeapHotUpdate classifier path.
		})
	}
	return nil
}

// xminFromTuple parses the xmin field at offset 0 of an encoded
// heap-tuple body. Returns ok=false when the payload is shorter
// than the tuple header (defensive — recovery already validates
// CRC, but a mis-shaped record shouldn't crash the decoder).
func xminFromTuple(tuple []byte) (storage.TransactionID, bool) {
	if len(tuple) < 4 {
		return 0, false
	}
	// Heap tuple bytes start with xmin(4) — see
	// internal/storage/heap.go MarshalBinary. This lets us
	// extract the xact id without re-parsing the whole header.
	return storage.TransactionID(uint32FromLE(tuple[0:4])), true
}

func uint32FromLE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
