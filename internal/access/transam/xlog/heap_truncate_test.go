package xlog

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// msgWriter keeps each PgOutput Write as one message.
type msgWriter struct{ msgs [][]byte }

func (w *msgWriter) Write(p []byte) (int, error) {
	w.msgs = append(w.msgs, append([]byte(nil), p...))
	return len(p), nil
}

// truncateOnlyFilter publishes TRUNCATE for the named tables only.
type truncateOnlyFilter map[string]bool

func (f truncateOnlyFilter) Allows(rel *RelationDef, k ChangeKind) bool {
	return k == ChangeTruncate && f[rel.Name]
}

// TestHeapTruncateCodec pins xl_heap_truncate's layout (heapam_xlog.h):
// dbId, nrelids, flags, padding to 12 bytes, then the relid array.
func TestHeapTruncateCodec(t *testing.T) {
	in := HeapTruncate{DBOid: 5, Flags: XLHTruncateCascade | XLHTruncateRestartSeqs, Relids: []uint32{16384, 16390}}
	rec, err := EncodeHeapTruncatePG(9, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) == 0 {
		t.Fatal("empty record")
	}
	main := []byte{5, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 0x00, 0x40, 0, 0, 0x06, 0x40, 0, 0}
	got, err := DecodeHeapTruncate(main)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(in) {
		t.Errorf("decoded %+v, want %+v", got, in)
	}
	if _, err := DecodeHeapTruncate(main[:15]); err == nil {
		t.Error("a short relid array decoded without error")
	}
}

// TestHeapTruncateDecodesToPgoutputTruncate: an XLOG_HEAP_TRUNCATE in the
// slot's database becomes, at commit, a Relation message per published
// relation and one `T` listing them with the option bits
// (pgoutput_truncate). A relation whose publication lacks truncate is left
// out, and a TRUNCATE in another database or an aborted one sends nothing.
func TestHeapTruncateDecodesToPgoutputTruncate(t *testing.T) {
	c := catalog.NewInMemory()
	cols := []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}
	a, err := c.CreateTable(parser.ObjectName{Name: "ta"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.CreateTable(parser.ObjectName{Name: "tb"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 1 << 16, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	app := func(payload []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	const db = 5
	app(EncodeHeapTruncatePG(7, HeapTruncate{DBOid: db, Flags: XLHTruncateRestartSeqs, Relids: []uint32{a.OID, b.OID}}))
	app(EncodeHeapTruncatePG(8, HeapTruncate{DBOid: 6, Relids: []uint32{a.OID}}))
	app(EncodeHeapTruncatePG(9, HeapTruncate{DBOid: db, Relids: []uint32{a.OID}}))
	app(EncodeXactAbortPG(9))
	app(EncodeXactCommitPG(8, false))
	app(EncodeXactCommitPG(7, false))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(dir, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	out := &msgWriter{}
	po := NewPgOutput(BuildCatalogSnapshot(c, nil), out)
	po.SetFilter(truncateOnlyFilter{"ta": true})
	d := NewDecoder(po)
	d.SetDatabase(db)
	for _, r := range recs {
		if err := Classify(d, r); err != nil {
			t.Fatal(err)
		}
	}
	var kinds []string
	var trunc *DecodedMessage
	for _, m := range out.msgs {
		dm, err := DecodeMessage(m)
		if err != nil {
			t.Fatalf("decode %x: %v", m, err)
		}
		kinds = append(kinds, string(rune(dm.Kind)))
		if dm.Kind == pgoTruncate {
			trunc = dm
		}
	}
	// xid 8's commit (other database) still frames an empty B/C pair, as
	// for any decoded transaction whose changes were all filtered.
	if got := fmt.Sprint(kinds); got != "[B C B R T C]" && got != "[B R T C]" {
		t.Fatalf("messages = %v", got)
	}
	if trunc == nil || fmt.Sprint(trunc.TruncateRels) != fmt.Sprint([]uint32{a.OID}) || trunc.TruncateOption != pgoTruncateRestartSeqs {
		t.Fatalf("truncate message = %+v", trunc)
	}
}
