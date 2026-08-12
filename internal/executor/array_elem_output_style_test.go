package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgarray"
)

// goopg flattens an array column to its "{…}" text during the HEAP DECODE,
// where upstream would defer to array_out at OUTPUT time. That is why the
// session DateStyle/TimeZone has to be carried down into the decode at all: the
// scalar date-time types keep their KindTime carrier and are formatted later,
// by internal/server's appendTypedCellText and executor's datumToCopyText,
// where the GUCs already reach — an array is the one type whose text is fixed
// before then.
//
// Expected strings are PostgreSQL 18.3's, captured live (see
// internal/pgarray/array_elem_output_style_test.go for the capture recipe).
// M0119-0006.

func tstzArrayCol() catalog.Column {
	return catalog.Column{Name: "c", Type: catalog.Type{Name: "timestamptz", IsArray: true}}
}

// TestHeapArrayDecodeHonoursSessionStyle is the end-to-end assertion for the
// deferral row this slice closes: the same stored blob, read under three
// sessions, renders the three texts PG renders.
func TestHeapArrayDecodeHonoursSessionStyle(t *testing.T) {
	col := tstzArrayCol()
	const lit = `{"2020-06-15 10:00:00+00","2020-01-15 10:00:00+00"}`
	blob, err := encodeArrayValuePG(col.Type, NewStringDatum(lit))
	if err != nil {
		t.Fatalf("heap encode: %v", err)
	}
	cases := []struct {
		name string
		st   pgarray.OutputStyle
		want string
	}{
		{"default (UTC/ISO)", pgarray.DefaultOutputStyle(),
			`{"2020-06-15 10:00:00+00","2020-01-15 10:00:00+00"}`},
		{"Asia/Kolkata ISO", pgarray.OutputStyle{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"},
			`{"2020-06-15 15:30:00+05:30","2020-01-15 15:30:00+05:30"}`},
		{"America/Los_Angeles ISO (DST both sides)", pgarray.OutputStyle{Style: "ISO", Order: "MDY", Zone: "America/Los_Angeles"},
			`{"2020-06-15 03:00:00-07","2020-01-15 02:00:00-08"}`},
		{"Postgres MDY + LA", pgarray.OutputStyle{Style: "Postgres", Order: "MDY", Zone: "America/Los_Angeles"},
			`{"Mon Jun 15 03:00:00 2020 PDT","Wed Jan 15 02:00:00 2020 PST"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, err := decodeArrayValuePGStyled(col.Type, blob, c.st)
			if err != nil {
				t.Fatalf("heap decode: %v", err)
			}
			if got := d.StringValue(); got != c.want {
				t.Errorf("heap decode = %q, want %q", got, c.want)
			}
		})
	}
}

// TestArrayKeyTextMatchesHeapTextUnderSessionStyle is the sibling guard
// (Hard-won Rule #2). An index-only scan rebuilds the array text from the INDEX
// KEY, a seq/bitmap scan from the HEAP. TestArrayKeyTextMatchesHeapText already
// pins that the two agree under the default style; threading the session GUCs
// into one path and not the other would make the SAME row print differently
// depending on which plan the planner picked — visible only under a non-default
// session, which is exactly the case no existing test covered.
func TestArrayKeyTextMatchesHeapTextUnderSessionStyle(t *testing.T) {
	col := tstzArrayCol()
	const lit = `{"2020-06-15 10:00:00+00","2020-01-15 10:00:00+00"}`
	blob, err := encodeArrayValuePG(col.Type, NewStringDatum(lit))
	if err != nil {
		t.Fatalf("heap encode: %v", err)
	}
	key, encErr := encodeBTreeKeyForColumn(NewStringDatum(lit), &col, 0)
	if encErr != nil {
		t.Fatalf("key encode: %s", encErr.Message)
	}
	for _, st := range []pgarray.OutputStyle{
		pgarray.DefaultOutputStyle(),
		{Style: "ISO", Order: "MDY", Zone: "Asia/Kolkata"},
		{Style: "German", Order: "DMY", Zone: "America/Los_Angeles"},
		{Style: "Postgres", Order: "MDY", Zone: "Asia/Kathmandu"},
	} {
		heapDatum, _, err := decodeArrayValuePGStyled(col.Type, blob, st)
		if err != nil {
			t.Fatalf("%+v: heap decode: %v", st, err)
		}
		keyDatum, err := decodeBTreeKeyToDatumStyled(key, col, st)
		if err != nil {
			t.Fatalf("%+v: key decode: %v", st, err)
		}
		if fromHeap, fromKey := heapDatum.StringValue(), keyDatum.StringValue(); fromHeap != fromKey {
			t.Errorf("style %+v: index-only scan would print %q where a seq scan prints %q",
				st, fromKey, fromHeap)
		}
	}
}

// TestArrayOutputStyleReadsTheSameGUCsAsCopy pins the resolver against the
// spellings and boot defaults RunCopyTo uses (internal/executor/copy.go): a
// COPY … TO and a SELECT of the same array column must print the same text, so
// the two must not disagree about which GUC names they read.
func TestArrayOutputStyleReadsTheSameGUCs(t *testing.T) {
	if got := arrayOutputStyle(nil); got != pgarray.DefaultOutputStyle() {
		t.Errorf("nil ctx = %+v, want the boot default %+v", got, pgarray.DefaultOutputStyle())
	}
	ctx := &Context{GetSetting: func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return "German, DMY", true
		case "timezone":
			return "Asia/Kolkata", true
		}
		return "", false
	}}
	got := arrayOutputStyle(ctx)
	want := pgarray.OutputStyle{Style: "German", Order: "DMY", Zone: "Asia/Kolkata"}
	if got != want {
		t.Errorf("arrayOutputStyle = %+v, want %+v", got, want)
	}
	// A session that has set neither GUC must land on the boot default, not on
	// a zero-valued style (whose empty Style would still format as ISO but
	// whose empty Order would be a silent behaviour change for SQL/Postgres).
	bare := &Context{GetSetting: func(string) (string, bool) { return "", false }}
	if got := arrayOutputStyle(bare); got != pgarray.DefaultOutputStyle() {
		t.Errorf("unset GUCs = %+v, want %+v", got, pgarray.DefaultOutputStyle())
	}
}

// TestColsHaveArrayGatesTheGUCLookup pins the cost guard: a scan of a relation
// without an array column must not resolve the style at all, so the ordinary
// TPC-H-shaped scan pays nothing for this slice.
func TestColsHaveArrayGatesTheGUCLookup(t *testing.T) {
	scalar := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "timestamptz"}},
	}
	if colsHaveArray(scalar) {
		t.Error("colsHaveArray = true for an array-free relation")
	}
	if !colsHaveArray(append(scalar, tstzArrayCol())) {
		t.Error("colsHaveArray = false for a relation with an array column")
	}
}
