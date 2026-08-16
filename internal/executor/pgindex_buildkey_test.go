package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-v guards — the build path's key, order and
// duplicate test.
//
// The bulk build is the writer with a property the runtime maintain path does
// not have: it SORTS its entries and then decides uniqueness by looking at
// neighbours. Both of those steps read the key, and both were written against
// the blob format — a TID-free byte string whose bytewise order is the index
// order. Under the tuple format neither holds: the heap TID is inside the image
// (so duplicates never compare equal) and a PG datum is not order-preserving
// under bytes.Compare (so the sort would place entries where no descent looks
// for them). These tests pin all three pieces plus the blob branch's
// byte-for-byte identity.

func buildKeyCtxAndIndex(t *testing.T, oid uint32, cols ...catalog.Column) (*Context, *catalog.Index, []*catalog.Column) {
	t.Helper()
	tbl := keyDescTable(cols...)
	names := make([]string, len(cols))
	ptrs := make([]*catalog.Column, len(cols))
	for i := range tbl.Columns {
		names[i] = tbl.Columns[i].Name
		ptrs[i] = &tbl.Columns[i]
	}
	idx := &catalog.Index{OID: oid, Name: "i", Table: tbl, Method: "btree", Columns: names}
	return &Context{}, idx, ptrs
}

func TestIndexBuildEntryKeyBlobIsTheOldEncoderVerbatim(t *testing.T) {
	withBlobIndexKeys(t)
	// Gate off is the shipped state: the funnel must reproduce
	// encodeCompositeBTreeKeyWithExprs exactly, and the heap TID it now receives
	// must stay out of the bytes (the blob format carries it beside the key, in
	// btree.BulkEntry.Ptr).
	ctx, idx, cols := buildKeyCtxAndIndex(t, 420, col("a", "int4"), col("b", "text"))
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	want, hasNull, encErr := encodeCompositeBTreeKeyWithExprs(ctx, row, cols, nil, 0)
	if encErr != nil || hasNull || want == nil {
		t.Fatalf("fixture encodeCompositeBTreeKeyWithExprs = %x, %v, %v", want, hasNull, encErr)
	}
	for _, tid := range []storage.ItemPointer{{}, {Block: 7, Offset: 3}} {
		got, hasNullKey, err := ctx.indexBuildEntryKey(idx, cols, nil, row, tid, 0)
		if err != nil || hasNullKey {
			t.Fatalf("indexBuildEntryKey(tid=%v) = %v, %v", tid, hasNullKey, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("blob build key with tid=%v = %x, want %x", tid, got, want)
		}
	}
}

func TestIndexBuildEntryKeyTupleCarriesTheRowsHeapTID(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx, cols := buildKeyCtxAndIndex(t, 421, col("a", "int4"), col("b", "text"))
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	lo, _, err := ctx.indexBuildEntryKey(idx, cols, nil, row, storage.ItemPointer{Block: 2, Offset: 1}, 0)
	if err != nil {
		t.Fatalf("indexBuildEntryKey: %v", err)
	}
	hi, _, err := ctx.indexBuildEntryKey(idx, cols, nil, row, storage.ItemPointer{Block: 2, Offset: 9}, 0)
	if err != nil {
		t.Fatalf("indexBuildEntryKey: %v", err)
	}

	// Same key attributes, different rows: the images must differ, and the
	// difference must be an ORDER (the heapkeyspace tiebreak the bulk sort has
	// to reproduce), not just distinct bytes.
	if bytes.Equal(lo, hi) {
		t.Fatal("two entries for different heap TIDs produced identical keys; the TID never reached the image")
	}
	if got := btree.BTreeTupleGetNAtts(lo, uint16(desc.NKeyAtts())); int(got) != desc.NKeyAtts() {
		t.Fatalf("entry natts = %d, want %d (a stored entry must not be a pivot)", got, desc.NKeyAtts())
	}
	if tid, ok := btree.BTreeTupleGetHeapTID(lo); !ok || tid.Block != 2 || tid.Offset != 1 {
		t.Fatalf("entry heap TID = %v, %v; want {2 1}", tid, ok)
	}
	cmp, cmpErr := btree.ComparePGIndexTuples(desc, lo, hi)
	if cmpErr != nil {
		t.Fatalf("ComparePGIndexTuples: %v", cmpErr)
	}
	if cmp >= 0 {
		t.Fatalf("offset 1 vs offset 9 = %d, want < 0", cmp)
	}
	// ...and the SAME pair is equal to the uniqueness question, which is the
	// whole reason ComparePGIndexTupleKeyAttrs exists.
	keyCmp, keyCmpErr := btree.ComparePGIndexTupleKeyAttrs(desc, lo, hi)
	if keyCmpErr != nil {
		t.Fatalf("ComparePGIndexTupleKeyAttrs: %v", keyCmpErr)
	}
	if keyCmp != 0 {
		t.Fatalf("key-attribute comparison of two duplicates = %d, want 0", keyCmp)
	}
}

func TestIndexBuildEntryKeyNullKeyColumnHasNoEntryInEitherFormat(t *testing.T) {
	// A NULL value key column has always meant "not indexable" for the build
	// (hasNullKey, no key), and the caller either skips the row or dedups it
	// under NULLS NOT DISTINCT. The tuple format CAN represent a NULL, so the
	// funnel must keep answering the same thing rather than quietly starting to
	// index NULLs — that divergence is tracked in the deferral ledger, not
	// changed here.
	for _, tuple := range []bool{false, true} {
		t.Run(map[bool]string{false: "blob", true: "tuple"}[tuple], func(t *testing.T) {
			if tuple {
				withPGIndexTupleKeys(t)
			}
			ctx, idx, cols := buildKeyCtxAndIndex(t, 422, col("a", "int4"), col("b", "text"))
			row := Row{NewIntDatum(42), NullDatum}
			key, hasNullKey, err := ctx.indexBuildEntryKey(idx, cols, nil, row, storage.ItemPointer{Block: 1, Offset: 1}, 0)
			if err != nil || key != nil || !hasNullKey {
				t.Fatalf("build key with a NULL attribute = %x, hasNullKey=%v, %v; want nil, true, nil", key, hasNullKey, err)
			}
		})
	}
}

func TestIndexBuildEntryKeyUndescribableIndexKeepsBlob(t *testing.T) {
	// An index buildPGIndexKeyDesc refuses keeps the blob path whole, gate on:
	// a tree's format is a per-index property, which the flip must assert rather
	// than assume.
	withPGIndexTupleKeys(t)
	ctx, idx, cols := buildKeyCtxAndIndex(t, 423, col("a", "int4"))
	idx.ColOpClasses = []string{"int4_ops"} // an explicit opclass is refused
	if ctx.pgIndexKeyDesc(idx) != nil {
		t.Fatal("fixture index was described; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42)}

	want, _, encErr := encodeCompositeBTreeKeyWithExprs(ctx, row, cols, nil, 0)
	if encErr != nil {
		t.Fatalf("fixture: %v", encErr)
	}
	got, _, err := ctx.indexBuildEntryKey(idx, cols, nil, row, storage.ItemPointer{Block: 7, Offset: 3}, 0)
	if err != nil {
		t.Fatalf("indexBuildEntryKey: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("undescribable index build key = %x, want the blob %x", got, want)
	}
}

func TestSortBuildEntriesFindDuplicateBlobUnchanged(t *testing.T) {
	withBlobIndexKeys(t)
	// The blob branch must be M0055-0006 Phase E byte for byte: bytewise order,
	// bytewise equality.
	ctx, idx, cols := buildKeyCtxAndIndex(t, 424, col("a", "int4"))
	mk := func(v int64, blk storage.BlockNumber) btree.BulkEntry {
		key, _, err := ctx.indexBuildEntryKey(idx, cols, nil, Row{NewIntDatum(v)}, storage.ItemPointer{Block: blk, Offset: 1}, 0)
		if err != nil {
			t.Fatalf("indexBuildEntryKey: %v", err)
		}
		return btree.BulkEntry{Key: key, Ptr: storage.ItemPointer{Block: blk, Offset: 1}}
	}
	entries := []btree.BulkEntry{mk(256, 1), mk(1, 2), mk(3, 3)}
	if sortBuildEntriesFindDuplicate(nil, entries) >= 0 {
		t.Fatal("distinct blob keys reported as duplicates")
	}
	if !sort.SliceIsSorted(entries, func(i, j int) bool {
		return string(entries[i].Key) < string(entries[j].Key)
	}) {
		t.Fatal("blob entries are not in bytewise order")
	}
	// Two rows with the same value: distinct heap TIDs, but the blob key is
	// TID-free, so they ARE the same key.
	dups := []btree.BulkEntry{mk(5, 1), mk(5, 2)}
	if sortBuildEntriesFindDuplicate(nil, dups) < 0 {
		t.Fatal("equal blob keys not reported as duplicates")
	}
}

func TestSortBuildEntriesFindDuplicateTupleOrdersByAttributeAndIgnoresTheTID(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx, cols := buildKeyCtxAndIndex(t, 425, col("a", "int4"))
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	mk := func(v int64, blk storage.BlockNumber, off uint16) btree.BulkEntry {
		key, _, err := ctx.indexBuildEntryKey(idx, cols, nil, Row{NewIntDatum(v)}, storage.ItemPointer{Block: blk, Offset: off}, 0)
		if err != nil {
			t.Fatalf("indexBuildEntryKey: %v", err)
		}
		return btree.BulkEntry{Key: key, Ptr: storage.ItemPointer{Block: blk, Offset: off}}
	}

	// 1 and 256 are the pair that separates the two orders: an int4 datum is
	// stored little-endian, so bytewise 256 (00 01 00 00) sorts BELOW 1
	// (01 00 00 00) while the index order is the opposite. A build that kept
	// sorting by string(key) would file them in the wrong leaf order and every
	// later descent would binary-search past them.
	//
	// Both entries deliberately carry the SAME heap TID: t_tid is the first
	// field of an index tuple, so a differing TID would dominate the bytewise
	// comparison and the pair would demonstrate the TID's effect on byte order
	// rather than the datum's.
	entries := []btree.BulkEntry{mk(256, 1, 1), mk(1, 1, 1)}
	if !(string(entries[0].Key) < string(entries[1].Key)) {
		t.Fatal("fixture pair is bytewise-ordered the same way as the index order; it cannot tell the two sorts apart")
	}
	if sortBuildEntriesFindDuplicate(desc, entries) >= 0 {
		t.Fatal("distinct tuple keys reported as duplicates")
	}
	first, _, err := btree.DeformPGIndexTuple(entries[0].Key, desc.Physical(), 1)
	if err != nil {
		t.Fatalf("deform: %v", err)
	}
	if got := int32(uint32(first[0][0]) | uint32(first[0][1])<<8 | uint32(first[0][2])<<16 | uint32(first[0][3])<<24); got != 1 {
		t.Fatalf("smallest entry after sort = %d, want 1 (index order, not bytewise order)", got)
	}

	// The duplicate test: same value, two heap rows. Under the tuple format the
	// keys DIFFER (the TID is in the image), which is exactly why bytes.Equal
	// would have admitted a duplicate into a unique index.
	dups := []btree.BulkEntry{mk(5, 1, 1), mk(5, 2, 4)}
	if bytes.Equal(dups[0].Key, dups[1].Key) {
		t.Fatal("fixture duplicates have identical keys; the test would pass for the wrong reason")
	}
	if sortBuildEntriesFindDuplicate(desc, dups) < 0 {
		t.Fatal("duplicate key values not reported: a unique index build would admit them")
	}
}

func TestBuildPathHasNoUnroutedKeyEncoder(t *testing.T) {
	// The funnel only holds if it cannot be bypassed: a build site that encodes
	// its own key hands a tuple-format tree a concatenated blob, and the failure
	// surfaces as a wrong index — never as a compile error.
	//
	// Two functions are allowed to name the blob encoders: they ARE the blob
	// encoders. backfillBTree is allowed because it is dead code with no callers
	// (see its comment); re-adding a caller must route it first, and this list is
	// where that shows up.
	allowed := map[string]bool{
		"encodeCompositeBTreeKey":          true,
		"encodeCompositeBTreeKeyWithExprs": true,
		"backfillBTree":                    true,
	}
	src, err := os.ReadFile(filepath.Clean("operators_ddl.go"))
	if err != nil {
		t.Fatalf("read operators_ddl.go: %v", err)
	}
	cur := ""
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "func ") {
			cur = funcNameOfDecl(line)
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue // prose may name them
		}
		if !strings.Contains(line, "encodeCompositeBTreeKey(") && !strings.Contains(line, "encodeCompositeBTreeKeyWithExprs(") {
			continue
		}
		if allowed[cur] {
			continue
		}
		t.Errorf("operators_ddl.go:%d (%s) bypasses indexBuildEntryKey: %s", i+1, cur, trimmed)
	}
}

// funcNameOfDecl extracts the function name from a top-level `func ...` line,
// method receiver included or not.
func funcNameOfDecl(line string) string {
	rest := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(rest, "(") {
		if c := strings.Index(rest, ")"); c >= 0 {
			rest = strings.TrimSpace(rest[c+1:])
		}
	}
	if p := strings.IndexAny(rest, "(["); p >= 0 {
		rest = rest[:p]
	}
	return strings.TrimSpace(rest)
}
