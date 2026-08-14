package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-iv guards — the row-key funnels.
//
// `encodeIndexKeyFromCols` has served two different jobs since M0100-0005:
// the key an entry is STORED under, and the key a uniqueness/exclusion probe
// POSITIONS with. Under the blob format those are the same bytes, so nothing
// could tell them apart. Under the tuple format the stored key embeds the row's
// heap TID and the probe key must not, so the two roles are now two functions.
// These tests pin that split, the blob branch's byte-for-byte identity, and the
// two remaining callers that must NOT move to either funnel.

func rowKeyCtxAndIndex(t *testing.T, oid uint32, cols ...catalog.Column) (*Context, *catalog.Index, []catalog.Column) {
	t.Helper()
	tbl := keyDescTable(cols...)
	names := make([]string, len(cols))
	for i := range cols {
		names[i] = cols[i].Name
	}
	idx := &catalog.Index{OID: oid, Name: "i", Table: tbl, Method: "btree", Columns: names}
	return &Context{}, idx, tbl.Columns
}

func TestIndexRowKeyBlobEntryAndProbeAreIdentical(t *testing.T) {
	withBlobIndexKeys(t)
	// Gate off is the shipped state: both funnels must reproduce
	// encodeIndexKeyFromCols exactly, and the real heap TID handed to
	// indexEntryKey must not reach the bytes.
	ctx, idx, cols := rowKeyCtxAndIndex(t, 400, col("a", "int4"), col("b", "text"))
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	want, err := encodeIndexKeyFromCols(nil, idx, cols, row, nil)
	if err != nil || want == nil {
		t.Fatalf("fixture encodeIndexKeyFromCols = %x, %v", want, err)
	}
	entry, err := ctx.indexEntryKey(idx, cols, row, storage.ItemPointer{Block: 7, Offset: 3})
	if err != nil {
		t.Fatalf("indexEntryKey: %v", err)
	}
	probe, err := ctx.indexRowProbeKey(idx, cols, row)
	if err != nil {
		t.Fatalf("indexRowProbeKey: %v", err)
	}
	if !bytes.Equal(entry, want) {
		t.Fatalf("blob entry key = %x, want %x", entry, want)
	}
	if !bytes.Equal(probe, want) {
		t.Fatalf("blob probe key = %x, want %x", probe, want)
	}
}

func TestIndexRowKeyTupleEntryCarriesTheHeapTIDAndProbeSortsFirst(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 401, col("a", "int4"), col("b", "text"))
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	tid := storage.ItemPointer{Block: 7, Offset: 3}
	entry, err := ctx.indexEntryKey(idx, cols, row, tid)
	if err != nil {
		t.Fatalf("indexEntryKey: %v", err)
	}
	probe, err := ctx.indexRowProbeKey(idx, cols, row)
	if err != nil {
		t.Fatalf("indexRowProbeKey: %v", err)
	}

	// The whole point of the split: same key attributes, different images.
	if bytes.Equal(entry, probe) {
		t.Fatal("tuple entry and probe keys are identical; the heap TID never reached the image")
	}
	// Both name every key attribute (neither is a pivot), so the ordering
	// difference is the TID tiebreak alone — and the zero TID is minus infinity,
	// so the probe lands BEFORE the entry it is looking for. That is what makes
	// a duplicate scan see all of its own matches.
	if got := btree.BTreeTupleGetNAtts(entry, uint16(desc.NKeyAtts())); int(got) != desc.NKeyAtts() {
		t.Fatalf("entry natts = %d, want %d (a full key must not be a pivot)", got, desc.NKeyAtts())
	}
	cmp, err := btree.ComparePGIndexTuples(desc, probe, entry)
	if err != nil {
		t.Fatalf("ComparePGIndexTuples: %v", err)
	}
	if cmp >= 0 {
		t.Fatalf("probe vs entry = %d, want < 0 (zero TID is minus infinity)", cmp)
	}

	// And the attributes themselves are the same on both sides.
	pa, _, err := btree.DeformPGIndexTuple(probe, desc.Physical(), desc.NKeyAtts())
	if err != nil {
		t.Fatalf("deform probe: %v", err)
	}
	ea, _, err := btree.DeformPGIndexTuple(entry, desc.Physical(), desc.NKeyAtts())
	if err != nil {
		t.Fatalf("deform entry: %v", err)
	}
	for i := range pa {
		if !bytes.Equal(pa[i], ea[i]) {
			t.Fatalf("attribute %d differs: probe %x, entry %x", i+1, pa[i], ea[i])
		}
	}
}

func TestIndexRowKeyTupleNullKeyColumnHasNoEntry(t *testing.T) {
	// A NULL key column has always meant "this row is not in this index"
	// (nil key, nil error). The tuple format CAN represent a NULL, so the funnel
	// must keep answering nil rather than quietly starting to index NULLs —
	// that is a semantic change, tracked separately (ledger, B2-a).
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 402, col("a", "int4"), col("b", "text"))
	row := Row{NewIntDatum(42), NullDatum}

	for _, tc := range []struct {
		name string
		fn   func() ([]byte, error)
	}{
		{"entry", func() ([]byte, error) {
			return ctx.indexEntryKey(idx, cols, row, storage.ItemPointer{Block: 1, Offset: 1})
		}},
		{"probe", func() ([]byte, error) { return ctx.indexRowProbeKey(idx, cols, row) }},
	} {
		key, err := tc.fn()
		if err != nil || key != nil {
			t.Errorf("%s key with a NULL attribute = %x, %v; want nil, nil", tc.name, key, err)
		}
	}
}

func TestIndexRowKeyUndescribableIndexKeepsBlob(t *testing.T) {
	// An index buildPGIndexKeyDesc refuses keeps the blob path whole — the
	// per-index dual-format property B2-c-ii and B2-c-iii both assert.
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 403, col("a", "int4"))
	idx.ColOpClasses = []string{"int4_ops"} // an explicit opclass is refused
	if ctx.pgIndexKeyDesc(idx) != nil {
		t.Fatal("fixture index was described; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42)}

	want, err := encodeIndexKeyFromCols(nil, idx, cols, row, nil)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	got, err := ctx.indexEntryKey(idx, cols, row, storage.ItemPointer{Block: 7, Offset: 3})
	if err != nil {
		t.Fatalf("indexEntryKey: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("undescribable index entry key = %x, want the blob %x", got, want)
	}
}

func TestIndexRowKeyIsTheOnlyTreeKeyWriterInTheRoutedFiles(t *testing.T) {
	// The funnel only holds if it cannot be bypassed: a site that builds a tree
	// key with encodeIndexKeyFromCols would hand a tuple-format tree a
	// concatenated blob, and the failure would surface as wrong ROWS, never as a
	// compile error.
	//
	// Scope is the two files whose every call site was routed. operators_storage.go
	// and ssi.go are deliberately not scanned: they retain the encoder for the two
	// VALUE-FINGERPRINT uses (indexKeyColumnsChanged, ssiRecordHashIndexInsert)
	// that must stay TID-free — see those functions' comments and the ledger row.
	const needle = "encodeIndexKeyFromCols("
	for _, f := range []string{"operators_upsert.go", "deferred_exclusion.go"} {
		src, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may name it
			}
			if strings.Contains(line, needle) {
				t.Errorf("%s:%d bypasses the row-key funnels: %s", f, i+1, trimmed)
			}
		}
	}
}
