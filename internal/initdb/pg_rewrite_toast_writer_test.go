// Guards for M0131-S20.2's out-of-line pg_rewrite.ev_action writer.
//
// The assertions are anchored on measurements taken from a freshly initdb'd
// PostgreSQL 18.3 cluster (see pg_rewrite_toast_writer.go's header for the
// table), not on a re-reading of goopg's own encoder. The two that matter most
// are the ones a header-only reading of varatt.h gets wrong:
//
//   - chunk 0 of a compressed value starts with the 4-byte va_tcinfo word
//     (the oracle's pg_indexes value starts `08 13 01 00` = 70408 = the
//     uncompressed length), NOT with the pglz stream;
//   - sum(len(chunk_data)) == va_extsize == pg_column_size of the value.

package initdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/pglz"
	"github.com/goopg/goopg/internal/storage"
)

// compressibleNodeTree builds a payload with the redundancy profile of a real
// nodeToString(Query) dump — the oracle's six oversize captures compress
// between 6.4:1 and 9.9:1 — so the compressed branch is the one exercised.
func compressibleNodeTree(approxRaw int) string {
	var b bytes.Buffer
	b.WriteString("({QUERY :commandType 1 :querySource 0 :canSetTag true :utilityStmt <> ")
	for i := 0; b.Len() < approxRaw; i++ {
		// Every entry differs (attnos, oids, names, locations), which is what
		// keeps the compression ratio in the 6-10:1 band real captures show.
		// A verbatim-repeated block would compress ~100:1 and never cross the
		// inline budget, so the guard would silently stop testing anything.
		fmt.Fprintf(&b, "{TARGETENTRY :expr {VAR :varno %d :varattno %d :vartype %d :vartypmod -1 "+
			":varcollid 950 :varnullingrels (b %d) :varlevelsup 0 :varreturningtype 0 "+
			":varnosyn %d :varattnosyn %d :location %d} :resno %d :resname col_%x_%d "+
			":ressortgroupref %d :resorigtbl %d :resorigcol %d :resjunk false} ",
			i%7+1, i%53+1, 19+i%1200, i*13%9973, i%7+1, i%53+1, i*37%65521,
			i+1, i*2654435761&0xffffff, i, i%11, 1259+i%997, i%31+1)
	}
	b.WriteString(")")
	return b.String()
}

// reassemble mirrors PG's create_detoast_datum: rebuild the inline varlena by
// prefixing the concatenated chunks with a 4-byte header sized extsize+4, then
// decode it. Any divergence in what the writer chunked shows up here as a
// pglz failure or a byte mismatch, not as a subtly short value.
func reassemble(t *testing.T, ptr []byte, chunks []toastChunk) []byte {
	t.Helper()
	extSize := binary.LittleEndian.Uint32(ptr[6:10]) & (1<<30 - 1)
	rawSize := int32(binary.LittleEndian.Uint32(ptr[2:6]))
	var payload []byte
	for i, c := range chunks {
		if c.Seq != int32(i) {
			t.Fatalf("chunk %d carries chunk_seq %d — detoast reads them in seq order", i, c.Seq)
		}
		payload = append(payload, c.Data...)
	}
	if uint32(len(payload)) != extSize {
		t.Fatalf("chunk bytes %d != va_extsize %d", len(payload), extSize)
	}
	if extSize == uint32(rawSize)-4 {
		return payload // not compressed (VARATT_EXTERNAL_IS_COMPRESSED is false)
	}
	blob := make([]byte, 4, 4+len(payload))
	binary.LittleEndian.PutUint32(blob[0:4], uint32(len(payload)+4)<<2|0x02) // VARATT_IS_4B_C
	blob = append(blob, payload...)
	out, _, err := pglz.DecodeInlineCompressed(blob)
	if err != nil {
		t.Fatalf("reassembled blob does not decompress: %v", err)
	}
	return out
}

func TestExternalizeVarlenaPayloadCompressedLayout(t *testing.T) {
	raw := []byte(compressibleNodeTree(200000))
	const valueID, toastRel = 12103, 2838
	ptr, chunks := externalizeVarlenaPayload(raw, valueID, toastRel)

	if len(ptr) != varattExternalPointerSize {
		t.Fatalf("pointer is %d bytes, want %d", len(ptr), varattExternalPointerSize)
	}
	if ptr[0] != 0x01 || ptr[1] != varTagOnDisk {
		t.Fatalf("pointer header %02x %02x, want 01 12 (VARATT_IS_1B_E, VARTAG_ONDISK)", ptr[0], ptr[1])
	}
	le := binary.LittleEndian
	if got, want := int32(le.Uint32(ptr[2:6])), int32(len(raw)+4); got != want {
		t.Fatalf("va_rawsize %d, want uncompressed+VARHDRSZ %d", got, want)
	}
	extInfo := le.Uint32(ptr[6:10])
	if cm := extInfo >> varlenaExtSizeBits; cm != toastPglzCompressionID {
		t.Fatalf("compression method %d, want %d (pglz)", cm, toastPglzCompressionID)
	}
	extSize := extInfo & varlenaExtSizeMask
	if le.Uint32(ptr[10:14]) != valueID {
		t.Fatalf("va_valueid %d, want %d", le.Uint32(ptr[10:14]), valueID)
	}
	if le.Uint32(ptr[14:18]) != toastRel {
		t.Fatalf("va_toastrelid %d, want %d", le.Uint32(ptr[14:18]), toastRel)
	}
	if extSize >= uint32(len(raw)) {
		t.Fatalf("extsize %d did not shrink a %d-byte node tree — the compressed branch never ran", extSize, len(raw))
	}

	// Chunk geometry: every chunk but the last is exactly TOAST_MAX_CHUNK_SIZE
	// and the total equals va_extsize (== pg_column_size upstream).
	var total int
	for i, c := range chunks {
		if c.ChunkID != valueID || c.ToastRel != toastRel {
			t.Fatalf("chunk %d addressed to (%d,%d), want (%d,%d)", i, c.ToastRel, c.ChunkID, toastRel, valueID)
		}
		if i < len(chunks)-1 && len(c.Data) != toastMaxChunkSize {
			t.Fatalf("chunk %d is %d bytes, want a full %d", i, len(c.Data), toastMaxChunkSize)
		}
		if len(c.Data) == 0 || len(c.Data) > toastMaxChunkSize {
			t.Fatalf("chunk %d is %d bytes", i, len(c.Data))
		}
		total += len(c.Data)
	}
	if uint32(total) != extSize {
		t.Fatalf("chunks total %d bytes, va_extsize says %d", total, extSize)
	}
	if want := (int(extSize) + toastMaxChunkSize - 1) / toastMaxChunkSize; len(chunks) != want {
		t.Fatalf("%d chunks for %d bytes, want %d", len(chunks), extSize, want)
	}

	// The oracle fact that no header states: chunk 0 opens with va_tcinfo,
	// whose low 30 bits are the UNCOMPRESSED length.
	tcinfo := le.Uint32(chunks[0].Data[0:4])
	if got := tcinfo & varlenaExtSizeMask; got != uint32(len(raw)) {
		t.Fatalf("chunk 0 opens with tcinfo rawsize %d, want %d — the 4-byte "+
			"va_header must be dropped and va_tcinfo kept", got, len(raw))
	}

	if got := reassemble(t, ptr, chunks); !bytes.Equal(got, raw) {
		t.Fatalf("round-trip mismatch: %d bytes back, %d in", len(got), len(raw))
	}
}

func TestExternalizeVarlenaPayloadIncompressibleLayout(t *testing.T) {
	// Deterministic pseudo-random bytes: pglz gives up on these, so the plain
	// branch runs and PG's "is it compressed?" test (extsize < rawsize-4) must
	// come out false.
	raw := make([]byte, 5000)
	for i := range raw {
		raw[i] = byte((i*2654435761 + i*i*97) >> 7)
	}
	ptr, chunks := externalizeVarlenaPayload(raw, 12345, 2838)
	le := binary.LittleEndian
	rawSize := int32(le.Uint32(ptr[2:6]))
	extSize := le.Uint32(ptr[6:10]) & varlenaExtSizeMask
	if extSize != uint32(rawSize)-4 {
		t.Fatalf("va_extsize %d != va_rawsize-VARHDRSZ %d — an incompressible "+
			"value must read as NOT compressed", extSize, rawSize-4)
	}
	if got := reassemble(t, ptr, chunks); !bytes.Equal(got, raw) {
		t.Fatalf("round-trip mismatch for the plain branch")
	}
}

// TestPgRewriteEvActionDatumSwitchesRepresentation pins the boundary: which
// corpus entries go out of line is a closed set, everything else stays inline
// (byte-identical to what S9 wrote), and a value past the budget becomes an
// 18-byte pointer plus chunks.
//
// M0131-S20.2b turned the "no corpus rule is toasted" half of this guard into
// its inverse. Until this slice the writer was INERT — no seeded ev_action was
// oversize — and the guard's job was to say so. Now four are, and naming them
// by rule OID is what makes a FIFTH (or a vanished one) a test failure rather
// than a silent change in what goopg's initdb writes to base/{1,5}/2838.
func TestPgRewriteEvActionDatumSwitchesRepresentation(t *testing.T) {
	// rule OID -> view, for the captures whose ev_action does not fit inline.
	// Upstream's stored sizes are 9002 / 9316 / 12196 / 11481 / 35379 / 10475 B.
	// M0131-S9.3f added the largest: pg_seclabels' 203378 B raw value
	// compresses to 34093 B over 18 chunks, more than the other four together.
	// M0131-S9.3g added the sixth and LAST — pg_stats_ext_exprs (rule 12066),
	// 92109 B raw over 6 chunks, which completes upstream's 80-view corpus.
	wantToasted := map[uint32]string{
		12046: "pg_indexes",
		12056: "pg_stats",
		12061: "pg_stats_ext",
		12066: "pg_stats_ext_exprs",
		12102: "pg_seclabels",
		12177: "pg_statio_all_tables",
	}
	gotToasted := map[uint32]bool{}
	for _, e := range pgRewriteInitialEntries() {
		d, chunks := pgRewriteEvActionDatum(e)
		if len(chunks) != 0 {
			if _, ok := wantToasted[e.OID]; !ok {
				t.Fatalf("corpus rule %d (%s) is toasted but is not in the "+
					"expected set %v; if a capture just landed, this guard is "+
					"the place to record it", e.OID, e.RuleName, wantToasted)
			}
			gotToasted[e.OID] = true
			// The out-of-line datum is the 18-byte pointer, not a varlena.
			if d.Kind != executor.KindBytes || len(d.BytesValue()) != varattExternalPointerSize {
				t.Fatalf("rule %d: toasted ev_action is not an 18-byte external pointer", e.OID)
			}
			for _, c := range chunks {
				if c.ChunkID != pgRewriteToastValueID(e.OID) {
					t.Fatalf("rule %d: chunk_id %d, want rule OID+1 = %d",
						e.OID, c.ChunkID, pgRewriteToastValueID(e.OID))
				}
			}
			continue
		}
		want := pglzVarlenaDatum(e.EvAction)
		if d.Kind != want.Kind {
			t.Fatalf("rule %d: inline datum kind changed", e.OID)
		}
	}
	for oid, view := range wantToasted {
		if !gotToasted[oid] {
			t.Errorf("rule %d (%s) is expected to be stored OUT OF LINE but came "+
				"back inline — either the capture was dropped from the corpus or "+
				"the inline budget moved", oid, view)
		}
	}

	big := pgRewriteEntry{OID: 12102, RuleName: "_RETURN", EvClass: 12101,
		EvType: '1', EvEnabled: 'O', IsInstead: true, EvQual: "<>",
		EvAction: compressibleNodeTree(203378)} // pg_seclabels' raw size
	d, chunks := pgRewriteEvActionDatum(big)
	if d.Kind != executor.KindBytes || len(d.BytesValue()) != varattExternalPointerSize {
		t.Fatalf("oversize ev_action did not become an 18-byte external pointer")
	}
	if len(chunks) < 2 {
		t.Fatalf("oversize ev_action produced %d chunks, want a multi-chunk value", len(chunks))
	}
	for _, c := range chunks {
		if c.ChunkID != pgRewriteToastValueID(big.OID) {
			t.Fatalf("chunk_id %d, want rule OID+1 = %d (upstream's own allocation)",
				c.ChunkID, pgRewriteToastValueID(big.OID))
		}
	}
	if got := binary.LittleEndian.Uint32(d.BytesValue()[10:14]); got != pgRewriteToastValueID(big.OID) {
		t.Fatalf("pointer names value id %d but the chunks are %d", got, pgRewriteToastValueID(big.OID))
	}
}

// TestWriteToastChunkHeapAndIndexRoundTrip drives the physical writers and
// reads the pages back the way PG does: the heap tuples must decode with the
// TOAST relation's own 3-column descriptor, and the index must carry one
// 16-byte (chunk_id, chunk_seq) tuple per chunk, in key order, each pointing
// at the chunk's real TID.
func TestWriteToastChunkHeapAndIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte(compressibleNodeTree(203378))
	ptr, chunks := externalizeVarlenaPayload(raw, 12103, 2838)
	if len(chunks) < 5 {
		t.Fatalf("want a multi-page chunk set, got %d chunks", len(chunks))
	}

	tids, err := writeToastChunkHeap(dir, 2838, chunks)
	if err != nil {
		t.Fatalf("writeToastChunkHeap: %v", err)
	}
	if len(tids) != len(chunks) {
		t.Fatalf("%d tids for %d chunks", len(tids), len(chunks))
	}
	// Four ~2032-byte chunk tuples fit an 8 KiB page, so a value of this size
	// must have spilled past block 0 — the multi-page requirement, asserted
	// rather than assumed.
	if tids[len(tids)-1].Block == 0 {
		t.Fatalf("all %d chunks landed on block 0; the heap writer is not multi-page", len(chunks))
	}
	if err := bootstrapToastChunkIndex(dir, 2839, chunks, tids); err != nil {
		t.Fatalf("bootstrapToastChunkIndex: %v", err)
	}

	// Read the heap back through the same decoder a hosted PG's descriptor
	// implies, and reassemble the value from what is physically on disk.
	raw1, err := os.ReadFile(filepath.Join(dir, "base", "1", "2838"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw1)%storage.BlockSize != 0 || len(raw1) < 2*storage.BlockSize {
		t.Fatalf("toast heap is %d bytes", len(raw1))
	}
	cols := toastChunkCols()
	readBack := make([]toastChunk, 0, len(chunks))
	for _, tid := range tids {
		page := storage.Page(raw1[int(tid.Block)*storage.BlockSize : (int(tid.Block)+1)*storage.BlockSize])
		ht, err := storage.PageGetHeapTuple(page, tid.Offset)
		if err != nil {
			t.Fatalf("block %d off %d: %v", tid.Block, tid.Offset, err)
		}
		row := make(executor.Row, len(cols))
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		if err := executor.DecodeRowIntoMctxPGTuple(row, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			t.Fatalf("decode chunk tuple: %v", err)
		}
		readBack = append(readBack, toastChunk{
			ChunkID: uint32(row[0].Int), Seq: int32(row[1].Int),
			Data: append([]byte(nil), row[2].BytesValue()...),
		})
	}
	if got := reassemble(t, ptr, readBack); !bytes.Equal(got, raw) {
		t.Fatalf("value read back from the physical heap does not match the input")
	}

	// base/5 must be identical — a PG hosted on template1 detoasts from its
	// own copy.
	raw5, err := os.ReadFile(filepath.Join(dir, "base", "5", "2838"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw1, raw5) {
		t.Fatalf("base/1 and base/5 toast heaps differ")
	}

	// Index: metapage + at least one leaf, with the leaf tuples in key order.
	idx, err := os.ReadFile(filepath.Join(dir, "base", "1", "2839"))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) < 2*storage.BlockSize {
		t.Fatalf("index is %d bytes — the metapage-only placeholder was not replaced", len(idx))
	}
	leaf := storage.Page(idx[storage.BlockSize : 2*storage.BlockSize])
	h, err := storage.Header(leaf)
	if err != nil {
		t.Fatalf("leaf header: %v", err)
	}
	nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
	if nItems != len(chunks) {
		t.Fatalf("leaf holds %d index tuples for %d chunks", nItems, len(chunks))
	}
	le := binary.LittleEndian
	prevSeq := int32(-1)
	for i := 0; i < nItems; i++ {
		raw32 := le.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
		off := raw32 & 0x7FFF
		if length := (raw32 >> 17) & 0x7FFF; length != 16 {
			t.Fatalf("index tuple %d is %d bytes, want 16", i, length)
		}
		gotID := le.Uint32(leaf[off+8 : off+12])
		gotSeq := int32(le.Uint32(leaf[off+12 : off+16]))
		if gotID != chunks[0].ChunkID {
			t.Fatalf("index tuple %d names chunk_id %d, want %d", i, gotID, chunks[0].ChunkID)
		}
		if gotSeq <= prevSeq {
			t.Fatalf("index tuple %d has chunk_seq %d after %d — leaves must be key-ordered", i, gotSeq, prevSeq)
		}
		prevSeq = gotSeq
		blk := uint32(le.Uint16(leaf[off:off+2]))<<16 | uint32(le.Uint16(leaf[off+2:off+4]))
		hoff := le.Uint16(leaf[off+4 : off+6])
		if blk != tids[gotSeq].Block || hoff != tids[gotSeq].Offset {
			t.Fatalf("index tuple for seq %d points at (%d,%d), heap has (%d,%d)",
				gotSeq, blk, hoff, tids[gotSeq].Block, tids[gotSeq].Offset)
		}
	}
}

// TestPgRewriteRowExternalPointerSurvivesTheCodec is the sibling-path guard.
// goopg's own startup decodes EVERY pg_rewrite row (loadViewsFromHeapForDB)
// before its ev_class filter discards the bootstrap ones, so an on-disk TOAST
// pointer that the codec refuses is a startup failure on a directory goopg
// itself wrote — the failure mode this asserts against.
func TestPgRewriteRowExternalPointerSurvivesTheCodec(t *testing.T) {
	e := pgRewriteEntry{OID: 12102, RuleName: "_RETURN", EvClass: 12101,
		EvType: '1', EvEnabled: 'O', IsInstead: true, EvQual: "<>",
		EvAction: compressibleNodeTree(203378)}
	row, chunks := pgRewriteRowToasted(e)
	if len(chunks) == 0 {
		t.Fatal("fixture did not go out of line")
	}
	cols := pgRewriteColDefs()
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded := make(executor.Row, len(cols))
	if err := executor.DecodeRowIntoMctxPGTuple(decoded, cols, payload, nil, len(cols), nil); err != nil {
		t.Fatalf("decode of a row with an external ev_action: %v", err)
	}
	got := decoded[7]
	if got.Kind != executor.KindBytes || len(got.BytesValue()) != varattExternalPointerSize {
		t.Fatalf("ev_action decoded as kind %d / %d bytes, want the 18-byte pointer verbatim",
			got.Kind, len(got.BytesValue()))
	}
	if !bytes.Equal(got.BytesValue(), row[7].BytesValue()) {
		t.Fatalf("pointer bytes changed across the codec")
	}
	// The columns after ev_action are none, but the ones before it must still
	// decode — a wrong consumed-length on the pointer would corrupt them.
	if uint32(decoded[0].Int) != e.OID || decoded[1].StringValue() != "_RETURN" {
		t.Fatalf("row prefix corrupted: oid=%v rulename=%q", decoded[0].Int, decoded[1].StringValue())
	}
}
