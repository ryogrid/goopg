package executor

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// lessKeyValsByCopy is lessKeyVals as it was before M0122-0012 C26: both
// Datums copied, fastCompareDatum tried first, then the string arm, then
// compareDatumTyped. The in-place comparator must order exactly as it did.
func lessKeyValsByCopy(o *sortOp, a, b []Datum) bool {
	for i, k := range o.keys {
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
		if !ok && i < len(o.keyText) && o.keyText[i] && av.Kind == KindString && bv.Kind == KindString {
			cmp, ok = strings.Compare(av.StringValue(), bv.StringValue()), true
		}
		if !ok {
			cmp, _ = compareDatumTyped(av, bv, k.Expr.Pos(), k.Expr)
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

// TestLessKeyValsInPlaceMatchesCopy pins M0122-0012 C26: the comparator that
// reads the key Datums through pointers orders every pair as the copying one
// did — over text keys (empty strings, shared prefixes, a non-ASCII byte),
// int and numeric keys at mixed scales, NULLs, and every Desc / NullsFirst
// combination.
func TestLessKeyValsInPlaceMatchesCopy(t *testing.T) {
	texts := []string{"", "a", "ab", "abc", "b", "Books", "Electronics", "é", "ab\x00"}
	rng := rand.New(rand.NewSource(20261027))
	val := func(kind int) Datum {
		if rng.Intn(6) == 0 {
			return NullDatum
		}
		switch kind {
		case 0:
			return NewStringDatum(texts[rng.Intn(len(texts))])
		case 1:
			return NewIntDatum(int64(rng.Intn(5) - 2))
		default:
			return Datum{Kind: KindNumeric, Int: int64(rng.Intn(400) - 200), Scale: int16(rng.Intn(3))}
		}
	}
	text := catalog.Type{Name: "text"}
	for mask := 0; mask < 8; mask++ {
		keys := []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0, Type: text}, Desc: mask&1 != 0, NullsFirst: mask&2 != 0},
			{Expr: &optimizer.ColumnRef{Index: 1, Type: catalog.Type{Name: "int4"}}, Desc: mask&4 != 0, NullsFirst: mask&1 != 0},
			{Expr: &optimizer.ColumnRef{Index: 2, Type: catalog.Type{Name: "numeric"}}, Desc: mask&2 != 0},
		}
		o := &sortOp{keys: keys, keyText: []bool{true, false, false}}
		rows := make([][]Datum, 60)
		for i := range rows {
			rows[i] = []Datum{val(0), val(1), val(2)}
		}
		for i := range rows {
			for j := range rows {
				if got, want := o.lessKeyVals(rows[i], rows[j]), lessKeyValsByCopy(o, rows[i], rows[j]); got != want {
					t.Fatalf("mask %d: less(%v, %v) = %v, want %v", mask, rows[i], rows[j], got, want)
				}
			}
		}
		if o.sortErr != nil {
			t.Fatalf("mask %d: sortErr %v", mask, o.sortErr)
		}
	}
}
