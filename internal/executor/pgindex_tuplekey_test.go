package executor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-a guards.
//
// The flip (B2-c) is atomic and therefore unverifiable in halves: a tree whose
// writer and comparer disagree mis-ORDERS, and mis-ordering is invisible to
// "does the query return rows?". Everything provable BEFORE the flip should be
// proved here, and the load-bearing question is exactly one:
//
//	for every type buildPGIndexKeyDesc agrees to describe, does the image
//	encodeValuePG produces — laid out by FormPGIndexTuple under that
//	descriptor — sort in PostgreSQL's order under ComparePGIndexTuples?
//
// TestPGIndexTupleKeyOrdersEveryDescribableType is that question as a table,
// and it also asserts that the answer DIFFERS from bytes.Compare somewhere in
// the table — otherwise the whole slice would be provable by the blob path it
// replaces, and the test would not be able to tell a regression to bytewise
// ordering from success.

func tupleKeyIndex(t *testing.T, tbl *catalog.Table, cols ...string) (*btree.PGIndexKeyDesc, []*catalog.Column, *catalog.Index) {
	t.Helper()
	idx := &catalog.Index{Name: "i", Table: tbl, Method: "btree", Columns: cols}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		t.Fatalf("buildPGIndexKeyDesc(%v): %v", cols, err)
	}
	keyCols := pgIndexKeyColumns(idx)
	if len(keyCols) != len(cols) {
		t.Fatalf("pgIndexKeyColumns resolved %d of %d key columns — it must agree with the descriptor", len(keyCols), len(cols))
	}
	return desc, keyCols, idx
}

var tupleKeyHeapTID = storage.ItemPointer{Block: 7, Offset: 3}

func TestPGIndexTupleKeyOrdersEveryDescribableType(t *testing.T) {
	// Each case's values are STRICTLY ASCENDING in PostgreSQL's order for that
	// type. Values are chosen to break the encodings that would be wrong:
	// little-endian bytewise (256 before 1), sign (-1 above every positive),
	// IEEE-754 bit patterns, varlena headers, and bpchar's ignored trailing
	// blanks.
	cases := []struct {
		name string
		typ  catalog.Type
		vals []Datum
	}{
		{"bool", catalog.Type{Name: "bool"}, []Datum{NewBoolDatum(false), NewBoolDatum(true)}},
		{"char", catalog.Type{Name: "char"}, []Datum{NewStringDatum("A"), NewStringDatum("Z"), NewStringDatum("a")}},
		{"name", catalog.Type{Name: "name"}, []Datum{NewStringDatum(""), NewStringDatum("aardvark"), NewStringDatum("zebra")}},
		{"int2", catalog.Type{Name: "int2"}, []Datum{NewIntDatum(-32768), NewIntDatum(-1), NewIntDatum(0), NewIntDatum(1), NewIntDatum(256), NewIntDatum(32767)}},
		{"int4", catalog.Type{Name: "int4"}, []Datum{NewIntDatum(-2147483648), NewIntDatum(-1), NewIntDatum(0), NewIntDatum(1), NewIntDatum(256), NewIntDatum(2147483647)}},
		{"int8", catalog.Type{Name: "int8"}, []Datum{NewIntDatum(-9223372036854775808), NewIntDatum(-1), NewIntDatum(0), NewIntDatum(1), NewIntDatum(1 << 40)}},
		{"oid", catalog.Type{Name: "oid"}, []Datum{NewIntDatum(0), NewIntDatum(1), NewIntDatum(256), NewIntDatum(4000000000)}},
		{"float4", catalog.Type{Name: "float4"}, []Datum{NewStringDatum("-2.5"), NewStringDatum("-0.5"), NewStringDatum("0"), NewStringDatum("0.5"), NewStringDatum("256")}},
		{"float8", catalog.Type{Name: "float8"}, []Datum{NewStringDatum("-2.5"), NewStringDatum("0"), NewStringDatum("256"), NewStringDatum("NaN")}},
		{"bytea", catalog.Type{Name: "bytea"}, []Datum{NewBytesDatum(nil), NewBytesDatum([]byte{0x00}), NewBytesDatum([]byte{0x01}), NewBytesDatum([]byte{0x01, 0x00})}},
		{"text", catalog.Type{Name: "text"}, []Datum{NewStringDatum(""), NewStringDatum("A"), NewStringDatum("a"), NewStringDatum("ab"), NewStringDatum("b")}},
		{"varchar", catalog.Type{Name: "varchar"}, []Datum{NewStringDatum("a"), NewStringDatum("ab"), NewStringDatum("b")}},
		{"bpchar", catalog.Type{Name: "bpchar", Args: []int64{4}}, []Datum{NewStringDatum("a"), NewStringDatum("ab"), NewStringDatum("b")}},
		// numeric and uuid are deliberately ABSENT: goopg's stored image for
		// them is a text varlena, not PostgreSQL's, so the mapper refuses to
		// describe them — see TestBuildPGIndexKeyDescRefusesNonPGImages.
		{"date", catalog.Type{Name: "date"}, []Datum{NewStringDatum("1999-12-31"), NewStringDatum("2000-01-01"), NewStringDatum("2026-08-10")}},
		{"time", catalog.Type{Name: "time"}, []Datum{NewStringDatum("00:00:00"), NewStringDatum("12:34:56"), NewStringDatum("23:59:59")}},
		{"timetz", catalog.Type{Name: "timetz"}, []Datum{NewStringDatum("00:00:00+00"), NewStringDatum("12:00:00+00"), NewStringDatum("23:00:00+00")}},
		{"timestamp", catalog.Type{Name: "timestamp"}, []Datum{NewStringDatum("1999-12-31 23:59:59"), NewStringDatum("2000-01-01 00:00:00"), NewStringDatum("2026-08-10 12:00:00")}},
		{"timestamptz", catalog.Type{Name: "timestamptz"}, []Datum{NewStringDatum("1999-12-31 23:59:59+00"), NewStringDatum("2000-01-01 00:00:00+00"), NewStringDatum("2026-08-10 12:00:00+00")}},
	}

	bytewiseDisagreed := false
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.Column{Name: "k", Type: tc.typ}
			tbl := keyDescTable(c)
			desc, keyCols, _ := tupleKeyIndex(t, tbl, "k")

			keys := make([][]byte, len(tc.vals))
			for i, v := range tc.vals {
				k, hasNull, err := pgIndexTupleKey(desc, keyCols, []Datum{v}, tupleKeyHeapTID)
				if err != nil {
					t.Fatalf("value %d: pgIndexTupleKey: %v", i, err)
				}
				if hasNull {
					t.Fatalf("value %d: hasNull on a non-NULL datum", i)
				}
				keys[i] = k
			}

			// The image inside the tuple must BE encodeValuePG's image: an
			// ordering-only assertion cannot catch a layout slip (a datum
			// written at the wrong alignment still sorts correctly among its
			// own siblings).
			for i, v := range tc.vals {
				vals, isnull, err := btree.DeformPGIndexTuple(keys[i], desc.Physical(), 1)
				if err != nil {
					t.Fatalf("value %d: DeformPGIndexTuple: %v", i, err)
				}
				if isnull[0] {
					t.Fatalf("value %d: deformed as NULL", i)
				}
				want, err := encodeValuePG(tc.typ, v)
				if err != nil {
					t.Fatalf("value %d: encodeValuePG: %v", i, err)
				}
				if got := vals[0]; !imagesEquivalent(got, want) {
					t.Fatalf("value %d: deformed image %x, want encodeValuePG image %x", i, got, want)
				}
			}

			for i := range keys {
				for j := range keys {
					got, err := btree.ComparePGIndexTuples(desc, keys[i], keys[j])
					if err != nil {
						t.Fatalf("ComparePGIndexTuples(%d,%d): %v", i, j, err)
					}
					w := sign(i - j)
					if got != w {
						t.Errorf("compare(v%d, v%d) = %d, want %d (values must be strictly ascending)", i, j, got, w)
					}
					if bytes.Compare(keys[i], keys[j]) != w {
						bytewiseDisagreed = true
					}
				}
			}
		})
	}
	if !bytewiseDisagreed {
		t.Errorf("no case in the table distinguishes the opclass order from bytes.Compare — " +
			"the table cannot detect a regression to bytewise ordering")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// imagesEquivalent compares a deformed attribute image with encodeValuePG's,
// tolerating the ONE difference FormPGIndexTuple is allowed to introduce:
// pgCanMakeShort rewrites a small 4-byte-header varlena into a 1-byte-header
// one (heap_fill_tuple does the same), so the payloads must match while the
// headers may not.
func imagesEquivalent(got, wantImg []byte) bool {
	if bytes.Equal(got, wantImg) {
		return true
	}
	gp, gok := varlenaPayloadForTest(got)
	wp, wok := varlenaPayloadForTest(wantImg)
	return gok && wok && bytes.Equal(gp, wp)
}

func varlenaPayloadForTest(v []byte) ([]byte, bool) {
	if len(v) == 0 {
		return nil, false
	}
	if v[0]&0x01 == 0x01 { // 1-byte header
		n := int(v[0] >> 1)
		if n < 1 || n > len(v) {
			return nil, false
		}
		return v[1:n], true
	}
	if len(v) < 4 || v[0]&0x03 != 0x00 { // 4-byte unaligned header
		return nil, false
	}
	n := int(uint32(v[0])|uint32(v[1])<<8|uint32(v[2])<<16|uint32(v[3])<<24) >> 2
	if n < 4 || n > len(v) {
		return nil, false
	}
	return v[4:n], true
}

// numeric and uuid are the two types the B2-a ordering table found goopg does
// not store the way PostgreSQL does: `encodeValuePG` writes the decimal string
// / the 36-character canonical UUID as a TEXT varlena. The mapper must refuse
// them, because a descriptor is a promise that the 3b-2a comparator can order
// the stored image — and this test also demonstrates the damage that promise
// would do, so it FAILS (telling the next loop to drop the guard) the moment
// encodeValuePG becomes PG-faithful.
func TestBuildPGIndexKeyDescRefusesNonPGImages(t *testing.T) {
	for _, typ := range []string{"numeric", "uuid"} {
		tbl := keyDescTable(col("k", typ))
		idx := &catalog.Index{Name: "i", Table: tbl, Method: "btree", Columns: []string{"k"}}
		if _, err := buildPGIndexKeyDesc(idx); err == nil {
			t.Errorf("%s: descriptor built, but goopg's stored image is not PostgreSQL's", typ)
		} else if !strings.Contains(err.Error(), "not PostgreSQL's") {
			t.Errorf("%s: refused for the wrong reason: %v", typ, err)
		}
	}

	// numeric: the ascending values below must NOT come out ascending under the
	// real numeric comparator, because the images are ASCII.
	numType := catalog.Type{Name: "numeric"}
	ascending := []Datum{NewNumericInt64Datum(-1000, 0), NewNumericInt64Datum(-1, 0), NewNumericInt64Datum(0, 0), NewNumericInt64Datum(1, 0)}
	imgs := make([][]byte, len(ascending))
	for i, d := range ascending {
		img, err := encodeValuePG(numType, d)
		if err != nil {
			t.Fatalf("encodeValuePG(numeric): %v", err)
		}
		imgs[i] = img
	}
	ordered := true
	for i := range imgs[:len(imgs)-1] {
		if btree.PGCompareNumeric(imgs[i], imgs[i+1]) >= 0 {
			ordered = false
		}
	}
	if ordered {
		t.Errorf("PGCompareNumeric now orders goopg's numeric images correctly — encodeValuePG appears to emit PG's " +
			"base-10000 NumericData, so drop numeric from pgIndexKeyImageIsPGFaithful and re-add it to the ordering table")
	}

	// uuid: PG's image is exactly 16 bytes; goopg's is the canonical string.
	uuidImg, err := encodeValuePG(catalog.Type{Name: "uuid"}, NewStringDatum("00000000-0000-0000-0000-000000000001"))
	if err != nil {
		t.Fatalf("encodeValuePG(uuid): %v", err)
	}
	if len(uuidImg) == 16 {
		t.Errorf("encodeValuePG now emits a 16-byte uuid image — drop uuid from pgIndexKeyImageIsPGFaithful " +
			"and re-add it to the ordering table")
	}
}

// A prefix key positions a scan at the FIRST entry matching that prefix. In the
// blob format that fell out of bytewise prefix comparison; in the tuple format
// it is the truncation rule, and it only works if the prefix is stamped as a
// pivot — an unstamped short tuple would claim the index's full key count and
// DeformPGIndexTuple would read attributes that are not there.
func TestPGIndexTupleKeyPrefixIsMinusInfinityBeyondItsAttributes(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"), col("b", "text"))
	desc, keyCols, _ := tupleKeyIndex(t, tbl, "a", "b")

	full := func(a int64, b string) []byte {
		k, _, err := pgIndexTupleKey(desc, keyCols, []Datum{NewIntDatum(a), NewStringDatum(b)}, tupleKeyHeapTID)
		if err != nil {
			t.Fatalf("full key (%d,%q): %v", a, b, err)
		}
		return k
	}
	prefix, _, err := pgIndexTupleKey(desc, keyCols[:1], []Datum{NewIntDatum(5)}, storage.ItemPointer{})
	if err != nil {
		t.Fatalf("prefix key: %v", err)
	}
	if !btree.BTreeTupleIsPivot(prefix) {
		t.Fatalf("prefix key is not stamped as a pivot; BTreeTupleGetNAtts would report the index's full key count")
	}
	if n := btree.BTreeTupleGetNAtts(prefix, 2); n != 1 {
		t.Fatalf("prefix natts = %d, want 1", n)
	}

	cmp := func(a, b []byte) int {
		got, err := btree.ComparePGIndexTuples(desc, a, b)
		if err != nil {
			t.Fatalf("ComparePGIndexTuples: %v", err)
		}
		return got
	}
	if got := cmp(prefix, full(5, "")); got != -1 {
		t.Errorf("prefix(5) vs (5,'') = %d, want -1 (the prefix is minus infinity in b)", got)
	}
	if got := cmp(prefix, full(4, "zzz")); got != 1 {
		t.Errorf("prefix(5) vs (4,'zzz') = %d, want 1", got)
	}
	if got := cmp(prefix, full(6, "")); got != -1 {
		t.Errorf("prefix(5) vs (6,'') = %d, want -1", got)
	}
}

// The heap TID is the last key attribute. A search key that supplies every key
// attribute but no TID must still sort before the matching entries, so the
// descent lands on the first duplicate rather than in the middle of them.
func TestPGIndexTupleKeyZeroTIDSortsBeforeEveryDuplicate(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"))
	desc, keyCols, _ := tupleKeyIndex(t, tbl, "a")

	at := func(tid storage.ItemPointer) []byte {
		k, _, err := pgIndexTupleKey(desc, keyCols, []Datum{NewIntDatum(42)}, tid)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		return k
	}
	search := at(storage.ItemPointer{})
	for _, tid := range []storage.ItemPointer{{Block: 0, Offset: 1}, {Block: 1, Offset: 1}, {Block: 9999, Offset: 200}} {
		if got, err := btree.ComparePGIndexTuples(desc, search, at(tid)); err != nil || got != -1 {
			t.Errorf("search key vs entry at %+v = %d (err %v), want -1", tid, got, err)
		}
	}
	// …and the entries themselves are ordered by TID, so equal keys have a
	// total order (heapkeyspace).
	if got, _ := btree.ComparePGIndexTuples(desc, at(storage.ItemPointer{Block: 1, Offset: 2}), at(storage.ItemPointer{Block: 1, Offset: 3})); got != -1 {
		t.Errorf("TID tiebreak not applied: got %d, want -1", got)
	}
}

// NULLs are representable in the tuple format (bitmap + per-attribute NULLS
// FIRST/LAST), which the blob format could not do. hasNull is reported so the
// writer can keep today's "a NULL key column means the row is not indexed"
// policy until that semantic change is made deliberately (ledger row).
func TestPGIndexTupleKeyNullAndDirectionFlags(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"))
	idx := &catalog.Index{Name: "i", Table: tbl, Method: "btree", Columns: []string{"a"}}
	keyCols := pgIndexKeyColumns(idx)

	build := func(descending, nullsFirst bool) *btree.PGIndexKeyDesc {
		idx.ColDescending = []bool{descending}
		idx.ColNullsFirst = []bool{nullsFirst}
		d, err := buildPGIndexKeyDesc(idx)
		if err != nil {
			t.Fatalf("buildPGIndexKeyDesc: %v", err)
		}
		return d
	}
	keyFor := func(d *btree.PGIndexKeyDesc, v Datum) ([]byte, bool) {
		k, hasNull, err := pgIndexTupleKey(d, keyCols, []Datum{v}, tupleKeyHeapTID)
		if err != nil {
			t.Fatalf("pgIndexTupleKey: %v", err)
		}
		return k, hasNull
	}

	asc := build(false, false)
	nullKey, hasNull := keyFor(asc, Datum{})
	if !hasNull {
		t.Fatalf("hasNull not reported for a NULL key attribute")
	}
	if !btree.PGIndexTupleHasNulls(nullKey) {
		t.Fatalf("INDEX_NULL_MASK not set on a NULL-bearing key")
	}
	one, _ := keyFor(asc, NewIntDatum(1))
	if got, _ := btree.ComparePGIndexTuples(asc, nullKey, one); got != 1 {
		t.Errorf("ASC NULLS LAST: NULL vs 1 = %d, want 1", got)
	}

	nf := build(false, true)
	nullKeyNF, _ := keyFor(nf, Datum{})
	oneNF, _ := keyFor(nf, NewIntDatum(1))
	if got, _ := btree.ComparePGIndexTuples(nf, nullKeyNF, oneNF); got != -1 {
		t.Errorf("ASC NULLS FIRST: NULL vs 1 = %d, want -1", got)
	}

	// DESC inverts the opclass result but NOT the TID tiebreak (upstream keeps
	// heap TID ascending regardless of indoption).
	dsc := build(true, false)
	lo, _ := keyFor(dsc, NewIntDatum(1))
	hi, _ := keyFor(dsc, NewIntDatum(2))
	if got, _ := btree.ComparePGIndexTuples(dsc, lo, hi); got != 1 {
		t.Errorf("DESC: 1 vs 2 = %d, want 1", got)
	}
}

func TestPGIndexTupleKeyFromRowProjectsByOrdinal(t *testing.T) {
	tbl := keyDescTable(col("pad", "text"), col("a", "int4"), col("b", "text"))
	desc, keyCols, _ := tupleKeyIndex(t, tbl, "b", "a") // key order != table order

	row := Row{NewStringDatum("ignored"), NewIntDatum(9), NewStringDatum("bee")}
	got, hasNull, err := pgIndexTupleKeyFromRow(desc, keyCols, row, tupleKeyHeapTID)
	if err != nil || hasNull {
		t.Fatalf("pgIndexTupleKeyFromRow: %v (hasNull=%v)", err, hasNull)
	}
	wantKey, _, err := pgIndexTupleKey(desc, keyCols, []Datum{NewStringDatum("bee"), NewIntDatum(9)}, tupleKeyHeapTID)
	if err != nil {
		t.Fatalf("pgIndexTupleKey: %v", err)
	}
	if !bytes.Equal(got, wantKey) {
		t.Errorf("row projection %x != explicit key %x — the projection must follow key order, not table order", got, wantKey)
	}
}

func TestPGIndexTupleKeyRejectsWhatItCannotIndex(t *testing.T) {
	tbl := keyDescTable(col("a", "text"))
	desc, keyCols, _ := tupleKeyIndex(t, tbl, "a")

	if _, _, err := pgIndexTupleKey(desc, keyCols, []Datum{NewToastPointerDatum(make([]byte, 12))}, tupleKeyHeapTID); err == nil {
		t.Errorf("an unresolved TOAST pointer was accepted as a key datum; PostgreSQL detoasts before index_form_tuple")
	}
	if _, _, err := pgIndexTupleKey(desc, keyCols, []Datum{NewStringDatum(strings.Repeat("x", 4000))}, tupleKeyHeapTID); err == nil {
		t.Errorf("an over-BTMaxItemSize key was accepted; the failure must be reported where the column is still nameable")
	}
	if _, _, err := pgIndexTupleKey(desc, keyCols, nil, tupleKeyHeapTID); err == nil {
		t.Errorf("a zero-attribute key was accepted")
	}
	if _, _, err := pgIndexTupleKey(nil, keyCols, []Datum{NewStringDatum("x")}, tupleKeyHeapTID); err == nil {
		t.Errorf("a nil descriptor was accepted")
	}
}

// pgIndexKeyColumns and buildPGIndexKeyDesc must refuse the same indexes: the
// writer decides "tuple or blob" from the descriptor, and a resolved column
// list for an index with no descriptor (or the reverse) would let the two
// halves of the flip disagree about which format an index is in.
func TestPGIndexKeyColumnsAgreesWithTheDescriptor(t *testing.T) {
	tbl := keyDescTable(col("a", "int4"))
	for _, tc := range []struct {
		name string
		idx  *catalog.Index
	}{
		{"expression key", &catalog.Index{Name: "i", Table: tbl, Columns: []string{""}, ColExprs: make([]*parser.Expr, 1)}},
		{"unknown column", &catalog.Index{Name: "i", Table: tbl, Columns: []string{"nosuch"}}},
		{"no columns", &catalog.Index{Name: "i", Table: tbl}},
		{"no table", &catalog.Index{Name: "i", Columns: []string{"a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols := pgIndexKeyColumns(tc.idx)
			_, err := buildPGIndexKeyDesc(tc.idx)
			if err == nil {
				t.Fatalf("buildPGIndexKeyDesc accepted %s", tc.name)
			}
			if cols != nil {
				t.Errorf("pgIndexKeyColumns resolved %d columns for %s, which the descriptor refuses", len(cols), tc.name)
			}
		})
	}
}
