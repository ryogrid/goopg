package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// arrayKeyOracleOrder is the ordering PostgreSQL 18.3 itself reports for these
// int4[] values under array_ops — captured from the reference cluster, not
// derived by reading array_cmp:
//
//	select x from (values ('{1,2}'::int4[]),('{10}'),('{2,0}'),('{}'),('{1}'),
//	                      ('{1,NULL}'),('{1,0}')) v(x) order by x;
//	 {} | {1} | {1,0} | {1,2} | {1,NULL} | {2,0} | {10}
//
// Every property the encoding has to reproduce is visible in that list: element
// by element rather than lexicographically over the text ({2,0} before {10}),
// a prefix before its extensions ({1} before {1,0}), and a NULL element after
// every non-NULL one ({1,2} before {1,NULL}).
var arrayKeyOracleOrder = []string{"{}", "{1}", "{1,0}", "{1,2}", "{1,NULL}", "{2,0}", "{10}"}

// TestEncodeArrayBTreeKeyMatchesArrayCmpOrder asserts the encoded keys sort in
// the oracle order above. Before M0119-0006 an int4[] column reached the scalar
// int4 arm of encodeBTreeKeyForColumn (catalog.Type.Name holds the ELEMENT type
// for an array column), so this produced 22P02 rather than any order at all.
func TestEncodeArrayBTreeKeyMatchesArrayCmpOrder(t *testing.T) {
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	keys := make([][]byte, len(arrayKeyOracleOrder))
	for i, lit := range arrayKeyOracleOrder {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", lit, err)
		}
		if len(k) == 0 {
			// A zero-length key is indistinguishable from "no key" to
			// encodeIndexKeyFromCols, which drops the row from the index.
			t.Fatalf("encode %s produced an empty key", lit)
		}
		keys[i] = k
	}
	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i-1], keys[i]) >= 0 {
			t.Errorf("key(%s)=%x is not below key(%s)=%x, but PG orders them that way",
				arrayKeyOracleOrder[i-1], keys[i-1], arrayKeyOracleOrder[i], keys[i])
		}
	}
}

// TestEncodeArrayBTreeKeyTextElements repeats the ordering check for a
// variable-width element type: the array segment must stay ordered when the
// element encoding is EncodeVarchar's 0x00-terminated form rather than a fixed
// width. PG 18.3 orders these '{a}' < '{a,b}' < '{ab}' < '{b}'.
func TestEncodeArrayBTreeKeyTextElements(t *testing.T) {
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "text", IsArray: true}}
	order := []string{"{}", "{a}", "{a,b}", "{ab}", "{b}"}
	var prev []byte
	for i, lit := range order {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", lit, err)
		}
		if i > 0 && bytes.Compare(prev, k) >= 0 {
			t.Errorf("key(%s)=%x is not below key(%s)=%x", order[i-1], prev, lit, k)
		}
		prev = k
	}
}

// TestEncodeArrayBTreeKeyDeclinesMultidim pins the deliberate refusal: goopg's
// array codec only ever writes ndim=1/lbound=1, so array_cmp's dimensionality
// tie-breaks cannot be reproduced and a nested literal must be declined rather
// than flattened into a key that claims an order it does not have.
func TestEncodeArrayBTreeKeyDeclinesMultidim(t *testing.T) {
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	if _, err := encodeBTreeKeyForColumn(nil, NewStringDatum("{{1,2},{3,4}}"), col, 0); err == nil {
		t.Fatal("multidimensional array literal was accepted as a B-tree key")
	} else if err.Code != "0A000" {
		t.Errorf("multidim array key error code = %s, want 0A000", err.Code)
	}
}

// TestArrayIndexBuildAndMaintainKeys is the sibling-path gate (Hard-won Rule
// #2) over the two encoders that write stored array keys: the CREATE INDEX bulk
// build and the runtime maintain path on INSERT. Both must index EVERY row —
// including the empty array — under identical bytes.
//
// The pre-fix behaviour of each half was a distinct silent defect: the bulk
// build raised 22P02 over pre-existing rows, and the maintain path wrote no
// entry at all (maintainUniqueIndexesForInsert swallows key-encode errors), so
// an array index was permanently empty and an index scan over it read as
// "no rows".
func TestArrayIndexBuildAndMaintainKeys(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	rows := []string{"{1,2}", "{10}", "{2,0}", "{}", "{1}"}
	// Maintain path: index first, rows after.
	stmts := []string{
		"CREATE TABLE arrkey_m (a int4[])",
		"CREATE INDEX arrkey_m_idx ON arrkey_m (a)",
	}
	for _, r := range rows {
		stmts = append(stmts, "INSERT INTO arrkey_m VALUES ('"+r+"')")
	}
	// Bulk-build path: rows first, index after.
	stmts = append(stmts, "CREATE TABLE arrkey_b (a int4[])")
	for _, r := range rows {
		stmts = append(stmts, "INSERT INTO arrkey_b VALUES ('"+r+"')")
	}
	stmts = append(stmts, "CREATE INDEX arrkey_b_idx ON arrkey_b (a)")
	for _, sql := range stmts {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	want := []string{"{}", "{1}", "{1,2}", "{2,0}", "{10}"} // PG array_ops order
	maintained := scanArrayIndexKeys(t, ctx, "arrkey_m_idx")
	built := scanArrayIndexKeys(t, ctx, "arrkey_b_idx")
	if len(maintained) != len(rows) {
		t.Errorf("maintain path indexed %d of %d rows (%x)", len(maintained), len(rows), maintained)
	}
	if len(built) != len(rows) {
		t.Errorf("bulk build indexed %d of %d rows (%x)", len(built), len(rows), built)
	}
	assertArrayKeysEqualLiterals(t, "maintain", maintained, want)
	assertArrayKeysEqualLiterals(t, "build", built, want)
}

// TestArrayIndexCompositeKeyIsSelfDelimiting covers the array segment as one
// column of a COMPOSITE key. Without the end marker the array bytes run
// straight into the next column's, so ('{1}',2) and ('{1,2}',0) share a byte
// prefix and the shorter array is then ordered by the SECOND column's leading
// byte — PG orders '{1}' before '{1,2}' whatever follows.
func TestArrayIndexCompositeKeyIsSelfDelimiting(t *testing.T) {
	acol := &catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	bcol := &catalog.Column{Name: "b", Type: catalog.Type{Name: "int4"}}
	compose := func(arr string, b int64) []byte {
		ak, err := encodeBTreeKeyForColumn(nil, NewStringDatum(arr), acol, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", arr, err)
		}
		bk, err := encodeBTreeKeyForColumn(nil, NewIntDatum(b), bcol, 0)
		if err != nil {
			t.Fatalf("encode %d: %v", b, err)
		}
		return append(append([]byte{}, ak...), bk...)
	}
	short := compose("{1}", 2)
	long := compose("{1,2}", 0)
	if bytes.Compare(short, long) >= 0 {
		t.Errorf("('{1}',2)=%x is not below ('{1,2}',0)=%x", short, long)
	}
}

func scanArrayIndexKeys(t *testing.T, ctx *Context, idxName string) [][]byte {
	t.Helper()
	idx := lookupIndexByName(t, ctx, idxName)
	tree, err := nbtree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("nbtree.Open(%s): %v", idxName, err)
	}
	var got [][]byte
	if err := tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		got = append(got, append([]byte{}, key...))
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan(%s): %v", idxName, err)
	}
	return got
}

// assertArrayKeysEqualLiterals checks the scanned keys are exactly the keys the
// encoder produces for wantLits, in that order — so the assertion pins both the
// set of indexed rows and the order the tree holds them in.
func assertArrayKeysEqualLiterals(t *testing.T, path string, got [][]byte, wantLits []string) {
	t.Helper()
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}}
	if len(got) != len(wantLits) {
		return // the count mismatch is already reported by the caller
	}
	for i, lit := range wantLits {
		want, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", lit, err)
		}
		if !bytes.Equal(got[i], want) {
			t.Errorf("%s path key[%d]=%x, want %x (%s)", path, i, got[i], want, lit)
		}
	}
}

// TestEncodeArrayBTreeKeyQuotedNullIsNotNull is the review/260831-2 EC-2 guard.
// The element scan used to hand encodeArrayBTreeKey the UNQUOTED text of every
// element, so `{"NULL"}` — a one-element array holding the four-character
// string — was encoded with the NULL element tag, byte-identical to `{NULL}`.
// PG keeps them apart and sorts them at opposite ends (captured from the PG
// 18.3 oracle):
//
//	select v.x::text from (values ('{}'::text[]),('{"NULL"}'),('{NULL}'),
//	                              ('{a}'),('{ZZ}')) v(x) order by v.x;
//	 {} | {"NULL"} | {ZZ} | {a} | {NULL}
//
// i.e. the string 'NULL' sorts with the other strings while a real NULL element
// sorts after all of them, and `'{NULL}' = '{"NULL"}'` is false.
func TestEncodeArrayBTreeKeyQuotedNullIsNotNull(t *testing.T) {
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "text", IsArray: true}}
	order := []string{"{}", `{"NULL"}`, "{ZZ}", "{a}", "{NULL}"}
	keys := make([][]byte, len(order))
	for i, lit := range order {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", lit, err)
		}
		keys[i] = k
	}
	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i-1], keys[i]) >= 0 {
			t.Errorf("key(%s)=%x is not below key(%s)=%x, but PG orders them that way",
				order[i-1], keys[i-1], order[i], keys[i])
		}
	}
	// The distinct-keys property spelled out: a probe for the string must not
	// land on the NULL element (a unique index would call that a duplicate).
	if bytes.Equal(keys[1], keys[4]) {
		t.Errorf(`key({"NULL"}) and key({NULL}) are identical (%x); PG reports '{NULL}' = '{"NULL"}' as false`, keys[1])
	}
	// The unquoted NULL token stays case-insensitive, as array_in has it.
	lower, err := encodeBTreeKeyForColumn(nil, NewStringDatum("{nUlL}"), col, 0)
	if err != nil {
		t.Fatalf("encode {nUlL}: %v", err)
	}
	if !bytes.Equal(lower, keys[4]) {
		t.Errorf("key({nUlL})=%x differs from key({NULL})=%x; array_in reads both as a NULL element", lower, keys[4])
	}
}
