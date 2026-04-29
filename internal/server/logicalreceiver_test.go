package server

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// TestBuildStartLogicalReplicationCommandShape pins the wire-level
// SQL command string the subscriber sends to the publisher. Mirrors
// upstream libpq's pgoutput-bound walreceiver shape so an upstream
// publisher would parse the command identically.
func TestBuildStartLogicalReplicationCommandShape(t *testing.T) {
	got := buildStartLogicalReplicationCommand("logical1", 0xCAFE, 1, []string{"p1", "p2"})
	want := `START_REPLICATION SLOT logical1 LOGICAL 0/CAFE ("proto_version" '1', "publication_names" 'p1,p2')`
	if got != want {
		t.Errorf("cmd=%q\nwant=%q", got, want)
	}
}

// TestBuildStartLogicalReplicationCommandNoPublications: empty
// Publications drops the publication_names option entirely
// (mirrors libpq's behaviour for "all visible publications").
func TestBuildStartLogicalReplicationCommandNoPublications(t *testing.T) {
	got := buildStartLogicalReplicationCommand("s", 0, 1, nil)
	if !strings.Contains(got, `LOGICAL 0/0`) {
		t.Errorf("missing LOGICAL 0/0 in %q", got)
	}
	if strings.Contains(got, "publication_names") {
		t.Errorf("publication_names emitted with empty list: %q", got)
	}
}

// TestLogicalReceiverHandleCopyDataAppliesPgoutputAndAdvancesLSN
// pins the in-process flow `'w' frame → pgoutput decode →
// ApplyWorker → applyLSN advance on commit`. We construct a
// LogicalReceiver shell (no live conn) and drive its
// handleCopyData with a `'w'` payload carrying pgoutput bytes
// produced by the existing PgOutput encoder. After the C
// message lands, the receiver's applyLSN should equal the
// commit's commit_lsn.
func TestLogicalReceiverHandleCopyDataAppliesPgoutputAndAdvancesLSN(t *testing.T) {
	subDir := t.TempDir()
	subMgr := storage.NewManager(storage.ManagerConfig{DataDir: subDir})
	defer subMgr.Close()
	subPool, err := storage.NewPool(subMgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer subPool.Close()
	subCat := catalog.NewInMemory()
	if _, err := subCat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	subTxnMgr := mvcc.NewManager()
	apply := executor.NewApplyWorker(subCat, subPool, subTxnMgr)

	// Build the receiver shell. Conn is nil — handleCopyData
	// doesn't touch the network in the success path; sendStatus
	// would, but the test doesn't go through the ticker.
	rec := &LogicalReceiver{
		cfg: LogicalReceiverConfig{Apply: apply, StatusInterval: time.Second},
	}

	// Build a publisher's catalog mirror and emit B → R → I → C
	// for one row.
	pubCat := catalog.NewInMemory()
	pubTbl, _ := pubCat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	})
	snap := wal.BuildCatalogSnapshot(pubCat)

	body := encodeBodyV0([]any{42, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body)

	// Emit each pgoutput message individually and feed it
	// through handleCopyData wrapped as a 'w' CopyData payload.
	emit := func(t *testing.T, fn func(po *wal.PgOutput) error) {
		t.Helper()
		var buf bytes.Buffer
		po := wal.NewPgOutput(snap, &buf)
		if err := fn(po); err != nil {
			t.Fatal(err)
		}
		// Each emitted byte sequence is one pgoutput message;
		// wrap it in a 'w' CopyData payload.
		walPayload := protocol.EncodeWALData(0, 0xCAFE, time.Now().UTC(), buf.Bytes())
		if err := rec.handleCopyData(walPayload); err != nil {
			t.Fatal(err)
		}
	}

	emit(t, func(po *wal.PgOutput) error { return po.Begin(42, 0xCAFE) })
	emit(t, func(po *wal.PgOutput) error {
		return po.Change(wal.Change{
			Kind:     wal.ChangeInsert,
			Rel:      pubCat.RelFileNode(pubTbl),
			NewTuple: tuple,
		})
	})
	emit(t, func(po *wal.PgOutput) error { return po.Commit(42, 0xCAFE) })

	// applyLSN must have advanced to the commit LSN.
	if got := rec.ApplyLSN(); got != 0xCAFE {
		t.Errorf("ApplyLSN=%x want 0xCAFE", got)
	}

	// The local table now contains the inserted row; we don't
	// re-verify here since TestApplyWorkerInsertsRowFromPgoutput
	// already pins that. Force a clean shutdown.
	apply.SafeRollback()
}

// encodeBodyV0 / wrapAsHeapTuple are duplicated here from the
// executor and wal test packages because Go-test packages can't
// share helpers across packages.
func encodeBodyV0(values []any, types []string) []byte {
	var out []byte
	for i, v := range values {
		if v == nil {
			out = append(out, 1)
			continue
		}
		out = append(out, 0)
		switch types[i] {
		case "int4":
			var tmp [4]byte
			tmp[0] = byte(uint32(int32(v.(int))) >> 24)
			tmp[1] = byte(uint32(int32(v.(int))) >> 16)
			tmp[2] = byte(uint32(int32(v.(int))) >> 8)
			tmp[3] = byte(uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
		case "text":
			s := v.(string)
			ln := uint32(len(s))
			out = append(out, byte(ln>>24), byte(ln>>16), byte(ln>>8), byte(ln))
			out = append(out, []byte(s)...)
		}
	}
	return out
}

func wrapAsHeapTuple(t *testing.T, body []byte) []byte {
	t.Helper()
	tup, err := storage.NewHeapTuple(42, 0, body).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return tup
}

// silence: filepath is imported above but only some platforms /
// future tests use it. Keep the import explicit.
var _ = filepath.Separator
