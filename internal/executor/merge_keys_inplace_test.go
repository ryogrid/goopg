package executor

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// mergeKeysLessByCopy is mergeKeysLess as it was before M0122-0012 C30:
// both Datums copied, fastCompareDatum, then compareDatumTyped for every
// other pair. The in-place comparator with the character-string arm must
// order every pair exactly as it did.
func mergeKeysLessByCopy(keys []optimizer.SortKey, a, b []Datum) bool {
	for i, k := range keys {
		av, bv := a[i], b[i]
		if av.IsNull() && !bv.IsNull() {
			return k.NullsFirst
		}
		if !av.IsNull() && bv.IsNull() {
			return !k.NullsFirst
		}
		if av.IsNull() && bv.IsNull() {
			continue
		}
		cmp, ok := fastCompareDatum(&av, &bv)
		if !ok {
			cmp, _ = compareDatumTyped(av, bv, 0, k.Expr)
		}
		if cmp == 0 {
			continue
		}
		if k.Desc {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

// TestMergeKeysLessInPlaceMatchesCopy pins M0122-0012 C30 over text, bpchar,
// int and numeric keys with NULLs, under every Desc / NullsFirst
// combination: the merge comparator Gather Merge and the ordered-merge
// setOp share must keep ordering exactly as before, and as sortOp's
// lessKeyVals does.
func TestMergeKeysLessInPlaceMatchesCopy(t *testing.T) {
	texts := []string{"", "a", "ab", "abc", "b", "Books", "Electronics", "é", "ab\x00", "1", "10", "9", "{1,2}", "{10}", "(1,2)", "(1,10)", "0/1A", "0/2", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"}
	rng := rand.New(rand.NewSource(20261027))
	val := func(kind int) Datum {
		if rng.Intn(6) == 0 {
			return NullDatum
		}
		switch kind {
		case 0, 3:
			return NewStringDatum(texts[rng.Intn(len(texts))])
		case 1:
			return NewIntDatum(int64(rng.Intn(5) - 2))
		default:
			return Datum{Kind: KindNumeric, Int: int64(rng.Intn(400) - 200), Scale: int16(rng.Intn(3))}
		}
	}
	bpchar := catalog.Type{Name: "bpchar", Args: []int64{10}}
	for mask := 0; mask < 16; mask++ {
		keys := []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0, Type: catalog.Type{Name: "text"}}, Desc: mask&1 != 0, NullsFirst: mask&2 != 0},
			{Expr: &optimizer.ColumnRef{Index: 1, Type: catalog.Type{Name: "int4"}}, Desc: mask&4 != 0, NullsFirst: mask&1 != 0},
			{Expr: &optimizer.ColumnRef{Index: 2, Type: catalog.Type{Name: "numeric"}}, Desc: mask&2 != 0},
			{Expr: &optimizer.ColumnRef{Index: 3, Type: bpchar}, Desc: mask&8 != 0, NullsFirst: mask&8 == 0},
		}
		keyText := sortKeyTextFlags(keys)
		if !keyText[0] {
			t.Fatalf("text key not flagged as a character string: %v", keyText)
		}
		rows := make([][]Datum, 50)
		for i := range rows {
			rows[i] = []Datum{val(0), val(1), val(2), val(3)}
		}
		var errp error
		for i := range rows {
			for j := range rows {
				if got, want := mergeKeysLess(keys, keyText, rows[i], rows[j], &errp), mergeKeysLessByCopy(keys, rows[i], rows[j]); got != want {
					t.Fatalf("mask %d: less(%v, %v) = %v, want %v", mask, rows[i], rows[j], got, want)
				}
			}
		}
		if errp != nil {
			t.Fatalf("mask %d: err %v", mask, errp)
		}
	}
}
