package executor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestApplyWorkerOriginDiffersAndMissingConflicts pins the conflicts PG's
// apply worker LOGs and resolves (conflict.c ReportApplyConflict at LOG):
//   - update_origin_differs: with track_commit_timestamp on, an UPDATE that
//     locates a row last committed by another origin (here a local, origin 0,
//     transaction) logs "Updating the row that was modified locally in
//     transaction N at T." with the existing local row, the remote row and
//     the replica identity, and still applies;
//   - update_missing: an UPDATE that locates nothing logs "Could not find the
//     row to be updated." and is skipped.
//
// M0149-0061e / 0061j.
func TestApplyWorkerOriginDiffersAndMissingConflicts(t *testing.T) {
	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	rel := subCat.RelFileNode(subTbl)
	subCtx.TxnMgr.SetTrackCommitTimestamp(true)

	relMsg := &xlog.DecodedMessage{Kind: 'R', Relation: &xlog.DecodedRelation{
		OID: rel.RelOid, Name: "items",
		Columns: []xlog.DecodedAttr{{Name: "id", TypeOID: 23, Flags: 1}, {Name: "label", TypeOID: 25}},
	}}
	col := func(v string) xlog.DecodedColumn { return xlog.DecodedColumn{Status: 't', Bytes: []byte(v)} }
	drive := func(w *ApplyWorker, msgs ...*xlog.DecodedMessage) {
		t.Helper()
		for _, m := range msgs {
			if _, err := w.ApplyMessage(m); err != nil {
				t.Fatalf("%c: %v", m.Kind, err)
			}
		}
	}

	// A local (origin 0) transaction writes the row: a worker with no
	// subscription carries no replication origin.
	local := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer local.SafeRollback()
	drive(local,
		&xlog.DecodedMessage{Kind: 'B', XID: 100, CommitLSN: 0x100},
		relMsg,
		&xlog.DecodedMessage{Kind: 'I', RelOID: rel.RelOid, NewTuple: []xlog.DecodedColumn{col("7"), col("local")}},
		&xlog.DecodedMessage{Kind: 'C', CommitLSN: 0x100, EndLSN: 0x100})

	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("sub1", "x", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddSubscriptionRel("sub1", rel.RelOid); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{catalog.SubRelStateDataCopy, catalog.SubRelStateSyncDone, catalog.SubRelStateReady} {
		if _, err := ps.AdvanceSubscriptionRel("sub1", rel.RelOid, st, 1); err != nil {
			t.Fatal(err)
		}
	}
	var logBuf bytes.Buffer
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	w.SetSubscriptionContext(ps, "sub1")
	w.SetLogger(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer w.SafeRollback()
	drive(w,
		&xlog.DecodedMessage{Kind: 'B', XID: 200, CommitLSN: 0x200},
		relMsg,
		&xlog.DecodedMessage{Kind: 'U', RelOID: rel.RelOid, NewTuple: []xlog.DecodedColumn{col("7"), col("remote")}},
		&xlog.DecodedMessage{Kind: 'U', RelOID: rel.RelOid, NewTuple: []xlog.DecodedColumn{col("8"), col("nobody")}},
		&xlog.DecodedMessage{Kind: 'C', CommitLSN: 0x200, EndLSN: 0x200})

	logs := logBuf.String()
	for _, want := range []string{
		`conflict=update_origin_differs`,
		`Updating the row that was modified locally in transaction`,
		`Existing local row (7, local); remote row (7, remote); replica identity`,
		`conflict=update_missing`,
		`Could not find the row to be updated.`,
		`Remote row (8, nobody); replica identity`,
	} {
		if !strings.Contains(strings.ReplaceAll(logs, `\"`, `"`), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
}
