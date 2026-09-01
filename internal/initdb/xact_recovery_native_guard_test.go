package initdb

// review/260831-2 IN-3 — the startup WAL scanners must not read Payload[0] as
// a goopg RecordKind on a record goopg did not emit.
//
// replayCLogFromWAL and walHasXactRecords both switched on the byte directly.
// A structurally real PG record (a checkpoint, say) carries raw struct bytes
// there, so its first byte can equal RecordKindXactCommit — which would stamp
// an unrelated XID committed during crash recovery, and force the
// crash-recovery branch on a cluster with no transaction history. The hazard
// is latent (PG-format records currently arrive with a nil Payload), which is
// exactly why it needs a test rather than a comment.

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
)

func TestNativeKindByteRejectsPGFormatRecords(t *testing.T) {
	payload := make([]byte, xlog.XactRecordSize)
	payload[0] = xlog.RecordKindXactCommit

	// A goopg-emitted record: the legacy shape (no decoded XLog header) is
	// trusted, as headerMatchesEmittedKind documents.
	if !nativeKindByte(xlog.Record{Payload: payload}, xlog.XactRecordSize) {
		t.Error("a native goopg xact record was rejected")
	}

	// A real PG record whose first MainData byte collides with the commit
	// kind. RM_XLOG is never the rmgr recordKindToRmgrInfo assigns to a
	// native kind, so the header cannot match.
	pg := xlog.Record{
		Payload: payload,
		XLog: &xlog.XLogDecodedRecord{
			Header:   xlog.XLogRecord{Rmid: xlog.RmgrXLog, Info: 0x10, XID: 4242},
			MainData: payload,
		},
	}
	if nativeKindByte(pg, xlog.XactRecordSize) {
		t.Error("a PG-format record was accepted as a native goopg kind tag")
	}

	// Short payloads never qualify, whatever the header says.
	if nativeKindByte(xlog.Record{Payload: payload[:1]}, xlog.XactRecordSize) {
		t.Error("a too-short payload was accepted")
	}
}
