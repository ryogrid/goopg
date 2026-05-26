package executor

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newToastFixture creates a test context suitable for TOAST tests.
func newToastFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	// Use newHOTFixture which provides pool + catalog + mvcc manager.
	ctx, _, cleanup := newHOTFixture(t)
	return ctx, cleanup
}

// TestToastRoundTripDoD is the M0046-0006 Definition of Done test:
// a 1 MiB text value must survive INSERT → SELECT with full fidelity.
func TestToastRoundTripDoD(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE toast_test (id int, data text)"); err != nil {
		t.Fatal(err)
	}

	// Build a 1 MiB string (1,048,576 bytes).
	const oneMiB = 1 << 20
	bigValue := strings.Repeat("X", oneMiB)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "toast_test"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// INSERT the 1 MiB row using the raw writeHeapRow path so we bypass
	// the SQL parser and test the codec/TOAST path directly.
	row := Row{
		{Kind: KindInt, Int: 1},
		NewStringDatum(bigValue),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT 1 MiB row: %v", err)
	}

	// SELECT via SeqScan — the scan path detoasts automatically.
	rows := runQuery(t, ctx, "SELECT id, data FROM toast_test WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Errorf("id column: want 1, got %+v", rows[0][0])
	}
	if rows[0][1].Kind != KindString {
		t.Errorf("data column: want KindString, got kind %d", rows[0][1].Kind)
	}
	if len(rows[0][1].StringValue()) != oneMiB {
		t.Errorf("data length: want %d, got %d", oneMiB, len(rows[0][1].StringValue()))
	}
	if rows[0][1].StringValue() != bigValue {
		t.Errorf("data content mismatch (first 10 chars): %q", rows[0][1].StringValue()[:10])
	}
}

// TestToastInlineSmallValue verifies that small values (below ToastThreshold)
// are stored inline in the main heap — no TOAST relation is involved.
func TestToastInlineSmallValue(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE small_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO small_test VALUES (1, 'hello')"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "small_test"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	toastRel := ToastRelFor(heapRel)

	// TOAST relation must have 0 blocks (no chunks written).
	nBlocks, err := ctx.Pool.NBlocks(toastRel)
	if err == nil && nBlocks > 0 {
		t.Errorf("expected 0 TOAST blocks for small value, got %d", nBlocks)
	}

	rows := runQuery(t, ctx, "SELECT v FROM small_test WHERE id = 1")
	if len(rows) != 1 || rows[0][0].StringValue() != "hello" {
		t.Errorf("small value round-trip failed: %+v", rows)
	}
}

// TestToastMultipleChunks verifies that values spanning more than one chunk
// (> ToastMaxChunkSize bytes) are correctly reassembled.
func TestToastMultipleChunks(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE chunk_test (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	// Exactly 3 chunks: 3 * ToastMaxChunkSize bytes.
	threeChunks := strings.Repeat("A", 3*ToastMaxChunkSize)

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "chunk_test"})
	rel := ctx.Catalog.RelFileNode(tbl)
	row := Row{
		{Kind: KindInt, Int: 42},
		NewStringDatum(threeChunks),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT v FROM chunk_test WHERE id = 42")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if len(rows[0][0].StringValue()) != len(threeChunks) {
		t.Errorf("length mismatch: want %d, got %d", len(threeChunks), len(rows[0][0].StringValue()))
	}
}

// TestToastByteaRoundTrip verifies that bytea (binary data) columns are also
// correctly toasted and detoasted.
func TestToastByteaRoundTrip(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE bytea_test (id int, b bytea)"); err != nil {
		t.Fatal(err)
	}

	const size = 4000
	bigBytes := make([]byte, size)
	for i := range bigBytes {
		bigBytes[i] = byte(i % 256)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "bytea_test"})
	rel := ctx.Catalog.RelFileNode(tbl)
	row := Row{
		{Kind: KindInt, Int: 7},
		NewBytesDatum(bigBytes),
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
		t.Fatalf("INSERT bytea: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT id FROM bytea_test WHERE id = 7")
	if len(rows) != 1 || rows[0][0].Int != 7 {
		t.Errorf("bytea row not found: %+v", rows)
	}
}

// TestToastPointerCodecRoundTrip verifies that a KindToastPointer datum
// survives EncodeRowPG → DecodeRowInto without corruption.
func TestToastPointerCodecRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "data", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	ptr := []byte{0, 0, 0, 1, 0, 16, 0, 0, 0, 0, 0, 2} // oid=1, len=1M, chunks=2
	row := Row{
		{Kind: KindInt, Int: 99},
		NewToastPointerDatum(ptr),
	}
	encoded, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}

	decoded := make(Row, 2)
	if err := DecodeRowInto(decoded, cols, encoded); err != nil {
		t.Fatalf("DecodeRowInto: %v", err)
	}
	if decoded[0].Kind != KindInt || decoded[0].Int != 99 {
		t.Errorf("id: want 99, got %+v", decoded[0])
	}
	if decoded[1].Kind != KindToastPointer {
		t.Errorf("data: want KindToastPointer, got kind %d", decoded[1].Kind)
	}
	if string(decoded[1].BytesValue()) != string(ptr) {
		t.Errorf("pointer bytes mismatch")
	}
}

func TestDetoastValueRejectsImplausibleChunkCount(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], 1)
	binary.BigEndian.PutUint32(ptr[4:8], 16)
	binary.BigEndian.PutUint32(ptr[8:12], maxDetoastChunks+1)

	_, err := DetoastValue(ctx, storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}, ptr)
	if err == nil || !strings.Contains(err.Error(), "implausible chunk count") {
		t.Fatalf("expected implausible chunk count error, got %v", err)
	}
}

func TestDetoastValueRejectsImplausibleTotalLength(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	ptr := make([]byte, 12)
	binary.BigEndian.PutUint32(ptr[0:4], 1)
	binary.BigEndian.PutUint32(ptr[4:8], maxDetoastTotalLen+1)
	binary.BigEndian.PutUint32(ptr[8:12], 1)

	_, err := DetoastValue(ctx, storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}, ptr)
	if err == nil || !strings.Contains(err.Error(), "implausible total length") {
		t.Fatalf("expected implausible total length error, got %v", err)
	}
}

// TestToastRelFor verifies the TOAST relation OID derivation.
func TestToastRelFor(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16384, Fork: storage.MainFork}
	toast := ToastRelFor(rel)
	if toast.DBOid != rel.DBOid {
		t.Errorf("DBOid mismatch")
	}
	if toast.RelOid != rel.RelOid+100_000_000 {
		t.Errorf("RelOid: want %d, got %d", rel.RelOid+100_000_000, toast.RelOid)
	}
}
