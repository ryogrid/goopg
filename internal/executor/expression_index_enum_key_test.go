package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// The three labels are chosen so DECLARATION order and ALPHABETICAL order are
// exact reverses of each other: sad < ok < happy by enumsortorder, happy < ok <
// sad by label bytes. Any test below that passes under label order would have to
// pass under a reversed sequence, which none of them accept.
const (
	enumKeyTypeSQL = "CREATE TYPE exprmood AS ENUM ('sad', 'ok', 'happy')"
	enumKeyTypeNam = "exprmood"
)

// enumKeyExpr is the smallest expression whose planner.ExprResultType names the
// enum — a bare ColumnRef of that type. Using it keeps the unit test independent
// of which enum-returning overloads the pg_proc seed happens to carry.
func enumKeyExpr() optimizer.Expr {
	return &optimizer.ColumnRef{Type: catalog.Type{Name: enumKeyTypeNam}}
}

// TestEncodeArbiterExprKeyEnumIsTypeDirected is the regression witness for the
// enum arm of the expression-key encoder (M0119-0006).
//
// The bug: encodeArbiterExprKey dispatched purely on the runtime Datum kind. An
// enum COLUMN key is EncodeFloat8(enumsortorder) — PG's enum_ops orders by
// enumsortorder (enum_cmp, src/backend/utils/adt/enum.c) — and every column path
// converts a KindString label into KindEnum before encoding, precisely so that
// holds (M0097-0022). An expression key column has no catalog column, so that
// conversion never ran: the raw label reached the KindString arm and was written
// with EncodeVarchar. The index then came out in alphabetical label order, and a
// row that DID arrive as KindEnum (the seq-scan path injects those) put 8 float
// bytes into the same index as the label bytes — two encodings, no ordering.
//
// The fix is type-directed: when the key expression's static result type names a
// user enum, the label is resolved through the catalog and every row goes
// through EncodeFloat8(sort order), whatever kind its datum arrived as.
func TestEncodeArbiterExprKeyEnumIsTypeDirected(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, enumKeyTypeSQL); err != nil {
		t.Fatalf("%s: %v", enumKeyTypeSQL, err)
	}
	et, ok := ctx.Catalog.(*catalog.InMemory).LookupEnum(enumKeyTypeNam)
	if !ok {
		t.Fatalf("enum %s not registered", enumKeyTypeNam)
	}
	order := map[string]float64{}
	for _, ev := range et.Values {
		order[ev.Label] = ev.SortOrder
	}

	keyExpr := enumKeyExpr()
	// Declaration order, which is the order the encoded keys must come out in.
	var prev []byte
	for _, label := range []string{"sad", "ok", "happy"} {
		asLabel := encodeArbiterExprKey(ctx, NewStringDatum(label), keyExpr, 0)
		if len(asLabel) != 8 {
			t.Fatalf("%q encoded to %d bytes, want the 8 of EncodeFloat8 — a "+
				"composite key walk would desynchronize at this column", label, len(asLabel))
		}
		// The two kinds an enum-typed datum reaches the encoder as must land on
		// the SAME bytes; that is the whole mixed-encoding hazard.
		asEnum := encodeArbiterExprKey(ctx, NewEnumDatum(order[label], label), keyExpr, 0)
		if string(asLabel) != string(asEnum) {
			t.Errorf("%q: KindString key %x != KindEnum key %x — the same value "+
				"would be indexed twice, under two encodings", label, asLabel, asEnum)
		}
		if prev != nil && string(prev) >= string(asLabel) {
			t.Errorf("%q (%x) does not sort after its predecessor (%x): the index "+
				"is not in enumsortorder", label, asLabel, prev)
		}
		prev = asLabel
	}

	// A value that is not a label of this enum has no place in the type's order.
	if got := encodeArbiterExprKey(ctx, NewStringDatum("nosuchlabel"), keyExpr, 0); got != nil {
		t.Errorf("non-label encoded to %x, want nil (row not indexable)", got)
	}

	// Without a catalog to resolve against, the encoder cannot know the type and
	// keeps kind dispatch — this asserts the two really did disagree, so the
	// assertions above are not vacuous.
	if got := encodeArbiterExprKey(nil, NewStringDatum("happy"), keyExpr, 0); len(got) == 8 {
		t.Fatal("kind dispatch now produces an 8-byte key for an enum label; " +
			"this test no longer proves the enum arm does anything")
	}
}

// TestExpressionIndexBuildEnumKey is the end-to-end sibling check (Hard-won
// Rule #2) across the two expression-key paths that write stored keys: the
// CREATE INDEX bulk build (encodeCompositeBTreeKeyWithExprs) and the runtime
// maintain path on INSERT (encodeExprIndexKey). Every row must be indexed under
// the same 8-byte enum-order encoding, and an ordered walk of the tree must
// reproduce the enum's DECLARATION order, not its label order.
func TestExpressionIndexBuildEnumKey(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		enumKeyTypeSQL,
		"CREATE TABLE exprenum_t (a int4, m exprmood)",
		"INSERT INTO exprenum_t VALUES (1, 'happy')",
		"INSERT INTO exprenum_t VALUES (2, 'sad')",
		// A CASE keeps the key an expression column (a bare column reference
		// would be an ordinary column key) while resolving to the enum type
		// through planner.ExprResultType's first-branch rule.
		"CREATE INDEX exprenum_idx ON exprenum_t ((CASE WHEN a > 0 THEN m ELSE m END))",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	idx := lookupIndexByName(t, ctx, "exprenum_idx")
	if got := countIndexEntries(t, ctx, idx); got != 2 {
		t.Fatalf("%d index entries after build over 2 pre-existing rows, want 2", got)
	}
	assertEnumKeyOrder(t, ctx, idx, []string{"sad", "happy"})

	// Runtime maintain path: the post-build INSERT must write a key in the same
	// encoding, and 'ok' must land BETWEEN the two built rows — under the old
	// label encoding it would have sorted last.
	if err := runDDL(t, ctx, "INSERT INTO exprenum_t VALUES (3, 'ok')"); err != nil {
		t.Fatalf("INSERT after build: %v", err)
	}
	assertEnumKeyOrder(t, ctx, idx, []string{"sad", "ok", "happy"})
}

// assertEnumKeyOrder walks idx in key order and asserts the keys are exactly the
// EncodeFloat8(enumsortorder) keys of wantLabels, in that order. A label-encoded
// key fails here rather than at some later scan: EncodeVarchar keys are neither
// 8 bytes nor float-decodable into the right neighbourhood.
func assertEnumKeyOrder(t *testing.T, ctx *Context, idx *catalog.Index, wantLabels []string) {
	t.Helper()
	et, ok := ctx.Catalog.(*catalog.InMemory).LookupEnum(enumKeyTypeNam)
	if !ok {
		t.Fatalf("enum %s not registered", enumKeyTypeNam)
	}
	want := make([]float64, 0, len(wantLabels))
	for _, label := range wantLabels {
		found := false
		for _, ev := range et.Values {
			if ev.Label == label {
				want, found = append(want, ev.SortOrder), true
				break
			}
		}
		if !found {
			t.Fatalf("label %q is not in enum %s", label, enumKeyTypeNam)
		}
	}

	tree, err := nbtree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("nbtree.Open(%s): %v", idx.Name, err)
	}
	var got []float64
	if err := tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		if len(key) != 8 {
			t.Errorf("key %x is %d bytes, want the 8 of EncodeFloat8", key, len(key))
			return true, nil
		}
		f, derr := nbtree.DecodeFloat8(key)
		if derr != nil {
			t.Errorf("key %x does not decode as float8: %v", key, derr)
			return true, nil
		}
		got = append(got, f)
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan(%s): %v", idx.Name, err)
	}
	if len(got) != len(want) {
		t.Fatalf("scanned %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d is sort order %v, want %v (%q) — index order is not "+
				"the enum's declaration order", i, got[i], want[i], wantLabels[i])
		}
	}
}
