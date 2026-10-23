package executor

import (
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// shapeStrings are string values whose shape makes compareDatum guess — rows,
// arrays, pg_lsn values, uuids in two spellings — beside plain and padded
// strings, so a comparator that skips the shape rules for character-string
// keys is checked exactly where the rules would have applied.
var shapeStrings = []string{
	"", "a", "ab", "ab  ", "(1,2)", "(01,2)", "(10,2)", "{1,2}", "{10}", "{2}",
	"0/16B3748", "0/A", "1/0", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
	"A0EEBC999C0B4EF8BB6D6BB9BD380A11", "zz",
}

// TestSortStringKeyFastPathMatchesCompareDatumTyped pins M0122-0012 slice 60:
// sortOp.lessKeyVals compares two strings of a character-string key with
// strings.Compare directly, and compareMergeKeys does the same for a key its
// text flags mark. Both must order every pair exactly as the calls they skip
// — compareDatumTyped / compareDatumPlain — and a key that is NOT a character
// string must keep compareDatum's shape rules (rows element-wise, pg_lsn
// numerically, uuids normalised).
func TestSortStringKeyFastPathMatchesCompareDatumTyped(t *testing.T) {
	text := &optimizer.ColumnRef{Index: 0, Name: "t", Type: catalog.Type{Name: "text"}}
	opaque := &optimizer.ColumnRef{Index: 0, Name: "r", Type: catalog.Type{Name: "record"}}
	for _, k := range []*optimizer.ColumnRef{text, opaque} {
		for _, desc := range []bool{false, true} {
			keys := []optimizer.SortKey{{Expr: k, Desc: desc}}
			o := &sortOp{keys: keys, keyText: sortKeyTextFlags(keys)}
			for _, as := range shapeStrings {
				for _, bs := range shapeStrings {
					a, b := NewStringDatum(as), NewStringDatum(bs)
					want, err := compareDatumTyped(a, b, 0, k)
					if err != nil {
						t.Fatalf("compareDatumTyped(%q, %q): %v", as, bs, err)
					}
					if desc {
						want = -want
					}
					if got := o.lessKeyVals([]Datum{a}, []Datum{b}); got != (want < 0) {
						t.Fatalf("%s desc=%v: lessKeyVals(%q, %q) = %v, compareDatumTyped says %d", k.Type.Name, desc, as, bs, got, want)
					}
					plain := k == text
					wantPlain, _ := compareDatumPlain(a, b, 0, plain)
					gotPlain, err := compareMergeKeys([]Datum{a}, []Datum{b}, 0, []bool{plain})
					if err != nil || sign(gotPlain) != sign(wantPlain) {
						t.Fatalf("%s: compareMergeKeys(%q, %q) = %d (%v), compareDatumPlain %d", k.Type.Name, as, bs, gotPlain, err, wantPlain)
					}
				}
			}
		}
	}
	if !textShapeAmbiguous(NewStringDatum("(1,2)"), NewStringDatum("(01,2)")) {
		t.Fatal("fixture lost its shape-ambiguous pairs")
	}
}

// TestDistinctOnKeyMatchesConcatenatedDatumKeys pins slice 60's DISTINCT ON
// key: appendDistinctOnKey must produce the bytes the pre-slice Next built by
// concatenating datumKey(trimStringDatum-if-bpchar(v)) + NUL per key column,
// over every value kind a key takes, with and without the bpchar trim, and
// with an out-of-range key column skipped as before.
func TestDistinctOnKeyMatchesConcatenatedDatumKeys(t *testing.T) {
	rng := rand.New(rand.NewSource(60))
	bigM, _ := new(big.Int).SetString("98765432109876543210", 10)
	pick := func() Datum {
		switch rng.Intn(9) {
		case 0:
			return NullDatum
		case 1:
			return NewBoolDatum(rng.Intn(2) == 0)
		case 2:
			return NewIntDatum(int64(rng.Intn(5) - 2))
		case 3:
			return Datum{Kind: KindNumeric, Int: int64(rng.Intn(500)), Scale: int16(rng.Intn(3))}
		case 4:
			return newNumeric(bigM, rng.Intn(3))
		case 5:
			return NewStringDatum(shapeStrings[rng.Intn(len(shapeStrings))])
		case 6:
			return NewBytesDatum([]byte{byte(rng.Intn(3)), 0})
		case 7:
			return NewTimeDatum(time.Unix(int64(rng.Intn(3)*86400), 0).UTC())
		default:
			return NewIntervalDatumFull(int32(rng.Intn(3)), 0, int64(rng.Intn(2)))
		}
	}
	for trial := 0; trial < 3000; trial++ {
		o := &distinctOnOp{
			plan:     &optimizer.DistinctOn{KeyCols: []int{2, 0, 5, 1}},
			keyTrims: []bool{rng.Intn(2) == 0, false, true, rng.Intn(2) == 0},
		}
		row := Row{pick(), pick(), pick()}
		var want string
		for ki, idx := range o.plan.KeyCols {
			if idx >= 0 && idx < len(row) {
				v := row[idx]
				if o.keyTrims[ki] {
					v = trimStringDatum(v)
				}
				want += datumKey(v) + "\x00"
			}
		}
		if got := string(o.appendDistinctOnKey(nil, row)); got != want {
			t.Fatalf("row %v trims %v: appendDistinctOnKey %q, concatenated %q", row, o.keyTrims, got, want)
		}
	}
}
