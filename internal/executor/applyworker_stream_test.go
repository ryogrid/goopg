package executor

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestApplyWorkerAppliesStreamedTransactions pins M0149-0063b against PG
// 18.3's apply_handle_stream_start / _stop / _commit / _abort: the changes
// of a streamed transaction (sent in segments, each change carrying the
// xid after its kind byte, other transactions in between) are spooled and
// applied only at its stream commit, in one transaction; a stream abort
// discards them. The frames are goopg's own pgoutput stream output, read
// back the way the receiver reads them (DecodeMessageInStream with the
// stream state tracked across messages).
func TestApplyWorkerAppliesStreamedTransactions(t *testing.T) {
	_, pubCat, pubCleanup := newStorageFixture(t)
	defer pubCleanup()
	pubTbl, _ := pubCat.LookupTable(parser.ObjectName{Name: "items"})
	snap := xlog.BuildCatalogSnapshot(pubCat.(*catalog.InMemory), nil)
	rel := pubCat.RelFileNode(pubTbl)
	tuple := func(id int, label string) []byte {
		return wrapAsHeapTuple(t, encodeBodyV0([]any{id, label}, []string{"int4", "text"}), 2)
	}
	ins := func(id int, label string) xlog.Change {
		return xlog.Change{Kind: xlog.ChangeInsert, Rel: rel, NewTuple: tuple(id, label)}
	}

	// Each Write is one pgoutput message (the walsender sends one per
	// CopyData), so record them separately.
	fw := &frameWriter{}
	po := xlog.NewPgOutput(snap, fw)
	po.SetStreaming(true, false)
	for _, s := range []func() error{
		func() error { return po.StreamStart(500, true) },
		func() error { return po.Change(ins(1, "s1")) },
		func() error { return po.StreamStop() },
		// An ordinary transaction between the segments.
		func() error { return po.Begin(501, 0x150) },
		func() error { return po.Change(ins(9, "plain")) },
		func() error { return po.Commit(501, 0x150) },
		func() error { return po.StreamStart(500, false) },
		func() error { return po.Change(ins(2, "s2")) },
		func() error { return po.StreamStop() },
		// A second streamed transaction that aborts.
		func() error { return po.StreamStart(502, true) },
		func() error { return po.Change(ins(3, "aborted")) },
		func() error { return po.StreamStop() },
		func() error { return po.StreamAbort(502, 502, 0x170) },
		func() error { return po.StreamCommit(500, 0x200) },
	} {
		if err := s(); err != nil {
			t.Fatal(err)
		}
		fw.marks = append(fw.marks, len(fw.frames))
	}
	frames := fw.frames

	subCtx, subCat, subCleanup := newStorageFixture(t)
	defer subCleanup()
	subTbl, _ := subCat.LookupTable(parser.ObjectName{Name: "items"})
	w := NewApplyWorker(subCat, subCtx.Pool, subCtx.TxnMgr)
	defer w.SafeRollback()

	count := func() string {
		tx, _ := subCtx.TxnMgr.Begin(0)
		defer subCtx.TxnMgr.Rollback(tx)
		snap2, _ := subCtx.TxnMgr.SnapshotFor(tx)
		sc := NewContext()
		sc.Pool, sc.Catalog, sc.TxnMgr, sc.Tx, sc.Snap = subCtx.Pool, subCat, subCtx.TxnMgr, tx, snap2
		scan := newSeqScanOp(&optimizer.SeqScan{Table: subTbl})
		if err := scan.Open(sc); err != nil {
			t.Fatal(err)
		}
		defer scan.Close()
		rows, err := drainScan(scan)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]int, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, int(r[0].Int))
		}
		sort.Ints(ids)
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = strconv.Itoa(id)
		}
		return strings.Join(out, ",")
	}
	inStream := false
	var commitLSN uint64
	for i, f := range frames {
		// pgoutputMessageLength knows only the plain kinds, so each frame
		// is exactly one message here.
		m, err := xlog.DecodeMessageInStream(f, inStream, false)
		if err != nil {
			t.Fatalf("frame %d (%q): %v", i, f[0], err)
		}
		switch m.Kind {
		case 'S':
			inStream = true
		case 'E':
			inStream = false
		}
		lsn, err := w.ApplyMessage(m)
		if err != nil {
			t.Fatalf("ApplyMessage(%q): %v", m.Kind, err)
		}
		if lsn != 0 {
			commitLSN = lsn
		}
		if i == fw.marks[12]-1 { // after the abort, before the stream commit
			if got := count(); got != "9" {
				t.Errorf("before the stream commit: rows %q, want only the plain transaction's 9", got)
			}
		}
	}
	if got := count(); got != "1,2,9" {
		t.Errorf("after the stream commit: rows %q, want 1,2,9 (the aborted stream's 3 discarded)", got)
	}
	if commitLSN != 0x200 {
		t.Errorf("stream commit LSN = %#x, want 0x200", commitLSN)
	}
}

// frameWriter records each Write as one frame.
type frameWriter struct {
	frames [][]byte
	marks  []int // len(frames) after each step
}

func (f *frameWriter) Write(p []byte) (int, error) {
	f.frames = append(f.frames, append([]byte(nil), p...))
	return len(p), nil
}
