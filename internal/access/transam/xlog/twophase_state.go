package xlog

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// Two-phase state, as twophase.c lays it out. The same bytes are the main
// data of an XLOG_XACT_PREPARE record (EndPrepare registers the state
// buffer) and, followed by a CRC-32C, the content of a pg_twophase/<XID>
// file (RecreateTwoPhaseFile). Every segment is MAXALIGN'd (8 bytes):
//
//  1. TwoPhaseFileHeader (xl_xact_prepare, 72 bytes)
//  2. the GID, NUL-terminated
//  3. the committed subtransaction XIDs
//  4. RelFileLocators to delete on commit, then on abort
//  5. dropped-stats items on commit, then on abort
//  6. invalidation messages
//  7. resource-manager records (TwoPhaseRecordOnDisk + data), ending with
//     the TWOPHASE_RM_END_ID sentinel
//
// M0149-0015a.

// TwoPhaseMagic is TWOPHASE_MAGIC, the state's format identifier.
const TwoPhaseMagic uint32 = 0x57F94534

// twoPhaseHeaderSize is sizeof(xl_xact_prepare).
const twoPhaseHeaderSize = 72

// twoPhaseRecordOnDiskSize is sizeof(TwoPhaseRecordOnDisk): uint32 len,
// uint8 rmid, (pad), uint16 info.
const twoPhaseRecordOnDiskSize = 8

// TwoPhaseState is the part of a prepared transaction's state goopg writes
// and reads back.
type TwoPhaseState struct {
	XID        storage.TransactionID
	Database   uint32
	PreparedAt int64 // TimestampTz, µs since 2000-01-01
	Owner      uint32
	GID        string
	Subxacts   []storage.TransactionID
	CommitRels []storage.RelFileNode // dropped if the transaction commits
	AbortRels  []storage.RelFileNode // dropped if it aborts (relations it created)
}

func maxAlign8(n int) int { return (n + 7) &^ 7 }

// appendAligned appends b and zero-pads to the next 8-byte boundary, as
// save_state_data does.
func appendAligned(buf, b []byte) []byte {
	buf = append(buf, b...)
	for len(buf)%8 != 0 {
		buf = append(buf, 0)
	}
	return buf
}

func relFileLocatorBytes(rels []storage.RelFileNode) []byte {
	out := make([]byte, 0, 12*len(rels))
	for _, r := range rels {
		spc := r.TblOid
		switch {
		case r.DBOid == 0:
			spc = pgGlobalTableSpaceOID
		case spc == 0:
			spc = pgDefaultTableSpaceOID
		}
		out = binary.LittleEndian.AppendUint32(out, spc)
		out = binary.LittleEndian.AppendUint32(out, storage.StorageDBOid(r.DBOid))
		out = binary.LittleEndian.AppendUint32(out, r.RelOid)
	}
	return out
}

// EncodeTwoPhaseState is StartPrepare + EndPrepare's state buffer, without
// the trailing CRC: total_len counts it, as EndPrepare sets
// hdr->total_len = records.total_len + sizeof(pg_crc32c). goopg registers no
// resource-manager records (locks, pgstat, multixact, predicate locks) —
// only the end sentinel.
func EncodeTwoPhaseState(st TwoPhaseState) []byte {
	gid := append([]byte(st.GID), 0)
	hdr := make([]byte, twoPhaseHeaderSize)
	le := binary.LittleEndian
	le.PutUint32(hdr[0:], TwoPhaseMagic)
	// total_len at [4:8] is filled in below.
	le.PutUint32(hdr[8:], uint32(st.XID))
	le.PutUint32(hdr[12:], st.Database)
	le.PutUint64(hdr[16:], uint64(st.PreparedAt))
	le.PutUint32(hdr[24:], st.Owner)
	le.PutUint32(hdr[28:], uint32(int32(len(st.Subxacts))))
	le.PutUint32(hdr[32:], uint32(int32(len(st.CommitRels))))
	le.PutUint32(hdr[36:], uint32(int32(len(st.AbortRels))))
	// ncommitstats, nabortstats, ninvalmsgs = 0; initfileinval = false.
	le.PutUint16(hdr[54:], uint16(len(gid)))
	// origin_lsn, origin_timestamp = 0 (no replication origin).

	buf := appendAligned(nil, hdr)
	buf = appendAligned(buf, gid)
	if len(st.Subxacts) > 0 {
		sub := make([]byte, 0, 4*len(st.Subxacts))
		for _, x := range st.Subxacts {
			sub = le.AppendUint32(sub, uint32(x))
		}
		buf = appendAligned(buf, sub)
	}
	if len(st.CommitRels) > 0 {
		buf = appendAligned(buf, relFileLocatorBytes(st.CommitRels))
	}
	if len(st.AbortRels) > 0 {
		buf = appendAligned(buf, relFileLocatorBytes(st.AbortRels))
	}
	// RegisterTwoPhaseRecord(TWOPHASE_RM_END_ID, 0, NULL, 0): len 0,
	// rmid TWOPHASE_RM_END_ID (0), info 0.
	buf = appendAligned(buf, make([]byte, twoPhaseRecordOnDiskSize))
	le.PutUint32(buf[4:], uint32(len(buf)+4))
	return buf
}

// ParsedTwoPhaseState is what ParseTwoPhaseState reads back.
type ParsedTwoPhaseState struct {
	XID        storage.TransactionID
	Database   uint32
	PreparedAt int64
	Owner      uint32
	GID        string
	Subxacts   []storage.TransactionID
	CommitRels []XactRelFileLocator
	AbortRels  []XactRelFileLocator
	TotalLen   uint32
}

// ParseTwoPhaseState decodes a two-phase state buffer — an XLOG_XACT_PREPARE
// record's main data, or a pg_twophase file without its CRC — as
// ProcessTwoPhaseBuffer / PrepareRedoAdd read it. Resource-manager records
// after the fixed part are not interpreted.
func ParseTwoPhaseState(buf []byte) (ParsedTwoPhaseState, error) {
	var out ParsedTwoPhaseState
	le := binary.LittleEndian
	if len(buf) < twoPhaseHeaderSize {
		return out, fmt.Errorf("wal: two-phase state is %d bytes, want >= %d", len(buf), twoPhaseHeaderSize)
	}
	if m := le.Uint32(buf[0:]); m != TwoPhaseMagic {
		return out, fmt.Errorf("wal: two-phase state has magic %#x, want %#x", m, TwoPhaseMagic)
	}
	out.TotalLen = le.Uint32(buf[4:])
	out.XID = storage.TransactionID(le.Uint32(buf[8:]))
	out.Database = le.Uint32(buf[12:])
	out.PreparedAt = int64(le.Uint64(buf[16:]))
	out.Owner = le.Uint32(buf[24:])
	nsub := int(int32(le.Uint32(buf[28:])))
	ncommit := int(int32(le.Uint32(buf[32:])))
	nabort := int(int32(le.Uint32(buf[36:])))
	ncommitStats := int(int32(le.Uint32(buf[40:])))
	nabortStats := int(int32(le.Uint32(buf[44:])))
	ninval := int(int32(le.Uint32(buf[48:])))
	gidlen := int(le.Uint16(buf[54:]))
	if nsub < 0 || ncommit < 0 || nabort < 0 || ncommitStats < 0 || nabortStats < 0 || ninval < 0 {
		return out, fmt.Errorf("wal: two-phase state has a negative count")
	}
	off := twoPhaseHeaderSize
	take := func(n int) ([]byte, error) {
		if off+n > len(buf) {
			return nil, fmt.Errorf("wal: two-phase state truncated at offset %d (need %d bytes)", off, n)
		}
		b := buf[off : off+n]
		off += maxAlign8(n)
		return b, nil
	}
	gid, err := take(gidlen)
	if err != nil {
		return out, err
	}
	if gidlen > 0 && gid[gidlen-1] == 0 {
		gid = gid[:gidlen-1]
	}
	out.GID = string(gid)
	if nsub > 0 {
		b, err := take(4 * nsub)
		if err != nil {
			return out, err
		}
		for i := range nsub {
			out.Subxacts = append(out.Subxacts, storage.TransactionID(le.Uint32(b[4*i:])))
		}
	}
	rels := func(n int) ([]XactRelFileLocator, error) {
		if n == 0 {
			return nil, nil
		}
		b, err := take(12 * n)
		if err != nil {
			return nil, err
		}
		r := make([]XactRelFileLocator, n)
		for i := range r {
			r[i] = XactRelFileLocator{SpcOid: le.Uint32(b[12*i:]), DbOid: le.Uint32(b[12*i+4:]), RelNumber: le.Uint32(b[12*i+8:])}
		}
		return r, nil
	}
	if out.CommitRels, err = rels(ncommit); err != nil {
		return out, err
	}
	if out.AbortRels, err = rels(nabort); err != nil {
		return out, err
	}
	return out, nil
}

// EncodeXactPreparePG builds the XLOG_XACT_PREPARE record for a two-phase
// state buffer. The header xid is the prepared transaction's own: PREPARE
// TRANSACTION writes it as that transaction.
func EncodeXactPreparePG(xid storage.TransactionID, state []byte) ([]byte, error) {
	return framedAssemble(RmgrXact, xlogXactPrepare, uint32(xid), state, nil)
}

// EncodeXactCommitPreparedPGAt is RecordTransactionCommitPrepared's record:
// an xl_xact_commit with XLOG_XACT_COMMIT_PREPARED and the prepared xid in
// the xl_xact_twophase chunk (XACT_XINFO_HAS_TWOPHASE). The finishing
// backend's own transaction has no xid, so the header xid is 0.
func EncodeXactCommitPreparedPGAt(xactTime int64, xid storage.TransactionID, subxacts []storage.TransactionID, rels []storage.RelFileNode, hasInvals bool) ([]byte, error) {
	info := xlogXactCommitPrepared | xlogXactHasInfo
	xinfo := uint32(xactXinfoHasTwophase)
	if hasInvals {
		xinfo |= xactXinfoHasInvals
	}
	if len(subxacts) > 0 {
		xinfo |= xactXinfoHasSubxacts
	}
	if len(rels) > 0 {
		xinfo |= xactXinfoHasRelfilelocators
	}
	mainData := binary.LittleEndian.AppendUint64(nil, uint64(xactTime))
	mainData = binary.LittleEndian.AppendUint32(mainData, xinfo)
	mainData = appendXactSubxacts(mainData, subxacts)
	mainData = appendXactRelFileLocators(mainData, rels)
	if hasInvals {
		mainData = binary.LittleEndian.AppendUint32(mainData, 0)
	}
	mainData = binary.LittleEndian.AppendUint32(mainData, uint32(xid)) // xl_xact_twophase
	return framedAssemble(RmgrXact, info, 0, mainData, nil)
}

// EncodeXactAbortPreparedPGAt is RecordTransactionAbortPrepared's record:
// XLOG_XACT_ABORT_PREPARED with the prepared xid in xl_xact_twophase.
func EncodeXactAbortPreparedPGAt(xactTime int64, xid storage.TransactionID, subxacts []storage.TransactionID, rels []storage.RelFileNode) ([]byte, error) {
	info := xlogXactAbortPrepared | xlogXactHasInfo
	xinfo := uint32(xactXinfoHasTwophase)
	if len(subxacts) > 0 {
		xinfo |= xactXinfoHasSubxacts
	}
	if len(rels) > 0 {
		xinfo |= xactXinfoHasRelfilelocators
	}
	mainData := binary.LittleEndian.AppendUint64(nil, uint64(xactTime))
	mainData = binary.LittleEndian.AppendUint32(mainData, xinfo)
	mainData = appendXactSubxacts(mainData, subxacts)
	mainData = appendXactRelFileLocators(mainData, rels)
	mainData = binary.LittleEndian.AppendUint32(mainData, uint32(xid))
	return framedAssemble(RmgrXact, info, 0, mainData, nil)
}
