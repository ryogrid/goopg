package xlog

import (
	"fmt"
	"sort"
	"strings"
)

// M0131-S25 — the index-AM boundary.
//
// Five of PostgreSQL 18's resource managers are index access methods goopg has
// no redo for at all: RM_HASH_ID (12), RM_GIN_ID (13), RM_GIST_ID (14),
// RM_SPGIST_ID (16) and RM_BRIN_ID (17) (rmgrlist.h:41-46). Unlike the four
// rmgrs S23 closed, these cannot be finished with a no-op or a page-diff
// replay: each is a full page-level algorithm, and every one of them is
// REACHABLE from an ordinary PG cluster the moment somebody writes
// `CREATE INDEX ... USING gin`.
//
// Before this slice all five fell through replayDecodedXLogRecord's `default:`
// arm and produced `wal: unsupported xlog record rmid=13 info=0x20 lsn[…]`.
// That is fail-closed, which is correct, but it is not actionable: the operator
// has to own a copy of rmgrlist.h to learn that 13 means GIN, and a cluster
// with both a GIN and a BRIN index teaches only about whichever record replay
// reached first — five restart attempts to learn one fact.
//
// So this file does two things and deliberately no more:
//
//  1. names the refusal (indexAMUnsupportedError), so the message says which
//     access method, at which LSN, on which relation; and
//  2. reports the WHOLE boundary at once (preflightIndexAMRecords), scanning
//     the replay range before the first page is written so one failed start
//     lists every unimplementable AM in the stream.
//
// Point 2 also moves the refusal EARLIER: replay now stops before applying any
// record, instead of applying the prefix up to the first GIN record and then
// stopping. Both orders leave the cluster unstarted, but the pre-flight order
// leaves the data directory untouched, which is the difference between a
// directory a real PG can still open and one that has been half-advanced by an
// engine that then refused to finish.
//
// What is NOT here: any attempt at a full-page-image shortcut. requireFullPageImages
// (recovery.go) rescues an unimplemented BTREE opcode when every block it
// touched carries an applicable image, and the natural question is why the same
// trick is not applied to these five. It is provably wrong for them, for two
// independent reasons:
//
//   - REGBUF_WILL_INIT blocks structurally never carry an FPI — XLogRecordAssemble
//     drops the image for a block the redo routine is expected to rebuild from
//     scratch. There are 23 REGBUF_WILL_INIT call sites across the five AMs
//     (hashovfl.c 2, hashpage.c 3, gindatapage.c 1, ginfast.c 4, ginutil.c 1,
//     gistxlog.c 1, spgdoinsert.c 7, brin_revmap.c 1, brin.c 1,
//     brin_pageops.c 2), so an image-only replay of those records restores
//     nothing and requireFullPageImages would refuse them anyway.
//   - GiST is worse than "no image": an image is not SUFFICIENT even when one is
//     present. gistRedoClearFollowRight (gistxlog.c:47-54) does its page work on
//     BLK_RESTORED as well as BLK_NEEDS_REDO, under the comment "we still update
//     the page even if it was restored from a full page image, because the
//     updated NSN is not included in the image". Restoring the image and
//     declaring the record applied yields a page with a stale NSN and a
//     still-set follow-right flag — a silently wrong index, which is the exact
//     failure mode S16.3 was written to eliminate.
//
// Sizes of the work being refused, measured at PG 18.3 (redo file LOC / opcode
// count): hash 1158 / 13, GIN 813 / 9, GiST 695 / 6, SP-GiST 1009 / 8,
// BRIN 367 / 6. See the deferral-ledger rows for the per-AM resume points.

// indexAMName maps an index-AM resource-manager id to the access-method name a
// user would type in `USING <am>`, which is the name worth putting in an error
// message. ok is false for every rmid that is not one of the five.
func indexAMName(rmid Rmgr) (string, bool) {
	switch rmid {
	case RmgrHash:
		return "hash", true
	case RmgrGin:
		return "gin", true
	case RmgrGist:
		return "gist", true
	case RmgrSPGist:
		return "spgist", true
	case RmgrBrin:
		return "brin", true
	default:
		return "", false
	}
}

// indexAMOpcode extracts the opcode from an index-AM record's xl_info.
//
// The mask is per-AM on purpose. Four of the five use the whole XLR_RMGR_INFO_MASK
// nibble-pair as the opcode, but BRIN steals the top bit: XLOG_BRIN_INIT_PAGE
// (0x80, brin_xlog.h:43) is a FLAG meaning "the target page is initialised by
// redo", ORed onto an opcode that is masked out with XLOG_BRIN_OPMASK (0x70,
// brin_xlog.h:38) before brin_redo's switch. Reporting 0xA0 instead of 0x20 for
// an init-page XLOG_BRIN_UPDATE would send whoever reads the message looking for
// an opcode that does not exist.
func indexAMOpcode(rmid Rmgr, info byte) byte {
	if rmid == RmgrBrin {
		return info & xlogBrinOpMask
	}
	return info & XLRRmgrInfoMask
}

const (
	// xlogBrinOpMask is XLOG_BRIN_OPMASK (brin_xlog.h:38).
	xlogBrinOpMask byte = 0x70
	// xlogBrinInitPage is XLOG_BRIN_INIT_PAGE (brin_xlog.h:43) — a flag ORed
	// onto the opcode, not an opcode.
	xlogBrinInitPage byte = 0x80
)

// indexAMUnsupportedError is the named refusal for one record. It reports the
// access method, the opcode, the LSN span and the target relation/block, so the
// message identifies both the feature that is missing and the object that
// triggered it.
func indexAMUnsupportedError(r Record, xlog *XLogDecodedRecord) error {
	am, ok := indexAMName(xlog.Header.Rmid)
	if !ok {
		return unsupportedDecodedXLogRecord(r)
	}
	info := xlog.Header.Info & XLRRmgrInfoMask
	extra := ""
	if xlog.Header.Rmid == RmgrBrin && info&xlogBrinInitPage != 0 {
		extra = " init_page"
	}
	return fmt.Errorf(
		"%w: %s index WAL: rmid=%d (%s) opcode=0x%02x%s lsn[%d,%d]%s: goopg has no %s redo, so this record cannot be replayed; drop or REINDEX the affected index on the PostgreSQL side (or finish recovery with PostgreSQL) before starting goopg on this data directory",
		ErrUnsupportedRecord, am, xlog.Header.Rmid, am,
		indexAMOpcode(xlog.Header.Rmid, xlog.Header.Info), extra,
		r.StartLSN, r.EndLSN, indexAMTargetSuffix(xlog), am)
}

// indexAMTargetSuffix renders " rel=<db>/<rel> blk=<n>" for the record's first
// block reference, or "" when it has none. An index-AM record essentially always
// has one; the empty case exists so a malformed record still produces a message
// rather than a panic.
func indexAMTargetSuffix(xlog *XLogDecodedRecord) string {
	if len(xlog.Blocks) == 0 {
		return ""
	}
	b := xlog.Blocks[0]
	return fmt.Sprintf(" rel=%d/%d/%d fork=%d blk=%d",
		b.Rel.TblOid, b.Rel.DBOid, b.Rel.RelOid, b.Rel.Fork, b.Block)
}

// indexAMSighting accumulates what the pre-flight scan learned about one AM.
type indexAMSighting struct {
	am       string
	count    int
	firstRec Record
	firstXL  *XLogDecodedRecord
}

// preflightIndexAMRecords scans the records replay is about to apply and, if any
// of them belong to an index AM goopg cannot replay, returns ONE error naming
// every distinct AM found — with a count and the first offending LSN per AM.
//
// Returning nil means only that no index-AM record is present; it is not a
// blanket "this stream is replayable". Every other refusal still happens
// per-record inside replayDecodedXLogRecord. This scan exists because the
// index-AM boundary is the one refusal an operator is expected to hit for a
// legitimate reason (their PG cluster has a GIN index), and learning it one
// restart at a time is the avoidable part.
func preflightIndexAMRecords(records []Record) error {
	byRmid := map[Rmgr]*indexAMSighting{}
	for _, r := range records {
		if r.XLog == nil {
			continue
		}
		am, ok := indexAMName(r.XLog.Header.Rmid)
		if !ok {
			continue
		}
		s := byRmid[r.XLog.Header.Rmid]
		if s == nil {
			byRmid[r.XLog.Header.Rmid] = &indexAMSighting{
				am: am, count: 1, firstRec: r, firstXL: r.XLog,
			}
			continue
		}
		s.count++
	}
	if len(byRmid) == 0 {
		return nil
	}

	rmids := make([]int, 0, len(byRmid))
	for rmid := range byRmid {
		rmids = append(rmids, int(rmid))
	}
	sort.Ints(rmids)

	parts := make([]string, 0, len(rmids))
	for _, rmid := range rmids {
		s := byRmid[Rmgr(rmid)]
		parts = append(parts, fmt.Sprintf("%s (rmid=%d, %d record(s), first at lsn[%d,%d]%s)",
			s.am, rmid, s.count, s.firstRec.StartLSN, s.firstRec.EndLSN,
			indexAMTargetSuffix(s.firstXL)))
	}
	return fmt.Errorf(
		"%w: this WAL stream contains index-AM records goopg has no redo for: %s; nothing has been replayed. Drop or REINDEX these indexes on the PostgreSQL side (or finish recovery with PostgreSQL) before starting goopg on this data directory",
		ErrUnsupportedRecord, strings.Join(parts, ", "))
}
