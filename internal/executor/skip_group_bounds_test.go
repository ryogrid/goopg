package executor

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
			if _, _, ok, rerr := e.groupBoundsRaw(idx, tc.skip, entry, 0); rerr != nil || !ok {
				t.Fatalf("%s entry %d: the raw-image path declined (ok=%v err=%v)", tc.index, ei, ok, rerr)
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

// TestSkipGroupBoundsRawImagesMatchDecodedTypes extends the pin to slice 52's
// raw-image path over the key types whose images differ most: numeric
// (varlena with a weight/dscale header), date, char(4) (blank-padded), text
// (short and long varlena headers) and int, with NULL prefix attributes. The
// raw path must take every entry and form the bytes the decode-and-encode
// path forms.
func TestSkipGroupBoundsRawImagesMatchDecodedTypes(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE typed (n numeric, d date, c char(4), t text, k int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX typed_ndctk ON typed (n, d, c, t, k)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "typed_ndctk"})
	if !ok {
		t.Fatal("lookup typed_ndctk")
	}
	long := strings.Repeat("long text value ", 20)
	entries := []Row{
		{{Kind: KindNumeric, Int: 12345, Scale: 2}, NewDateDatum(time.Date(2001, 7, 13, 0, 0, 0, 0, time.UTC)), NewStringDatum("ab  "), NewStringDatum("x"), NewIntDatum(1)},
		{{Kind: KindNumeric, Int: -7, Scale: 0}, NewDateDatum(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)), NewStringDatum("abcd"), NewStringDatum(long), NewIntDatum(2)},
		{NullDatum, NullDatum, NullDatum, NullDatum, NewIntDatum(3)},
		{{Kind: KindNumeric, Int: 0, Scale: 3}, NewDateDatum(time.Date(2020, 2, 29, 0, 0, 0, 0, time.UTC)), NewStringDatum("    "), NewStringDatum(""), NewIntDatum(4)},
	}
	var e btreeSkipEnum
	if empty, err := e.init(ctx, idx, 4, []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}, nil, 0, "test"); err != nil || empty {
		t.Fatalf("init empty=%v err=%v", empty, err)
	}
	if e.desc == nil {
		t.Fatal("not a tuple-format index; nothing is exercised")
	}
	for ei, vals := range entries {
		entry, _, err := pgIndexTupleKey(e.desc, e.keyCols, vals, storage.ItemPointer{Block: 1, Offset: 2})
		if err != nil {
			t.Fatalf("entry %d: %v", ei, err)
		}
		probe, pivot, ok, rerr := e.groupBoundsRaw(idx, 4, entry, 0)
		if rerr != nil || !ok {
			t.Fatalf("entry %d: raw path declined (ok=%v err=%v)", ei, ok, rerr)
		}
		parts := make([]indexProbeKeyPart, 0, 5)
		for i := 0; i < 4; i++ {
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
			t.Fatalf("entry %d: raw probe %x pivot %x, decoded %x / %x", ei, probe, pivot, wantProbe, wantPivot)
		}
	}
}

// TestSkipGroupBoundsFixedTemplate pins M0122-0012 slice 56: when every probe
// attribute is fixed-width, groupBoundsFixed copies the entry's prefix bytes
// into per-Rescan templates instead of deforming the entry and forming two
// tuples. The prefix (smallint, bigint, date) puts alignment padding between
// attributes, so a wrong offset moves bytes. For every entry the bytes must
// be indexProbeKey's; an entry with a NULL prefix attribute (a null bitmap)
// must decline to the general path, which still forms the right bytes; and
// two groups' keys must not share memory.
func TestSkipGroupBoundsFixedTemplate(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE fixed (s smallint, b bigint, d date, k int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX fixed_sbdk ON fixed (s, b, d, k)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "fixed_sbdk"})
	if !ok {
		t.Fatal("lookup fixed_sbdk")
	}
	day := func(y int) Datum { return NewDateDatum(time.Date(y, 3, 1, 0, 0, 0, 0, time.UTC)) }
	entries := []Row{
		{NewIntDatum(1), NewIntDatum(1 << 40), day(2001), NewIntDatum(1)},
		{NewIntDatum(-2), NewIntDatum(-5), day(1970), NewIntDatum(2)},
		{NewIntDatum(32767), NewIntDatum(0), day(2038), NewIntDatum(3)},
		{NullDatum, NewIntDatum(7), day(2000), NewIntDatum(4)},
		{NewIntDatum(3), NullDatum, NullDatum, NewIntDatum(5)},
	}
	var e btreeSkipEnum
	if empty, err := e.init(ctx, idx, 3, []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}, nil, 0, "test"); err != nil || empty {
		t.Fatalf("init empty=%v err=%v", empty, err)
	}
	if e.desc == nil {
		t.Fatal("not a tuple-format index; nothing is exercised")
	}
	nkey, phys := e.desc.NKeyAtts(), e.desc.Physical()
	var prev []byte
	for ei, vals := range entries {
		entry, _, err := pgIndexTupleKey(e.desc, e.keyCols, vals, storage.ItemPointer{Block: 1, Offset: uint16(ei + 1)})
		if err != nil {
			t.Fatalf("entry %d: %v", ei, err)
		}
		// groupBoundsRaw first: it encodes the bound images the templates
		// are formed from, as walkLeaf's calls always do.
		probe, pivot, ok, rerr := e.groupBoundsRaw(idx, 3, entry, 0)
		if rerr != nil || !ok {
			t.Fatalf("entry %d: raw path declined (ok=%v err=%v)", ei, ok, rerr)
		}
		_, _, fixed := e.groupBoundsFixed(idx, 3, entry, nkey, phys, 0)
		hasNull := vals[0].IsNull() || vals[1].IsNull() || vals[2].IsNull()
		if fixed == hasNull {
			t.Fatalf("entry %d: fixed path taken=%v, want %v (NULL prefix declines)", ei, fixed, !hasNull)
		}
		parts := make([]indexProbeKeyPart, 0, 4)
		for i := 0; i < 3; i++ {
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
			t.Fatalf("entry %d: probe %x pivot %x, indexProbeKey %x / %x", ei, probe, pivot, wantProbe, wantPivot)
		}
		if prev != nil {
			saved := append([]byte(nil), prev...)
			clear(probe)
			clear(pivot)
			if !bytes.Equal(prev, saved) {
				t.Fatalf("entry %d: clearing this group's keys changed the previous group's probe", ei)
			}
		}
		if prev, _, _, rerr = e.groupBoundsRaw(idx, 3, entry, 0); rerr != nil {
			t.Fatal(rerr)
		}
	}
}
