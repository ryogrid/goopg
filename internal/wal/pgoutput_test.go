package wal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// snapshotForRel builds a one-table CatalogSnapshot for tests.
func snapshotForRel(t *testing.T, name string, cols []catalog.Column) (*CatalogSnapshot, storage.RelFileNode) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
	if err != nil {
		t.Fatal(err)
	}
	return BuildCatalogSnapshot(c, nil), c.RelFileNode(tbl)
}

// encodeBodyV0 mirrors the executor codec's null-flag-then-value
// frame so tests can construct on-disk bytes the plugin will
// decode. Kept self-contained to avoid pulling in the executor
// package.
// encodeBodyV0 builds a PG-physical column-data body (M0111-0002: single
// on-disk format). Test helper supporting non-NULL int4/int8/text — all the
// pgoutput tests need. Alignment mirrors executor.physicalPGTypeAlign
// (int4/text=4, int8=8); pair it with wrapAsHeapTuple to stamp natts.
func encodeBodyV0(values []any, types []string) []byte {
	var out []byte
	alignTo := func(a int) {
		for len(out)%a != 0 {
			out = append(out, 0)
		}
	}
	for i, v := range values {
		switch types[i] {
		case "int4":
			alignTo(4)
			var tmp [4]byte
			binary.LittleEndian.PutUint32(tmp[:], uint32(int32(v.(int))))
			out = append(out, tmp[:]...)
		case "int8":
			alignTo(8)
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], uint64(v.(int64)))
			out = append(out, tmp[:]...)
		case "text":
			alignTo(4)
			s := v.(string)
			total := len(s) + 1 // PG short varlena: header byte included in len
			out = append(out, byte((total<<1)|0x01))
			out = append(out, []byte(s)...)
		}
	}
	return out
}

func wrapAsHeapTuple(t *testing.T, body []byte, natts int) []byte {
	t.Helper()
	tup := storage.NewHeapTuple(42, 0, body)
	tup.Header.SetNatts(natts)
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestPgOutputBeginEmitsCanonicalShape pins the M0008 / 0008-0002
// `B` message wire shape: kind(1) | final_lsn(8) | commit_time(8)
// | xid(4) = 21 bytes.
func TestPgOutputBeginEmitsCanonicalShape(t *testing.T) {
	var buf bytes.Buffer
	po := NewPgOutput(&CatalogSnapshot{}, &buf)
	if err := po.Begin(42, 0x0123456789ABCDEF); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if len(out) != 21 {
		t.Fatalf("len=%d want 21", len(out))
	}
	if out[0] != 'B' {
		t.Errorf("kind=%q want B", out[0])
	}
	if got := binary.BigEndian.Uint64(out[1:9]); got != 0x0123456789ABCDEF {
		t.Errorf("final_lsn=%x want 0x0123456789ABCDEF", got)
	}
	if got := binary.BigEndian.Uint32(out[17:21]); got != 42 {
		t.Errorf("xid=%d want 42", got)
	}
}

// TestPgOutputCommitEmitsCanonicalShape pins the `C` message
// wire shape: kind(1) | flags(1)=0 | commit_lsn(8) | end_lsn(8)
// | commit_time(8) = 26 bytes.
func TestPgOutputCommitEmitsCanonicalShape(t *testing.T) {
	var buf bytes.Buffer
	po := NewPgOutput(&CatalogSnapshot{}, &buf)
	if err := po.Commit(99, 0xCAFE); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if len(out) != 26 {
		t.Fatalf("len=%d want 26", len(out))
	}
	if out[0] != 'C' || out[1] != 0 {
		t.Errorf("header=% x want C 00", out[:2])
	}
	if got := binary.BigEndian.Uint64(out[2:10]); got != 0xCAFE {
		t.Errorf("commit_lsn=%x want 0xCAFE", got)
	}
	if got := binary.BigEndian.Uint64(out[10:18]); got != 0xCAFE {
		t.Errorf("end_lsn=%x want 0xCAFE", got)
	}
}

// TestPgOutputInsertEmitsRelationOnceThenInsert: the first
// Change touching a relation emits `R` then `I`; the second
// Change against the same relation emits only `I`. Mirrors
// upstream's relsynced behaviour.
func TestPgOutputInsertEmitsRelationOnceThenInsert(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	body := encodeBodyV0([]any{1, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body, 2)

	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if out[0] != 'R' {
		t.Fatalf("first byte=%q want R (relation emitted before insert)", out[0])
	}
	// Locate the I message after the R; not byte-precise (rel
	// payload length varies) but the kind sequence is the
	// invariant we want to pin.
	if !bytes.Contains(out, []byte{'I'}) {
		t.Errorf("missing I message after R: %x", out)
	}

	buf.Reset()
	body2 := encodeBodyV0([]any{2, "beta"}, []string{"int4", "text"})
	tup2 := wrapAsHeapTuple(t, body2, 2)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tup2}); err != nil {
		t.Fatal(err)
	}
	out2 := buf.Bytes()
	if out2[0] != 'I' {
		t.Errorf("second-change first byte=%q want I (R already emitted)", out2[0])
	}
	if bytes.Contains(out2, []byte{'R'}) {
		t.Errorf("second change re-emitted R: %x", out2)
	}
}

// TestPgOutputInsertEncodesIntAndText pins the tuple body
// decoding: an int4=1 + text="alpha" row produces an `I` whose
// tuple body has nliveatts=2, two `t`-status columns with the
// canonical text bytes "1" and "alpha".
func TestPgOutputInsertEncodesIntAndText(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	body := encodeBodyV0([]any{42, "alpha"}, []string{"int4", "text"})
	tuple := wrapAsHeapTuple(t, body, 2)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}

	// Skip the `R` message and find the `I`. The first byte is
	// 'R'; scan forward to the first 'I'.
	idx := bytes.IndexByte(buf.Bytes(), 'I')
	if idx < 0 {
		t.Fatal("no I message in output")
	}
	insert := buf.Bytes()[idx:]
	// I framing: kind(1) | rel_oid(4) | 'N' | nliveatts(2) | …
	if insert[5] != 'N' {
		t.Errorf("insert action byte=%q want N", insert[5])
	}
	nliveatts := binary.BigEndian.Uint16(insert[6:8])
	if nliveatts != 2 {
		t.Fatalf("nliveatts=%d want 2", nliveatts)
	}
	// First column: 't' | len(4) | "1"
	off := 8
	if insert[off] != 't' {
		t.Errorf("col[0].status=%q want t", insert[off])
	}
	col0len := binary.BigEndian.Uint32(insert[off+1 : off+5])
	col0val := string(insert[off+5 : off+5+int(col0len)])
	if col0val != "42" {
		t.Errorf("col[0] value=%q want 42", col0val)
	}
	off += 5 + int(col0len)
	// Second column: 't' | len(4) | "alpha"
	if insert[off] != 't' {
		t.Errorf("col[1].status=%q want t", insert[off])
	}
	col1len := binary.BigEndian.Uint32(insert[off+1 : off+5])
	col1val := string(insert[off+5 : off+5+int(col1len)])
	if col1val != "alpha" {
		t.Errorf("col[1] value=%q want alpha", col1val)
	}
}

// TestPgOutputDeleteEmitsKMarker pins the `D` message: kind(1)
// | rel_oid(4) | 'K' | nliveatts(2)=0. v0's HeapDelete record
// carries no pre-image; the wire shape stays well-formed via a
// 0-attribute tuple body. The apply worker resolves the row by
// (rel, block, slot) lookup.
func TestPgOutputDeleteEmitsKMarker(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	if err := po.Change(Change{Kind: ChangeDelete, Rel: rel}); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	idx := bytes.IndexByte(out, 'D')
	if idx < 0 {
		t.Fatal("no D message in output")
	}
	d := out[idx:]
	if len(d) < 8 {
		t.Fatalf("D message too short: %d bytes", len(d))
	}
	if d[5] != 'K' {
		t.Errorf("delete action byte=%q want K", d[5])
	}
	nliveatts := binary.BigEndian.Uint16(d[6:8])
	if nliveatts != 0 {
		t.Errorf("delete nliveatts=%d want 0 (no pre-image in v0)", nliveatts)
	}
}

// TestPgOutputSkipsUnknownRelation: a Change against a relation
// not in the snapshot is silently dropped. Mirrors v0's
// "snapshot-time relations only" contract from
// docs/design/0008-0001-logical-decoding-pipeline.md.
func TestPgOutputSkipsUnknownRelation(t *testing.T) {
	var buf bytes.Buffer
	po := NewPgOutput(&CatalogSnapshot{}, &buf)
	if err := po.Change(Change{
		Kind: ChangeInsert,
		Rel:  storage.RelFileNode{DBOid: 1, RelOid: 99999},
	}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("unknown rel produced output: %x", buf.Bytes())
	}
}

// alwaysFalseFilter rejects every change. Used to pin
// PgOutput.Change's filter contract.
type alwaysFalseFilter struct{}

func (alwaysFalseFilter) Allows(_ *RelationDef, _ ChangeKind) bool { return false }

// onlyInsertFilter admits inserts on every relation, rejects
// other change kinds.
type onlyInsertFilter struct{}

func (onlyInsertFilter) Allows(_ *RelationDef, k ChangeKind) bool {
	return k == ChangeInsert
}

// TestPgOutputFilterSuppressesEmission pins the M0008 /
// 0008-0003 publication-membership contract: when SetFilter
// rejects a change, PgOutput emits nothing — neither the `R`
// nor the `I`/`D` payload. Subscribers never see a relation
// descriptor for changes they'll never receive.
func TestPgOutputFilterSuppressesEmission(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	po.SetFilter(alwaysFalseFilter{})

	body := encodeBodyV0([]any{1}, []string{"int4"})
	tuple := wrapAsHeapTuple(t, body, 1)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("filter-rejected change produced %d bytes; want 0", buf.Len())
	}
}

// TestPgOutputFilterPerKind: a filter that admits only inserts
// passes inserts through (with their preceding R) and drops
// deletes silently — and the dropped delete must not pre-emit
// a stray R for its relation either.
func TestPgOutputFilterPerKind(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	po.SetFilter(onlyInsertFilter{})

	// Delete first — should be silently dropped.
	if err := po.Change(Change{Kind: ChangeDelete, Rel: rel}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("filter-rejected delete emitted %d bytes; want 0", buf.Len())
	}

	// Then an insert — should emit R + I.
	body := encodeBodyV0([]any{1}, []string{"int4"})
	tuple := wrapAsHeapTuple(t, body, 1)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != 'R' {
		t.Errorf("first byte after allowed insert=%q want R", buf.Bytes()[0])
	}
	if !bytes.Contains(buf.Bytes(), []byte{'I'}) {
		t.Errorf("missing I after allowed insert: %x", buf.Bytes())
	}
}

// TestPgoutputUpdateMessageEncoding verifies that a ChangeUpdate
// emits a 'U' message containing both old and new tuple data, and
// that the decoder round-trips it correctly.
func TestPgoutputUpdateMessageEncoding(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "val", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	oldBody := encodeBodyV0([]any{1, "hello"}, []string{"int4", "text"})
	newBody := encodeBodyV0([]any{1, "world"}, []string{"int4", "text"})
	oldTuple := wrapAsHeapTuple(t, oldBody, 2)
	newTuple := wrapAsHeapTuple(t, newBody, 2)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	if err := po.Change(Change{Kind: ChangeUpdate, Rel: rel, OldTuple: oldTuple, NewTuple: newTuple}); err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	// First message is 'R' (relation descriptor, lazy-emitted).
	if len(raw) == 0 || raw[0] != 'R' {
		t.Fatalf("first byte=%q want R; raw=%x", raw[0], raw)
	}
	// Find the 'U' message (after the R).
	uIdx := bytes.IndexByte(raw, 'U')
	if uIdx < 0 {
		t.Fatalf("no U message in output: %x", raw)
	}

	// Decode the U message.
	msg, err := DecodeMessage(raw[uIdx:])
	if err != nil {
		t.Fatalf("DecodeMessage U: %v", err)
	}
	if msg.Kind != 'U' {
		t.Errorf("decoded kind=%q want U", msg.Kind)
	}
	if len(msg.OldTuple) == 0 {
		t.Error("U message OldTuple is empty")
	}
	if len(msg.NewTuple) == 0 {
		t.Error("U message NewTuple is empty")
	}
	// Old tuple: id=1 val='hello'
	if len(msg.OldTuple) < 2 || msg.OldTuple[1].Status != 't' {
		t.Errorf("OldTuple[1] (val) status=%q want t", msg.OldTuple[1].Status)
	}
	if string(msg.OldTuple[1].Bytes) != "hello" {
		t.Errorf("OldTuple val=%q want 'hello'", msg.OldTuple[1].Bytes)
	}
	// New tuple: id=1 val='world'
	if len(msg.NewTuple) < 2 || msg.NewTuple[1].Status != 't' {
		t.Errorf("NewTuple[1] (val) status=%q want t", msg.NewTuple[1].Status)
	}
	if string(msg.NewTuple[1].Bytes) != "world" {
		t.Errorf("NewTuple val=%q want 'world'", msg.NewTuple[1].Bytes)
	}
}

// TestPgoutputDeleteWithOldTupleEmitsO verifies that a ChangeDelete
// with a non-empty OldTuple emits a 'D' message with 'O' tuple type.
func TestPgoutputDeleteWithOldTupleEmitsO(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	body := encodeBodyV0([]any{42}, []string{"int4"})
	oldTuple := wrapAsHeapTuple(t, body, 1)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	if err := po.Change(Change{Kind: ChangeDelete, Rel: rel, OldTuple: oldTuple}); err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	dIdx := bytes.IndexByte(raw, 'D')
	if dIdx < 0 {
		t.Fatalf("no D message in output: %x", raw)
	}
	// Byte after RelOID(4) should be 'O' (full old tuple).
	if dIdx+5 >= len(raw) {
		t.Fatalf("D message too short: %x", raw[dIdx:])
	}
	tupleType := raw[dIdx+5]
	if tupleType != 'O' {
		t.Errorf("D message tuple type=%q want O (full old tuple)", tupleType)
	}

	// Decode and verify old tuple.
	msg, err := DecodeMessage(raw[dIdx:])
	if err != nil {
		t.Fatalf("DecodeMessage D: %v", err)
	}
	if len(msg.OldTuple) == 0 {
		t.Error("D message OldTuple is empty")
	}
	if msg.OldTuple[0].Status != 't' {
		t.Errorf("OldTuple[0] status=%q want t", msg.OldTuple[0].Status)
	}
	if string(msg.OldTuple[0].Bytes) != "42" {
		t.Errorf("OldTuple[0] val=%q want '42'", msg.OldTuple[0].Bytes)
	}
}

// TestPgoutputUpdateWithoutOldTupleGoesDirectlyToN pins M0103-0004:
// when REPLICA IDENTITY DEFAULT applies and no replica-identity column
// was modified, upstream `logicalrep_write_update` skips the K/O block
// entirely and emits 'U' | rel_oid(4) | 'N' | new_tuple. A real libpq
// subscriber relies on this exact shape — a 'K' marker with natts=0
// (the goopg pre-2026-05-14 behaviour) is malformed because natts must
// equal the relation's column count.
func TestPgoutputUpdateWithoutOldTupleGoesDirectlyToN(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "val", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	snap, rel := snapshotForRel(t, "items", cols)

	newBody := encodeBodyV0([]any{7, "after"}, []string{"int4", "text"})
	newTuple := wrapAsHeapTuple(t, newBody, 2)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	if err := po.Change(Change{Kind: ChangeUpdate, Rel: rel, NewTuple: newTuple}); err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	uIdx := bytes.IndexByte(raw, 'U')
	if uIdx < 0 {
		t.Fatalf("no U message in output: %x", raw)
	}
	// Wire shape: U(1) | rel_oid(4) | 'N'(1) | <new tuple body>.
	// Byte at uIdx+5 must be 'N' directly — no 'K'/'O' marker.
	if uIdx+5 >= len(raw) {
		t.Fatalf("U message too short: %x", raw[uIdx:])
	}
	if got := raw[uIdx+5]; got != 'N' {
		t.Errorf("byte after rel_oid = %q want 'N' (no K/O block expected)", got)
	}

	// Round-trip through DecodeMessage: must succeed with empty OldTuple.
	msg, err := DecodeMessage(raw[uIdx:])
	if err != nil {
		t.Fatalf("DecodeMessage U: %v", err)
	}
	if msg.Kind != 'U' {
		t.Errorf("decoded kind=%q want U", msg.Kind)
	}
	if len(msg.OldTuple) != 0 {
		t.Errorf("OldTuple len=%d want 0 (no K/O block expected)", len(msg.OldTuple))
	}
	if len(msg.NewTuple) != 2 {
		t.Fatalf("NewTuple len=%d want 2", len(msg.NewTuple))
	}
	if string(msg.NewTuple[1].Bytes) != "after" {
		t.Errorf("NewTuple[1] val=%q want 'after'", msg.NewTuple[1].Bytes)
	}
}

// TestPgOutputEmitsChangeForNonDefaultDB pins M0119-0006 (deferral row 1354
// claim 2) end-to-end at the plugin level — the "was silently nothing" proof.
// A snapshot scoped to a non-default DB makes PgOutput emit a change for a
// DB-2 relation; the pre-fix DB-1-default snapshot silently skipped it
// (snap.Lookup miss ⇒ zero bytes, TestPgOutputSkipsUnknownRelation). Mirrors
// upstream: a logical slot lives in the slot's database (slot.c:1760;
// walsender.c:1447-1518) and pgoutput resolves relations in MyDatabaseId.
func TestPgOutputEmitsChangeForNonDefaultDB(t *testing.T) {
	c := catalog.NewInMemory()
	db2Oid, err := c.CreateDatabase("db2", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}, db2Oid)
	if err != nil {
		t.Fatal(err)
	}
	rel := c.RelFileNode(tbl)
	body := encodeBodyV0([]any{1}, []string{"int4"})
	tuple := wrapAsHeapTuple(t, body, 1)

	// DB-2-scoped snapshot: the change must emit (R + I).
	var buf2 bytes.Buffer
	po2 := NewPgOutput(BuildCatalogSnapshot(c, nil, db2Oid), &buf2)
	if err := po2.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	if buf2.Len() == 0 {
		t.Fatal("DB-2 change produced no output with a DB-2-scoped snapshot")
	}
	if buf2.Bytes()[0] != 'R' {
		t.Errorf("first byte=%q want R (relation emitted before insert)", buf2.Bytes()[0])
	}
	if !bytes.Contains(buf2.Bytes(), []byte{'I'}) {
		t.Errorf("missing I message after R: %x", buf2.Bytes())
	}

	// Default (DB-1) snapshot: the same DB-2 relation is unknown ⇒ nothing —
	// this is the silent-drop the fix removes.
	var buf1 bytes.Buffer
	po1 := NewPgOutput(BuildCatalogSnapshot(c, nil), &buf1)
	if err := po1.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	if buf1.Len() != 0 {
		t.Errorf("DB-2 change leaked into the default snapshot: %x", buf1.Bytes())
	}
}
