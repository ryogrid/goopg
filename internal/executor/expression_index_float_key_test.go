package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// floatKeyExprOf builds the resolved key expression shape the encoder inspects:
// anything whose planner.ExprResultType is a float type. A bare ColumnRef is the
// smallest such expression and keeps this unit test independent of which
// float-returning overloads the pg_proc seed happens to carry.
func floatKeyExprOf(typeName string) planner.Expr {
	return &planner.ColumnRef{Type: catalog.Type{Name: typeName}}
}

// TestEncodeArbiterExprKeyFloatIsTypeDirected is the regression witness for the
// float arm of the expression-key encoder (M0119-0006).
//
// The bug: encodeArbiterExprKey dispatched purely on the runtime Datum kind, and
// goopg has no KindFloat. A stored float is rendered through PGFloatOut and
// re-parsed (floatTextDatum), so ONE float column yields KindNumeric for a value
// that prints as a plain decimal ("1.5") and KindString for one that does not
// ("1e+30", "Infinity", "NaN"). Under kind dispatch those two rows of the SAME
// expression index were encoded by two DIFFERENT encoders — EncodeNumericKey vs
// EncodeVarchar — whose byte spaces do not interleave. The index was then not
// ordered at all: a range scan could miss arbitrarily many live rows, and
// amcheck's item-order check would report a healthy index as corrupt.
//
// The fix is type-directed: when the key expression's static result type is a
// float type, every row goes through EncodeFloat8 — the encoder a float8 COLUMN
// key already uses.
func TestEncodeArbiterExprKeyFloatIsTypeDirected(t *testing.T) {
	keyExpr := floatKeyExprOf("float8")

	// Ascending float order, spanning both Datum kinds a float column produces
	// and both infinities. PG's float8 opclass orders -Infinity < finite <
	// Infinity < NaN (float8_cmp_internal, src/backend/utils/adt/float.c), which
	// is exactly what EncodeFloat8's bit flip reproduces.
	ascending := []struct {
		name string
		v    Datum
	}{
		{"-Infinity (string kind)", NewStringDatum("-Infinity")},
		{"-1e+30 (string kind)", NewStringDatum("-1e+30")},
		{"-1.5 (numeric kind)", Datum{Kind: KindNumeric, Int: -15, Scale: 1}},
		{"0 (int kind)", NewIntDatum(0)},
		{"1.5 (numeric kind)", Datum{Kind: KindNumeric, Int: 15, Scale: 1}},
		{"9 (numeric kind)", Datum{Kind: KindNumeric, Int: 9, Scale: 0}},
		{"10 (numeric kind)", Datum{Kind: KindNumeric, Int: 10, Scale: 0}},
		{"1e+30 (string kind)", NewStringDatum("1e+30")},
		{"Infinity (string kind)", NewStringDatum("Infinity")},
		{"NaN (string kind)", NewStringDatum("NaN")},
	}

	prevIdx := -1
	var prev []byte
	for i, tc := range ascending {
		got := encodeArbiterExprKey(tc.v, keyExpr, 0)
		if got == nil {
			t.Fatalf("%s: nil encoding — the row would be dropped from the index", tc.name)
		}
		if len(got) != 8 {
			t.Fatalf("%s: %d key bytes, want the 8 of EncodeFloat8 — a composite "+
				"key walk would desynchronize at this column", tc.name, len(got))
		}
		if prev != nil && bytes.Compare(prev, got) >= 0 {
			t.Errorf("%s is not ordered after %s (%x >= %x)",
				tc.name, ascending[prevIdx].name, prev, got)
		}
		prev, prevIdx = got, i
	}

	// Without the resolved expression the encoder cannot know the type and keeps
	// kind dispatch — this asserts the two really did disagree, so the test above
	// is not vacuous.
	numericKind := encodeArbiterExprKey(Datum{Kind: KindNumeric, Int: 15, Scale: 1}, nil, 0)
	stringKind := encodeArbiterExprKey(NewStringDatum("1e+30"), nil, 0)
	if len(numericKind) == 8 && len(stringKind) == 8 {
		t.Fatal("kind dispatch now produces 8-byte keys for both float " +
			"representations; this test no longer proves the float arm does anything")
	}
}

// TestExpressionIndexBuildFloatKey is the end-to-end sibling check (Hard-won
// Rule #2) across all three expression-key paths: the CREATE INDEX bulk build
// (encodeCompositeBTreeKeyWithExprs), the runtime maintain path on INSERT
// (encodeExprIndexKey) and the shared encoder they both call. Every row must be
// indexed under the SAME 8-byte float encoding, and the tree must come out in
// float order — including for the rows whose float text does not re-parse as a
// decimal, which is where the two encoders used to diverge.
func TestExpressionIndexBuildFloatKey(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TABLE exprfloat_t (a int4, f float8)",
		"INSERT INTO exprfloat_t VALUES (1, 1.5)",
		"INSERT INTO exprfloat_t VALUES (2, -2.25)",
		// Prints in exponent form, so it re-parses as KindString, not KindNumeric.
		"INSERT INTO exprfloat_t VALUES (3, 1e+30)",
		"CREATE INDEX exprfloat_idx ON exprfloat_t ((f * 2))",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	idx := lookupIndexByName(t, ctx, "exprfloat_idx")
	if got := countIndexEntries(t, ctx, idx); got != 3 {
		t.Fatalf("%d index entries after build over 3 pre-existing rows, want 3", got)
	}
	assertFloatKeyOrder(t, ctx, idx, 3)

	// Runtime maintain path: the post-build INSERT must write a key in the same
	// encoding, or it lands outside the ordered key space of the existing ones.
	if err := runDDL(t, ctx, "INSERT INTO exprfloat_t VALUES (4, -1e+30)"); err != nil {
		t.Fatalf("INSERT after build: %v", err)
	}
	if got := countIndexEntries(t, ctx, idx); got != 4 {
		t.Fatalf("%d entries after post-build INSERT, want 4", got)
	}
	assertFloatKeyOrder(t, ctx, idx, 4)
}

// assertFloatKeyOrder walks idx in key order and asserts every key is an 8-byte
// float8 key whose decoded value is strictly ascending. A mixed-encoding index
// fails here rather than at some later scan: EncodeVarchar keys are neither 8
// bytes nor float-decodable into the right neighbourhood.
func assertFloatKeyOrder(t *testing.T, ctx *Context, idx *catalog.Index, want int) {
	t.Helper()
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("btree.Open(%s): %v", idx.Name, err)
	}
	var vals []float64
	if err := tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		if len(key) != 8 {
			t.Errorf("key %x is %d bytes, want the 8 of EncodeFloat8", key, len(key))
			return true, nil
		}
		f, derr := btree.DecodeFloat8(key)
		if derr != nil {
			t.Errorf("key %x does not decode as float8: %v", key, derr)
			return true, nil
		}
		vals = append(vals, f)
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan(%s): %v", idx.Name, err)
	}
	if len(vals) != want {
		t.Fatalf("scanned %d keys, want %d", len(vals), want)
	}
	for i := 1; i < len(vals); i++ {
		if vals[i] <= vals[i-1] {
			t.Errorf("key order is not float order: %v follows %v", vals[i], vals[i-1])
		}
	}
}
