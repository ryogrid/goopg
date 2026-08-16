package xlog

import (
	"encoding/binary"
	"fmt"
)

// M0131-S22: decoding the variable-length body of a real PostgreSQL
// xl_xact_commit / xl_xact_abort record.
//
// goopg's own commit records are the minimal shape (xact_time only, plus an
// optional empty invals chunk), so until S22 nothing had to walk the chunk
// chain. A real PG's records do not have that luxury: every commit made by a
// transaction that used a SAVEPOINT carries an XACT_XINFO_HAS_SUBXACTS chunk
// listing the subtransaction XIDs, and upstream's xact_redo_commit stamps the
// WHOLE tree — TransactionIdCommitTree(xid, parsed->nsubxacts,
// parsed->subxacts) at postgres/src/backend/access/transam/xact.c:6182 — not
// just the top-level XID. A recovery that stamps only the top leaves every
// subtransaction's clog slot Unknown, and initdb.Open's MarkUnknownAsAborted
// sweep then stamps them ABORTED: the committed transaction's
// after-the-savepoint rows silently vanish.
//
// The parse mirrors ParseCommitRecord / ParseAbortRecord (xact.c:5673-5799).
// Chunks appear in a fixed order and each is int32-aligned by construction
// ("All the individual data chunks should be sized to multiples of
// sizeof(int)", xact.h:236-239), so a plain sequential walk is exact.

const (
	// xactXinfoHasDbinfo is XACT_XINFO_HAS_DBINFO (xact.h:188): an
	// xl_xact_dbinfo{dbId, tsId} chunk follows.
	xactXinfoHasDbinfo uint32 = 1 << 0
	// xactXinfoHasSubxacts is XACT_XINFO_HAS_SUBXACTS (xact.h:189): an
	// xl_xact_subxacts{nsubxacts, subxacts[]} chunk follows.
	xactXinfoHasSubxacts uint32 = 1 << 1

	// sizeOfXactDbinfo is sizeof(xl_xact_dbinfo): dbId(4) + tsId(4).
	sizeOfXactDbinfo = 8
	// minSizeOfXactSubxacts is MinSizeOfXactSubxacts — the nsubxacts count
	// that precedes the flexible TransactionId array.
	minSizeOfXactSubxacts = 4

	// maxParsedSubxacts bounds the array goopg will allocate from a decoded
	// record. Upstream caps a commit record's subxact list at
	// PGPROC_MAX_CACHED_SUBXIDS-driven batching plus the overflow list, but the
	// count word is attacker-visible bytes as far as this decoder is concerned;
	// the reader has already CRC-checked them, so an implausible count means a
	// record shape goopg misparsed rather than corruption. Refusing loudly is
	// better than allocating from it.
	maxParsedSubxacts = 1 << 20
)

// XactParsed is the subset of PG's xl_xact_parsed_commit / xl_xact_parsed_abort
// that goopg's crash-recovery clog pass consumes.
type XactParsed struct {
	// Xinfo is the xl_xact_xinfo word, or 0 when XLOG_XACT_HAS_INFO is clear.
	Xinfo uint32
	// Subxacts are the subtransaction XIDs of the committing/aborting
	// transaction, in the record's order. Empty when the transaction used no
	// subtransactions (the common case).
	Subxacts []uint32
}

// ParseXactRecord decodes the main data of an RM_XACT_ID commit or abort record
// far enough to recover the subtransaction list. `info` is the raw
// XLogRecord.Info byte (op bits + XLOG_XACT_HAS_INFO); `mainData` is the
// record's main-data chunk.
//
// It is the caller's job to have established that the record IS a commit or an
// abort (info&XlogXactOpMask): xl_xact_commit and xl_xact_abort share this
// prefix and chunk order, but XLOG_XACT_ASSIGNMENT and XLOG_XACT_INVALIDATIONS
// have entirely different bodies and must never be handed here.
//
// A body that ends mid-chunk is an error, not a silent truncation — a partially
// decoded subxact list would stamp some of a transaction's subtransactions and
// leave the rest to be swept ABORTED, which is exactly the torn-transaction
// outcome S22 exists to prevent.
func ParseXactRecord(info uint8, mainData []byte) (XactParsed, error) {
	var out XactParsed
	if len(mainData) < minSizeOfXactCommit {
		return out, fmt.Errorf("wal: xact record main data is %d bytes, want >= %d (xact_time)",
			len(mainData), minSizeOfXactCommit)
	}
	// xact_time (TimestampTz, 8 bytes) — goopg has no use for it.
	data := mainData[minSizeOfXactCommit:]

	if info&xlogXactHasInfo != 0 {
		if len(data) < 4 {
			return out, fmt.Errorf("wal: xact record sets XLOG_XACT_HAS_INFO but carries no xinfo word")
		}
		out.Xinfo = binary.LittleEndian.Uint32(data[:4])
		data = data[4:]
	}
	if out.Xinfo&xactXinfoHasDbinfo != 0 {
		if len(data) < sizeOfXactDbinfo {
			return out, fmt.Errorf("wal: xact record xinfo has DBINFO but only %d bytes remain", len(data))
		}
		data = data[sizeOfXactDbinfo:]
	}
	if out.Xinfo&xactXinfoHasSubxacts != 0 {
		if len(data) < minSizeOfXactSubxacts {
			return out, fmt.Errorf("wal: xact record xinfo has SUBXACTS but only %d bytes remain", len(data))
		}
		n := int(int32(binary.LittleEndian.Uint32(data[:4])))
		data = data[minSizeOfXactSubxacts:]
		if n < 0 || n > maxParsedSubxacts {
			return out, fmt.Errorf("wal: xact record declares %d subxacts, refusing to decode", n)
		}
		if len(data) < n*4 {
			return out, fmt.Errorf("wal: xact record declares %d subxacts but only %d bytes remain", n, len(data))
		}
		out.Subxacts = make([]uint32, n)
		for i := 0; i < n; i++ {
			out.Subxacts[i] = binary.LittleEndian.Uint32(data[i*4 : i*4+4])
		}
		// Later chunks (relfilelocators, stats items, invals, twophase, gid,
		// origin) are not consumed here; goopg's clog pass needs nothing beyond
		// the subxact array, and the relcache-init-file signal is read
		// separately by xactCommitCarriesInvals.
	}
	return out, nil
}
