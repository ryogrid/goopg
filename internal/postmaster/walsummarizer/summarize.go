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
// The package imports only xlog, blkreftable and walsummary, so internal/backup
// can import it later for incremental backup.
package walsummarizer

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/backup/walsummary"
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
	spc := rel.Rel.TblOid
	if spc == 0 {
		spc = defaultTablespaceOID
		if rel.Rel.DBOid == 0 {
			spc = globalTablespaceOID
		}
	}
	return blkreftable.RelFileLocator{SpcOid: spc, DbOid: rel.Rel.DBOid, RelNumber: rel.Rel.RelOid}
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

// summarizeRange is one SummarizeWAL call with exact=true over recs[i:], where
// recs[i] is the first record at or after start. It returns where the summary
// ended, the index of the first record it did not consume, and whether it
// reached a boundary; reachedEnd means the records ran out first, so the
// range is still open and no file is written (the live summarizer would wait
// for more WAL). wrote is false when the range was fast-forwarded or empty.
func summarizeRange(dataDir string, tli uint32, recs []xlog.Record, i int, start uint64) (end uint64, next int, wrote, reachedEnd bool, err error) {
	tab := blkreftable.NewTable()
	fastForward := true
	end = start
	for ; i < len(recs); i++ {
		r := recs[i]
		x := r.XLog
		if x == nil {
			// A goopg-native record without an XLogRecord header carries no
			// block references the summarizer can see (design §4.4, S6a).
			end = endRecPtr(r)
			continue
		}
		switch x.Header.Rmid {
		case rmXlogID:
			isBoundary, ff, berr := boundary(x)
			if berr != nil {
				return 0, i, false, false, berr
			}
			if isBoundary {
				if readRecPtr(r) > start {
					end = readRecPtr(r)
					goto done
				}
				fastForward = ff
			}
		case rmDbaseID:
			if !fastForward {
				err = summarizeDbase(x, tab)
			}
		case rmSmgrID:
			if !fastForward {
				err = summarizeSmgr(x, tab)
			}
		case rmXactID:
			if !fastForward {
				err = summarizeXact(x, tab)
			}
		}
		if err != nil {
			return 0, i, false, false, fmt.Errorf("record at %X/%X: %w", uint32(readRecPtr(r)>>32), uint32(readRecPtr(r)), err)
		}
		if !fastForward {
			for _, b := range x.Blocks {
				if int32(b.Rel.Fork) != fsmFork {
					tab.MarkBlockModified(blockLocator(b), int32(b.Rel.Fork), uint32(b.Block))
				}
			}
		}
		end = endRecPtr(r)
	}
	return end, i, false, true, nil
done:
	if end > start && !fastForward {
		if err := walsummary.Write(dataDir, walsummary.File{TLI: tli, Start: start, End: end}, tab); err != nil {
			return 0, i, false, false, err
		}
		wrote = true
	}
	return end, i, wrote, false, nil
}

// Summarize runs the summarizer over recs, a decoded WAL stream on timeline
// tli, from start (an exact record start, as after a previous summary) to the
// last redo point in recs, writing one summary file per completed range under
// dataDir's pg_wal/summaries. It returns the files written, in order, and the
// LSN where summarization would resume.
func Summarize(dataDir string, tli uint32, recs []xlog.Record, start uint64) ([]walsummary.File, uint64, error) {
	i := 0
	for i < len(recs) && readRecPtr(recs[i]) < start {
		i++
	}
	var files []walsummary.File
	for {
		end, next, wrote, reachedEnd, err := summarizeRange(dataDir, tli, recs, i, start)
		if err != nil {
			return files, start, err
		}
		if reachedEnd {
			return files, start, nil
		}
		if wrote {
			files = append(files, walsummary.File{TLI: tli, Start: start, End: end})
		}
		start, i = end, next
	}
}
