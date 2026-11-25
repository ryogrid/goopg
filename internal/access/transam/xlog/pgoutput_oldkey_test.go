package xlog

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgoutputOldKeyTuple pins logicalrep_write_update / _delete's old
// tuple (proto.c, M0149-0040d): under REPLICA IDENTITY DEFAULT the old row
// goes out as 'K' with only the identity columns valued and every other
// column NULL, so a subscriber locates the row by its key even when a
// non-key column differs locally; under REPLICA IDENTITY FULL it goes out as
// 'O' with every column.
func TestPgoutputOldKeyTuple(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "bid", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	}
	oldTuple := wrapAsHeapTuple(t, encodeBodyV0([]any{1, 2}, []string{"int4", "int4"}), 2)
	newTuple := wrapAsHeapTuple(t, encodeBodyV0([]any{6, 2}, []string{"int4", "int4"}), 2)

	for _, tc := range []struct {
		ident    byte
		wantKind byte
		wantBid  byte // DecodedColumn.Status of the non-key column
	}{
		{'d', 'K', 'n'},
		{'f', 'O', 't'},
	} {
		snap, rel := snapshotForRel(t, "tab", cols)
		def := snap.rels[rel.RelOid]
		if def == nil {
			for _, d := range snap.rels {
				def = d
			}
		}
		def.ReplIdent = tc.ident
		def.Columns[0].ReplIdent = true
		def.Columns[1].ReplIdent = tc.ident == 'f'

		for _, ch := range []Change{
			{Kind: ChangeUpdate, Rel: rel, OldTuple: oldTuple, NewTuple: newTuple},
			{Kind: ChangeDelete, Rel: rel, OldTuple: oldTuple},
		} {
			var buf bytes.Buffer
			po := NewPgOutput(snap, &buf)
			if err := po.Change(ch); err != nil {
				t.Fatal(err)
			}
			raw := buf.Bytes()
			op := byte('U')
			if ch.Kind == ChangeDelete {
				op = 'D'
			}
			idx := bytes.IndexByte(raw, op)
			if idx < 0 || idx+5 >= len(raw) {
				t.Fatalf("ident %c: no %c message: %x", tc.ident, op, raw)
			}
			if got := raw[idx+5]; got != tc.wantKind {
				t.Errorf("ident %c %c: old tuple kind %q, want %q", tc.ident, op, got, tc.wantKind)
			}
			msg, err := DecodeMessage(raw[idx:])
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			if len(msg.OldTuple) != 2 || string(msg.OldTuple[0].Bytes) != "1" || msg.OldTuple[1].Status != tc.wantBid {
				t.Errorf("ident %c %c: old tuple %+v, want id 1 and bid status %q", tc.ident, op, msg.OldTuple, tc.wantBid)
			}
		}
	}
}
