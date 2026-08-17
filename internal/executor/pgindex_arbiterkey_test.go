package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-vii guards — the arbiter-key funnel.
//
// `encodeArbiterKey` builds ONE key that the upsert path both probes the
// arbiter tree with and inserts into it. Under the blob format those really are
// the same bytes (no TID in a blob key), which is why the conflation was
// invisible and why reusing the key is deliberate — it is what keeps a
// side-effectful arbiter expression from being evaluated twice. Under the tuple
// format the entry carries the row's heap TID and the probe carries the zero
// TID, so they are two keys; these tests pin the split, the blob branch's
// byte-for-byte identity, the per-index dual-format property, and the
// column/attribute reconciliation the tuple branch cannot do silently.

func arbiterFixture(t *testing.T, oid uint32, cols ...catalog.Column) (*Context, *optimizer.OnConflictPlan, *catalog.Table) {
	t.Helper()
	tbl := keyDescTable(cols...)
	names := make([]string, len(cols))
	ords := make([]int, len(cols))
	for i := range cols {
		names[i] = tbl.Columns[i].Name
		ords[i] = i
	}
	idx := &catalog.Index{OID: oid, Name: "arb", Table: tbl, Method: "btree", Unique: true, Columns: names}
	return &Context{}, &optimizer.OnConflictPlan{ArbiterIndex: idx, ArbiterColumns: ords}, tbl
}

func TestArbiterKeyBlobProbeAndEntryAreIdentical(t *testing.T) {
	withBlobIndexKeys(t)
	// Gate off is the shipped state. Both funnels must reproduce
	// encodeArbiterKey exactly, and the heap TID handed to the entry funnel must
	// not reach the bytes — otherwise the Phase-B key reuse in applyInsert would
	// start writing a different key than it probed with.
	ctx, oc, tbl := arbiterFixture(t, 500, col("a", "int4"), col("b", "text"))
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	want, err := encodeArbiterKey(ctx, oc, tbl, row, 0)
	if err != nil || want == nil {
		t.Fatalf("fixture encodeArbiterKey = %x, %v", want, err)
	}
	probe, err := ctx.arbiterProbeKey(oc, tbl, row, 0)
	if err != nil {
		t.Fatalf("arbiterProbeKey: %v", err)
	}
	entry, err := ctx.arbiterEntryKey(oc, tbl, row, storage.ItemPointer{Block: 7, Offset: 3}, 0)
	if err != nil {
		t.Fatalf("arbiterEntryKey: %v", err)
	}
	if !bytes.Equal(probe, want) {
		t.Fatalf("blob probe key = %x, want %x", probe, want)
	}
	if !bytes.Equal(entry, want) {
		t.Fatalf("blob entry key = %x, want %x", entry, want)
	}
}

func TestArbiterKeyTupleEntryCarriesTheHeapTIDAndProbeSortsFirst(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, oc, tbl := arbiterFixture(t, 501, col("a", "int4"), col("b", "text"))
	desc := ctx.pgIndexKeyDesc(oc.ArbiterIndex)
	if desc == nil {
		t.Fatal("fixture arbiter index is not describable; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	probe, err := ctx.arbiterProbeKey(oc, tbl, row, 0)
	if err != nil {
		t.Fatalf("arbiterProbeKey: %v", err)
	}
	entry, err := ctx.arbiterEntryKey(oc, tbl, row, storage.ItemPointer{Block: 7, Offset: 3}, 0)
	if err != nil {
		t.Fatalf("arbiterEntryKey: %v", err)
	}
	if bytes.Equal(probe, entry) {
		t.Fatal("tuple probe and entry keys are identical; the heap TID never reached the image")
	}
	// Neither is a pivot (both name every key attribute), so the only ordering
	// difference is the TID tiebreak — and the zero TID is minus infinity, so the
	// probe lands BEFORE the entry it is looking for. That is what makes an
	// arbiter scan over duplicates see all of its own matches.
	if got := nbtree.BTreeTupleGetNAtts(entry, uint16(desc.NKeyAtts())); int(got) != desc.NKeyAtts() {
		t.Fatalf("entry natts = %d, want %d (a full key must not be a pivot)", got, desc.NKeyAtts())
	}
	cmp, err := nbtree.ComparePGIndexTuples(desc, probe, entry)
	if err != nil {
		t.Fatalf("ComparePGIndexTuples: %v", err)
	}
	if cmp >= 0 {
		t.Fatalf("probe vs entry = %d, want < 0 (zero TID is minus infinity)", cmp)
	}
	// Same key attributes on both sides: the split is about the TID, not the key.
	if c, err := nbtree.ComparePGIndexTupleKeyAttrs(desc, probe, entry); err != nil || c != 0 {
		t.Fatalf("key-attribute comparison = %d, %v; want 0, nil", c, err)
	}
}

func TestArbiterKeyTupleNullConflictColumnHasNoKey(t *testing.T) {
	// A NULL conflict-key column never conflicts (upstream's unique-constraint
	// inference semantics), which both formats express as a nil key with a nil
	// error — the caller's "no probe, no maintenance" signal. The tuple format
	// CAN represent a NULL, so the funnel must keep answering nil rather than
	// quietly starting to index NULL-keyed rows.
	withPGIndexTupleKeys(t)
	ctx, oc, tbl := arbiterFixture(t, 502, col("a", "int4"), col("b", "text"))
	row := Row{NewIntDatum(42), NullDatum}

	for _, tc := range []struct {
		name string
		fn   func() ([]byte, error)
	}{
		{"probe", func() ([]byte, error) { return ctx.arbiterProbeKey(oc, tbl, row, 0) }},
		{"entry", func() ([]byte, error) {
			return ctx.arbiterEntryKey(oc, tbl, row, storage.ItemPointer{Block: 1, Offset: 1}, 0)
		}},
	} {
		key, err := tc.fn()
		if err != nil || key != nil {
			t.Errorf("%s key with a NULL conflict column = %x, %v; want nil, nil", tc.name, key, err)
		}
	}
}

func TestArbiterKeyExpressionArbiterKeepsBlob(t *testing.T) {
	// An expression arbiter column (ArbiterColumns[i] == -1) belongs to an index
	// buildPGIndexKeyDesc refuses, so it resolves to a nil descriptor and keeps
	// the blob path whole — the per-index dual-format property. If the resolver
	// ever accepted such an index, the -1 ordinal would be caught by the
	// reconciliation below rather than encoded as a wrong column.
	withPGIndexTupleKeys(t)
	ctx, oc, tbl := arbiterFixture(t, 503, col("a", "int4"))
	oc.ArbiterIndex.Columns = []string{""}
	oc.ArbiterColumns = []int{-1}
	if ctx.pgIndexKeyDesc(oc.ArbiterIndex) != nil {
		t.Fatal("expression arbiter index was described; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42)}
	// No ArbiterExprs: encodeArbiterKey answers nil (nothing to evaluate), and
	// the funnel must answer exactly what it answers, not an error.
	want, err := encodeArbiterKey(ctx, oc, tbl, row, 0)
	if err != nil {
		t.Fatalf("fixture encodeArbiterKey: %v", err)
	}
	got, err := ctx.arbiterProbeKey(oc, tbl, row, 0)
	if err != nil {
		t.Fatalf("arbiterProbeKey: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("expression arbiter probe key = %x, want the blob answer %x", got, want)
	}
}

func TestArbiterKeyTupleRejectsColumnMismatch(t *testing.T) {
	// ArbiterColumns are ordinals into the table the upsert runs against, which
	// need not be the table the index was resolved on. Under the blob format a
	// mismatched ordinal produced a key that simply matched nothing; a tuple key
	// built from the wrong column matches something ELSE, so the funnel
	// reconciles the two sides by name and reports a disagreement.
	withPGIndexTupleKeys(t)
	ctx, oc, tbl := arbiterFixture(t, 504, col("a", "int4"), col("b", "int4"))
	if ctx.pgIndexKeyDesc(oc.ArbiterIndex) == nil {
		t.Fatal("fixture arbiter index is not describable")
	}
	oc.ArbiterColumns = []int{1, 0} // swapped against the index key order
	row := Row{NewIntDatum(42), NewIntDatum(7)}
	if _, err := ctx.arbiterProbeKey(oc, tbl, row, 0); err == nil {
		t.Fatal("swapped arbiter ordinals were encoded silently; want an error")
	}
	// Out of range is the other half of the same guard.
	oc.ArbiterColumns = []int{0, 9}
	if _, err := ctx.arbiterProbeKey(oc, tbl, row, 0); err == nil {
		t.Fatal("out-of-range arbiter ordinal was encoded silently; want an error")
	}
}

func TestArbiterKeyIsTheOnlyArbiterKeyWriter(t *testing.T) {
	// The funnel only holds if it cannot be bypassed: a site calling
	// encodeArbiterKey directly would probe a tuple-format tree with a
	// concatenated blob, and the failure would surface as a missed conflict —
	// a duplicate row, never a compile error.
	const needle = "encodeArbiterKey("
	src, err := os.ReadFile(filepath.Clean("operators_upsert.go"))
	if err != nil {
		t.Fatalf("read operators_upsert.go: %v", err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue // prose may name it
		}
		if strings.HasPrefix(trimmed, "func encodeArbiterKey(") {
			continue // the definition itself, which the funnel calls
		}
		if strings.Contains(line, needle) {
			t.Errorf("operators_upsert.go:%d bypasses the arbiter-key funnel: %s", i+1, trimmed)
		}
	}
}
