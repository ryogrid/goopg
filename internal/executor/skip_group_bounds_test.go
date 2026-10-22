package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestSkipGroupBoundsMatchesIndexProbeKey pins M0122-0012 slice 49:
// btreeSkipEnum.groupBounds forms a group's probe (prefix || bound columns)
// and pivot (prefix alone) straight from the enumeration's own descriptor and
// key columns instead of through ctx.indexProbeKey, which re-resolved both on
// every call. The bytes must be indexProbeKey's, for every entry: an int
// prefix (items_ab, skipping a) and a two-column prefix holding a varchar
// (items_acb, skipping a and c — alignment and varlena headers in the image).
func TestSkipGroupBoundsMatchesIndexProbeKey(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE INDEX items_acb ON items (a, c, b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	entries := []Row{
		{NewIntDatum(1), NewIntDatum(10), NewStringDatum("x")},
		{NewIntDatum(2), NewIntDatum(40), NewStringDatum("q")},
		{NewIntDatum(7), NewIntDatum(50), NewStringDatum("a longer varchar value")},
		{NewIntDatum(-3), NewIntDatum(0), NewStringDatum("")},
	}
	for _, tc := range []struct {
		index string
		skip  int
		order []int // fixture row column for each index key column
	}{
		{"items_ab", 1, []int{0, 1}},
		{"items_acb", 2, []int{0, 2, 1}},
	} {
		idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: tc.index})
		if !ok {
			t.Fatalf("lookup %s", tc.index)
		}
		var e btreeSkipEnum
		bound := []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}
		if empty, err := e.init(ctx, idx, tc.skip, bound, nil, 0, "test"); err != nil || empty {
			t.Fatalf("%s: init empty=%v err=%v", tc.index, empty, err)
		}
		if e.desc == nil {
			t.Fatalf("%s: not a tuple-format index; nothing is exercised", tc.index)
		}
		for ei, r := range entries {
			vals := make([]Datum, len(tc.order))
			for i, c := range tc.order {
				vals[i] = r[c]
			}
			entry, _, err := pgIndexTupleKey(e.desc, e.keyCols, vals, storage.ItemPointer{Block: 3, Offset: 7})
			if err != nil {
				t.Fatalf("%s entry %d: %v", tc.index, ei, err)
			}
			probe, pivot, gerr := e.groupBounds(ctx, idx, tc.skip, entry, 0, "test")
			if gerr != nil {
				t.Fatalf("%s entry %d: groupBounds: %v", tc.index, ei, gerr)
			}
			parts := make([]indexProbeKeyPart, 0, tc.skip+len(e.rest))
			for i := 0; i < tc.skip; i++ {
				parts = append(parts, indexProbeKeyPart{col: e.keyCols[i], val: vals[i]})
			}
			wantProbe, perr := ctx.indexProbeKey(idx, append(parts, e.rest...))
			if perr != nil {
				t.Fatal(perr)
			}
			wantPivot, perr := ctx.indexProbeKey(idx, parts)
			if perr != nil {
				t.Fatal(perr)
			}
			if !bytes.Equal(probe, wantProbe) || !bytes.Equal(pivot, wantPivot) {
				t.Fatalf("%s entry %d: probe %x pivot %x, indexProbeKey %x / %x", tc.index, ei, probe, pivot, wantProbe, wantPivot)
			}
		}
	}
}
