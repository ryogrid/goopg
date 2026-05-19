package wal

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestPgoutputDecoderRoundTripBegin pins the encoder/decoder
// symmetry for the `B` message: a freshly encoded Begin parses
// back to the same xid + commit LSN.
func TestPgoutputDecoderRoundTripBegin(t *testing.T) {
	var buf bytes.Buffer
	po := NewPgOutput(&CatalogSnapshot{}, &buf)
	if err := po.Begin(42, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMessage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != pgoBegin {
		t.Errorf("Kind=%q want B", m.Kind)
	}
	if m.XID != 42 {
		t.Errorf("XID=%d want 42", m.XID)
	}
	if m.CommitLSN != 0xCAFE {
		t.Errorf("CommitLSN=%x want 0xCAFE", m.CommitLSN)
	}
}

// TestPgoutputDecoderRoundTripCommit pins `C` round-trip.
func TestPgoutputDecoderRoundTripCommit(t *testing.T) {
	var buf bytes.Buffer
	po := NewPgOutput(&CatalogSnapshot{}, &buf)
	if err := po.Commit(99, 0x1234); err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMessage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != pgoCommit {
		t.Errorf("Kind=%q want C", m.Kind)
	}
	if m.CommitLSN != 0x1234 {
		t.Errorf("CommitLSN=%x", m.CommitLSN)
	}
	if m.EndLSN != 0x1234 {
		t.Errorf("EndLSN=%x", m.EndLSN)
	}
}

// TestPgoutputDecoderRoundTripRelationAndInsert: end-to-end
// the encoder emits `R` followed by `I`; the decoder splits them
// and the tuple body round-trips byte-for-byte.
func TestPgoutputDecoderRoundTripRelationAndInsert(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := BuildCatalogSnapshot(c)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	body := encodeBodyV0([]any{42, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: c.RelFileNode(tbl), NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}

	stream := buf.Bytes()
	// First message: 'R'.
	relM, err := decodeOne(t, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if relM.Kind != pgoRelation {
		t.Fatalf("first kind=%q want R", relM.Kind)
	}
	if relM.Relation.Name != "items" {
		t.Errorf("rel name=%q", relM.Relation.Name)
	}
	if len(relM.Relation.Columns) != 2 ||
		relM.Relation.Columns[0].Name != "id" ||
		relM.Relation.Columns[1].Name != "label" {
		t.Errorf("rel columns=%+v", relM.Relation.Columns)
	}

	// Second message: 'I'.
	insM, err := decodeOne(t, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if insM.Kind != pgoInsert {
		t.Fatalf("second kind=%q want I", insM.Kind)
	}
	if insM.RelOID != tbl.OID {
		t.Errorf("RelOID=%d want %d", insM.RelOID, tbl.OID)
	}
	if len(insM.NewTuple) != 2 {
		t.Fatalf("NewTuple len=%d want 2", len(insM.NewTuple))
	}
	wantCols := []DecodedColumn{
		{Status: pgoColText, Bytes: []byte("42")},
		{Status: pgoColText, Bytes: []byte("alpha")},
	}
	if !reflect.DeepEqual(insM.NewTuple, wantCols) {
		t.Errorf("NewTuple=%+v want %+v", insM.NewTuple, wantCols)
	}
}

// TestPgoutputDecoderRoundTripDelete pins the empty-K-body
// shape v0 emits for DELETE.
func TestPgoutputDecoderRoundTripDelete(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, _ := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	})
	snap := BuildCatalogSnapshot(c)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	if err := po.Change(Change{Kind: ChangeDelete, Rel: c.RelFileNode(tbl)}); err != nil {
		t.Fatal(err)
	}

	stream := buf.Bytes()
	relM, _ := decodeOne(t, &stream)
	if relM.Kind != pgoRelation {
		t.Fatal("expected R first")
	}
	delM, err := decodeOne(t, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if delM.Kind != pgoDelete {
		t.Fatalf("kind=%q want D", delM.Kind)
	}
	if len(delM.OldTuple) != 0 {
		t.Errorf("OldTuple len=%d want 0 (empty K body)", len(delM.OldTuple))
	}
}

// TestPgoutputDecoderRejectsTruncated: short payloads surface as
// ErrTruncatedMessage.
func TestPgoutputDecoderRejectsTruncated(t *testing.T) {
	if _, err := DecodeMessage(nil); err == nil {
		t.Errorf("nil payload accepted")
	}
	if _, err := DecodeMessage([]byte{pgoBegin}); err == nil {
		t.Errorf("kind-only payload accepted")
	}
}


// TestPgoutputDecoderTruncateMessage hand-crafts a pgoutput 'T'
// frame and pins the parsed fields. M0103-0007 rung 9: goopg's
// PgOutput encoder does not (yet) emit TRUNCATE, so we synthesise
// the upstream wire shape directly. The byte layout mirrors
// `logicalrep_write_truncate` in
// postgres/src/backend/replication/logical/proto.c:
//   'T' | nrelids(4 BE) | option_bits(1) | relid_1(4 BE) … relid_n(4 BE)
//
// The option_bits byte is the bitwise OR of TRUNCATE_CASCADE (0x01)
// and TRUNCATE_RESTART_SEQS (0x02); we exercise both so the round
// trip pins both bit positions independently.
func TestPgoutputDecoderTruncateMessage(t *testing.T) {
	build := func(opt byte, oids []uint32) []byte {
		out := []byte{pgoTruncate}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(oids)))
		out = append(out, n[:]...)
		out = append(out, opt)
		for _, oid := range oids {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], oid)
			out = append(out, b[:]...)
		}
		return out
	}

	t.Run("single_relation_no_flags", func(t *testing.T) {
		m, err := DecodeMessage(build(0, []uint32{16384}))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m.Kind != pgoTruncate {
			t.Errorf("Kind=%q want T", m.Kind)
		}
		if got := m.TruncateRels; len(got) != 1 || got[0] != 16384 {
			t.Errorf("TruncateRels=%v want [16384]", got)
		}
		if m.TruncateOption != 0 {
			t.Errorf("TruncateOption=%d want 0", m.TruncateOption)
		}
	})

	t.Run("multi_relation_cascade_restart", func(t *testing.T) {
		opts := pgoTruncateCascade | pgoTruncateRestartSeqs
		m, err := DecodeMessage(build(opts, []uint32{16384, 16385, 16386}))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := m.TruncateRels; len(got) != 3 ||
			got[0] != 16384 || got[1] != 16385 || got[2] != 16386 {
			t.Errorf("TruncateRels=%v want [16384 16385 16386]", got)
		}
		if m.TruncateOption != opts {
			t.Errorf("TruncateOption=0x%x want 0x%x", m.TruncateOption, opts)
		}
	})

	t.Run("empty_relid_list", func(t *testing.T) {
		// Defensive: nrelids=0 must not crash the decoder.
		// The publisher never emits this in practice but the
		// apply worker tolerates it as a no-op.
		m, err := DecodeMessage(build(0, nil))
		if err != nil {
			t.Fatalf("decode empty: %v", err)
		}
		if len(m.TruncateRels) != 0 {
			t.Errorf("TruncateRels=%v want empty", m.TruncateRels)
		}
	})

	t.Run("truncated_after_option", func(t *testing.T) {
		// nrelids=2 but only one relid follows — must error.
		payload := []byte{pgoTruncate, 0, 0, 0, 2, 0, 0, 0, 0, 16}
		if _, err := DecodeMessage(payload); err == nil {
			t.Errorf("decode short payload: nil err want error")
		}
	})
}

// decodeOne parses one message off the head of *stream and
// advances the slice past it. The pgoutput format isn't framed
// at the byte level — callers know per-kind sizes — so v0
// derives the consumed length from the kind. Only used by tests.
func decodeOne(t *testing.T, stream *[]byte) (*DecodedMessage, error) {
	t.Helper()
	if len(*stream) == 0 {
		return nil, nil
	}
	kind := (*stream)[0]
	consumed, err := messageLength(*stream, kind)
	if err != nil {
		return nil, err
	}
	m, err := DecodeMessage((*stream)[:consumed])
	if err != nil {
		return nil, err
	}
	*stream = (*stream)[consumed:]
	return m, nil
}

// messageLength walks the payload to compute its total length per
// kind. A real apply worker reads the stream framed inside libpq
// CopyData messages where each message has a length prefix; this
// helper exists only so tests can chain encoder output through
// the decoder without that framing.
func messageLength(buf []byte, kind byte) (int, error) {
	switch kind {
	case pgoBegin:
		return 21, nil
	case pgoCommit:
		return 26, nil
	case pgoRelation:
		// kind(1) | oid(4) | nspname\0 | relname\0 | replident(1)
		// | natts(2) | per-attr (flags(1) | name\0 | typoid(4) |
		// typmod(4)).
		off := 1 + 4
		// nspname
		i := off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1
		// relname
		i = off
		for i < len(buf) && buf[i] != 0 {
			i++
		}
		off = i + 1 + 1 // + replident
		if off+2 > len(buf) {
			return 0, ErrTruncatedMessage
		}
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			off++ // flags
			i = off
			for i < len(buf) && buf[i] != 0 {
				i++
			}
			off = i + 1 + 4 + 4 // name term + typoid + typmod
		}
		return off, nil
	case pgoInsert, pgoDelete:
		// kind(1) | oid(4) | action(1) | nliveatts(2) | per-col …
		off := 1 + 4 + 1
		if off+2 > len(buf) {
			return 0, ErrTruncatedMessage
		}
		natts := int(buf[off])<<8 | int(buf[off+1])
		off += 2
		for j := 0; j < natts; j++ {
			if off >= len(buf) {
				return 0, ErrTruncatedMessage
			}
			st := buf[off]
			off++
			if st == pgoColText {
				if off+4 > len(buf) {
					return 0, ErrTruncatedMessage
				}
				ln := int(buf[off])<<24 | int(buf[off+1])<<16 | int(buf[off+2])<<8 | int(buf[off+3])
				off += 4 + ln
			}
		}
		return off, nil
	}
	return 0, ErrTruncatedMessage
}

// silence unused-import warning when storage isn't referenced by
// every test in the file (used in encoder helpers via wrapAsHeapTuple
// above; keep the import explicit here).
var _ = storage.InvalidTransactionID
