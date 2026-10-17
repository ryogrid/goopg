// Package walsummarizer is a port of PostgreSQL 18.3's
// backend/postmaster/walsummarizer.c: it reads WAL and records, per range
// between two redo points, which blocks of which relations were modified
// (docs/design/0100-0149/0122-0009-wal-summarizer.md).
//
// This file is slice S2b: the record classifier (SummarizeDbaseRecord,
// SummarizeSmgrRecord, SummarizeXactRecord, SummarizeXlogRecord and the
// block-reference loop) and the SummarizeWAL loop, run offline over a list
// of decoded records. The live goroutine (S3) feeds the same core.
//
// The package imports only xlog, storage, blkreftable and walsummary, so
// internal/backup can import it later for incremental backup.
package walsummarizer

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/backup/walsummary"
	"github.com/goopg/goopg/internal/storage"
)

// Resource managers and record types the summarizer inspects (rmgrlist.h,
// pg_control.h, storage_xlog.h, dbcommands_xlog.h, xact.h).
const (
	rmXlogID  = 0
	rmXactID  = 1
	rmSmgrID  = 2
	rmDbaseID = 4

	xlrInfoMask = 0x0F // XLR_INFO_MASK

	xlogCheckpointShutdown = 0x00
	xlogParameterChange    = 0x60
	xlogEndOfRecovery      = 0x90
	xlogCheckpointRedo     = 0xE0

	xlogSmgrCreate   = 0x10
	xlogSmgrTruncate = 0x20
	smgrTruncateHeap = 0x0001
	smgrTruncateVM   = 0x0002

	xlogDbaseCreateFileCopy = 0x00
	xlogDbaseCreateWalLog   = 0x10
	xlogDbaseDrop           = 0x20

	xlogXactOpMask         = 0x70
	xlogXactCommit         = 0x00
	xlogXactAbort          = 0x20
	xlogXactCommitPrepared = 0x30
	xlogXactAbortPrepared  = 0x40

	walLevelMinimal = 0 // WAL_LEVEL_MINIMAL

	mainFork = 0
	fsmFork  = 1
	vmFork   = 2
	maxFork  = 3 // MAX_FORKNUM (INIT_FORKNUM)

	// The built-in tablespaces, which goopg's WAL decoder folds to
	// TblOid 0 (pg_xlog_decode.go).
	defaultTablespaceOID = 1663
	globalTablespaceOID  = 1664
)

// readRecPtr and endRecPtr translate a decoded record's positions to PG's
// XLogReaderState: goopg's StartLSN is 1-based (StartLSN = ReadRecPtr + 1),
// and EndLSN is PG's EndRecPtr. Every LSN this package stores or names a
// file by goes through these two helpers.
func readRecPtr(r xlog.Record) uint64 { return r.StartLSN - 1 }
func endRecPtr(r xlog.Record) uint64  { return r.EndLSN }

// blockLocator rebuilds the RelFileLocator of a decoded block reference.
// The decoder keeps a user tablespace's OID in TblOid but folds the two
// built-in ones to 0: dbOid 0 means pg_global, anything else pg_default.
func blockLocator(rel xlog.XLogBlockRef) blkreftable.RelFileLocator {
	return relLocator(rel.Rel)
}

// relLocator is blockLocator for a storage.RelFileNode, which goopg-native
// payloads also carry with TblOid 0.
func relLocator(rel storage.RelFileNode) blkreftable.RelFileLocator {
	spc := rel.TblOid
	if spc == 0 {
		spc = defaultTablespaceOID
		if rel.DBOid == 0 {
			spc = globalTablespaceOID
		}
	}
	return blkreftable.RelFileLocator{SpcOid: spc, DbOid: rel.DBOid, RelNumber: rel.RelOid}
}

func rawLocator(b []byte) blkreftable.RelFileLocator {
	return blkreftable.RelFileLocator{
		SpcOid:    binary.LittleEndian.Uint32(b[0:4]),
		DbOid:     binary.LittleEndian.Uint32(b[4:8]),
		RelNumber: binary.LittleEndian.Uint32(b[8:12]),
	}
}

// boundary reports whether r is a record SummarizeXlogRecord stops at
// (CHECKPOINT_REDO, CHECKPOINT_SHUTDOWN, PARAMETER_CHANGE, END_OF_RECOVERY)
// and, if so, whether the WAL after it was written at wal_level=minimal and
// must be fast-forwarded.
func boundary(x *xlog.XLogDecodedRecord) (isBoundary, fastForward bool, err error) {
	var off int
	switch x.Header.Info &^ xlrInfoMask {
	case xlogCheckpointRedo:
		off = 0 // the payload is the int wal_level
	case xlogCheckpointShutdown:
		off = 20 // CheckPoint.wal_level
	case xlogParameterChange:
		off = 20 // xl_parameter_change.wal_level
	case xlogEndOfRecovery:
		off = 16 // xl_end_of_recovery.wal_level
	default:
		return false, false, nil
	}
	if len(x.MainData) < off+4 {
		return false, false, fmt.Errorf("walsummarizer: RM_XLOG info %#x record has %d bytes of main data", x.Header.Info, len(x.MainData))
	}
	level := int32(binary.LittleEndian.Uint32(x.MainData[off:]))
	return true, level == walLevelMinimal, nil
}

// summarizeDbase is SummarizeDbaseRecord: creating or dropping a database
// sets the limit block of the {tablespace, database, relNumber 0} sentinel to
// 0, which makes an incremental backup copy the whole database.
func summarizeDbase(x *xlog.XLogDecodedRecord, tab *blkreftable.Table) error {
	d := x.MainData
	switch x.Header.Info &^ xlrInfoMask {
	case xlogDbaseCreateFileCopy, xlogDbaseCreateWalLog:
		if len(d) < 8 {
			return fmt.Errorf("walsummarizer: short dbase create record (%d bytes)", len(d))
		}
		db, spc := binary.LittleEndian.Uint32(d[0:4]), binary.LittleEndian.Uint32(d[4:8])
		tab.SetLimitBlock(blkreftable.RelFileLocator{SpcOid: spc, DbOid: db}, mainFork, 0)
	case xlogDbaseDrop:
		if len(d) < 8 {
			return fmt.Errorf("walsummarizer: short dbase drop record (%d bytes)", len(d))
		}
		db, n := binary.LittleEndian.Uint32(d[0:4]), int(int32(binary.LittleEndian.Uint32(d[4:8])))
		if n < 0 || len(d) < 8+4*n {
			return fmt.Errorf("walsummarizer: dbase drop record lists %d tablespaces in %d bytes", n, len(d))
		}
		for i := 0; i < n; i++ {
			spc := binary.LittleEndian.Uint32(d[8+4*i:])
			tab.SetLimitBlock(blkreftable.RelFileLocator{SpcOid: spc, DbOid: db}, mainFork, 0)
		}
	}
	return nil
}

// summarizeSmgr is SummarizeSmgrRecord: a created fork starts with limit
// block 0; a truncated main fork or visibility map gets the truncation point.
// The FSM is not fully WAL-logged and is ignored.
func summarizeSmgr(x *xlog.XLogDecodedRecord, tab *blkreftable.Table) error {
	d := x.MainData
	switch x.Header.Info &^ xlrInfoMask {
	case xlogSmgrCreate: // xl_smgr_create{rlocator, forkNum}
		if len(d) < 16 {
			return fmt.Errorf("walsummarizer: short smgr create record (%d bytes)", len(d))
		}
		fork := int32(binary.LittleEndian.Uint32(d[12:16]))
		if fork != fsmFork {
			tab.SetLimitBlock(rawLocator(d[0:12]), fork, 0)
		}
	case xlogSmgrTruncate: // xl_smgr_truncate{blkno, rlocator, flags}
		if len(d) < 20 {
			return fmt.Errorf("walsummarizer: short smgr truncate record (%d bytes)", len(d))
		}
		blk := binary.LittleEndian.Uint32(d[0:4])
		loc := rawLocator(d[4:16])
		flags := binary.LittleEndian.Uint32(d[16:20])
		if flags&smgrTruncateHeap != 0 {
			tab.SetLimitBlock(loc, mainFork, blk)
		}
		if flags&smgrTruncateVM != 0 {
			tab.SetLimitBlock(loc, vmFork, blk) // sic: the heap block number, as PG does
		}
	}
	return nil
}

// summarizeXact is SummarizeXactRecord: every fork but the FSM of a relation
// dropped at commit or abort gets limit block 0.
func summarizeXact(x *xlog.XLogDecodedRecord, tab *blkreftable.Table) error {
	switch (x.Header.Info &^ xlrInfoMask) & xlogXactOpMask {
	case xlogXactCommit, xlogXactCommitPrepared, xlogXactAbort, xlogXactAbortPrepared:
	default:
		return nil
	}
	parsed, err := xlog.ParseXactRecord(x.Header.Info, x.MainData)
	if err != nil {
		return err
	}
	for _, r := range parsed.DroppedRels {
		loc := blkreftable.RelFileLocator{SpcOid: r.SpcOid, DbOid: r.DbOid, RelNumber: r.RelNumber}
		for fork := int32(0); fork <= maxFork; fork++ {
			if fork != fsmFork {
				tab.SetLimitBlock(loc, fork, 0)
			}
		}
	}
	return nil
}

// summarizeNative classifies the goopg-native records that modify a block
// without naming it in a block reference (design §4.4 items 1-3), so a
// summary does not miss what PG's block-ref'd equivalent would have marked:
//   - HeapLock (PG: XLOG_HEAP_LOCK, block 0 = the heap page) marks the page;
//   - HeapVacuum (PG: XLOG_HEAP2_PRUNE_VACUUM_SCAN) marks the page. No live
//     producer emits it any more; it is classified for older WAL;
//   - HeapVisible (PG: XLOG_HEAP2_VISIBLE, block 0 = the VM page, block 1 =
//     the heap page) marks both when it sets bits. A clear marks nothing:
//     PG clears the map inside the heap record without a VM block
//     reference. A dropped relation's map gets limit block 0, as the
//     SMGR_TRUNCATE of its VM fork would set.
//
// The payloads carry no tablespace, so relLocator places them in pg_default
// (pg_global for a shared relation), where goopg keeps every VM fork.
func summarizeNative(payload []byte, tab *blkreftable.Table) error {
	if len(payload) == 0 {
		return nil
	}
	switch payload[0] {
	case xlog.RecordKindHeapLock:
		rel, blk, _, _, _, err := xlog.DecodeHeapLock(payload)
		if err != nil {
			return err
		}
		tab.MarkBlockModified(relLocator(rel), mainFork, uint32(blk))
	case xlog.RecordKindHeapVacuum:
		rel, blk, _, err := xlog.DecodeHeapVacuum(payload)
		if err != nil {
			return err
		}
		tab.MarkBlockModified(relLocator(rel), mainFork, uint32(blk))
	case xlog.RecordKindHeapVisible:
		p, err := xlog.DecodeHeapVisible(payload)
		if err != nil {
			return err
		}
		loc := relLocator(p.Rel)
		switch {
		case p.Flags&xlog.HeapVisibleDropRelation != 0:
			tab.SetLimitBlock(loc, vmFork, 0)
		case p.Flags&(xlog.HeapVisibleSetAllVisible|xlog.HeapVisibleSetAllFrozen) != 0:
			tab.MarkBlockModified(loc, vmFork, uint32(storage.VMBlockForHeapBlock(p.HeapBlk)))
			tab.MarkBlockModified(loc, mainFork, uint32(p.HeapBlk))
		}
	}
	return nil
}

// rangeSummarizer is one SummarizeWAL call, fed a record at a time: it
// accumulates the BlockRefTable for the WAL from start until the next
// boundary record. Both the offline Summarize and the live Summarizer drive
// it, so the two cannot drift apart.
type rangeSummarizer struct {
	tli         uint32
	start, end  uint64
	fastForward bool
	tab         *blkreftable.Table
}

func newRangeSummarizer(tli uint32, start uint64) *rangeSummarizer {
	return &rangeSummarizer{tli: tli, start: start, end: start, fastForward: true, tab: blkreftable.NewTable()}
}

// add feeds one record whose ReadRecPtr is at or after start. done reports
// that r is a boundary ending this range at r's ReadRecPtr; r is then NOT
// consumed and must be fed to the next range, which starts there.
func (rs *rangeSummarizer) add(r xlog.Record) (done bool, err error) {
	x := r.XLog
	if x == nil {
		// A legacy bare-record stream has no XLogRecord header; only the
		// native payload says what it modified.
		if !rs.fastForward {
			if err := summarizeNative(r.Payload, rs.tab); err != nil {
				return false, fmt.Errorf("record at %X/%X: %w", uint32(readRecPtr(r)>>32), uint32(readRecPtr(r)), err)
			}
		}
		rs.end = endRecPtr(r)
		return false, nil
	}
	switch x.Header.Rmid {
	case rmXlogID:
		isBoundary, ff, berr := boundary(x)
		if berr != nil {
			return false, berr
		}
		if isBoundary {
			if readRecPtr(r) > rs.start {
				rs.end = readRecPtr(r)
				return true, nil
			}
			rs.fastForward = ff
		}
	case rmDbaseID:
		if !rs.fastForward {
			err = summarizeDbase(x, rs.tab)
		}
	case rmSmgrID:
		if !rs.fastForward {
			err = summarizeSmgr(x, rs.tab)
		}
	case rmXactID:
		if !rs.fastForward {
			err = summarizeXact(x, rs.tab)
		}
	}
	// A goopg-native record is stamped with its PG analogue's (rmid, info)
	// but has no block reference; a real PG record of those kinds always
	// has one, so the two cannot be confused.
	if err == nil && !rs.fastForward && len(x.Blocks) == 0 && xlog.IsGoopgNativeRecord(r) {
		err = summarizeNative(r.Payload, rs.tab)
	}
	if err != nil {
		return false, fmt.Errorf("record at %X/%X: %w", uint32(readRecPtr(r)>>32), uint32(readRecPtr(r)), err)
	}
	if !rs.fastForward {
		for _, b := range x.Blocks {
			if int32(b.Rel.Fork) != fsmFork {
				rs.tab.MarkBlockModified(blockLocator(b), int32(b.Rel.Fork), uint32(b.Block))
			}
		}
	}
	rs.end = endRecPtr(r)
	return false, nil
}

// finish writes the range's summary file, unless the range is empty or was
// fast-forwarded (wal_level=minimal), and reports whether it wrote one.
func (rs *rangeSummarizer) finish(dataDir string) (walsummary.File, bool, error) {
	f := walsummary.File{TLI: rs.tli, Start: rs.start, End: rs.end}
	if rs.end <= rs.start || rs.fastForward {
		return f, false, nil
	}
	if err := walsummary.Write(dataDir, f, rs.tab); err != nil {
		return f, false, err
	}
	return f, true, nil
}

// Summarize runs the summarizer over recs, a decoded WAL stream on timeline
// tli, from start (an exact record start, as after a previous summary) to the
// last redo point in recs, writing one summary file per completed range under
// dataDir's pg_wal/summaries. It returns the files written, in order, and the
// LSN where summarization would resume. Records after the last redo point stay
// unsummarized; the live summarizer would wait for more WAL.
func Summarize(dataDir string, tli uint32, recs []xlog.Record, start uint64) ([]walsummary.File, uint64, error) {
	var files []walsummary.File
	cur := newRangeSummarizer(tli, start)
	for _, r := range recs {
		if readRecPtr(r) < start {
			continue
		}
		done, err := cur.add(r)
		if err != nil {
			return files, cur.start, err
		}
		if !done {
			continue
		}
		f, wrote, err := cur.finish(dataDir)
		if err != nil {
			return files, cur.start, err
		}
		if wrote {
			files = append(files, f)
		}
		cur = newRangeSummarizer(tli, cur.end)
		if _, err := cur.add(r); err != nil { // the boundary record opens the next range
			return files, cur.start, err
		}
	}
	return files, cur.start, nil
}
