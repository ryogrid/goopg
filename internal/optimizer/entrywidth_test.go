package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor/hashsize"
)

// ordersLikeRel is the witness the fix was measured on: a 9-column relation
// whose ANALYZEd variable payload lives entirely in four columns the build
// side does not retain (TPC-H `orders`, SF=1 — o_comment 50, o_clerk 15,
// o_orderpriority 8, o_orderstatus 1, everything else 0).
func ordersLikeRel() *RelOptInfo {
	widths := map[string]float64{
		"o_orderkey": 0, "o_custkey": 0, "o_orderstatus": 1,
		"o_totalprice": 0, "o_orderdate": 0, "o_orderpriority": 8,
		"o_clerk": 15, "o_shippriority": 0, "o_comment": 50,
	}
	var sum float64
	for _, w := range widths {
		sum += w
	}
	return &RelOptInfo{Relids: 1, NCols: len(widths), AvgVarBytes: sum, ColVarBytes: widths}
}

func schemaOf(names ...string) Schema {
	s := make(Schema, len(names))
	for i, n := range names {
		s[i] = SchemaColumn{Name: n}
	}
	return s
}

func TestBuildAvgVarBytesFollowsTheRetainedSchema(t *testing.T) {
	rel := ordersLikeRel()
	build := &Path{Kind: PathPrebuilt, Rel: rel}

	// Un-narrowed: every column retained, so the answer is the rel-wide sum
	// and nothing changes for a build that keeps its whole row.
	full := buildAvgVarBytes(build, schemaOf("o_orderkey", "o_custkey", "o_orderstatus",
		"o_totalprice", "o_orderdate", "o_orderpriority", "o_clerk", "o_shippriority", "o_comment"))
	if full != rel.AvgVarBytes {
		t.Fatalf("un-narrowed build: got %v, want the rel-wide sum %v", full, rel.AvgVarBytes)
	}

	// Narrowed to the two columns `narrowBuildInput` keeps on Q9: both are
	// fixed-width, so the retained payload is zero and the entry is exactly
	// the Datum array plus its slice header — the 120 B/row the executor's
	// own `Memory Usage:` accounting measures.
	got := buildAvgVarBytes(build, schemaOf("o_orderdate", "o_orderkey"))
	if got != 0 {
		t.Fatalf("narrowed build: got %v of variable payload, want 0", got)
	}
	if entry := hashsize.EntryBytes(2, got); entry != 2*hashsize.DatumBytes+hashsize.RowSliceBytes {
		t.Fatalf("narrowed entry = %v, want %v", entry, 2*hashsize.DatumBytes+hashsize.RowSliceBytes)
	}

	// A retained text column still costs its own width, and only its own.
	if got := buildAvgVarBytes(build, schemaOf("o_orderkey", "o_comment")); got != 50 {
		t.Fatalf("retained o_comment: got %v, want 50", got)
	}
}

func TestBuildAvgVarBytesDeclinesUpward(t *testing.T) {
	rel := ordersLikeRel()
	build := &Path{Kind: PathPrebuilt, Rel: rel}

	// A column the statistics do not describe (a computed Project target, a
	// subquery output) must not be charged zero: the whole answer falls back
	// to the rel-wide sum, which OVER-states the entry. Under-stating is the
	// dangerous direction — it makes the geometry believe a build fits.
	if got := buildAvgVarBytes(build, schemaOf("o_orderkey", "amount")); got != rel.AvgVarBytes {
		t.Fatalf("unattributed column: got %v, want the rel-wide sum %v", got, rel.AvgVarBytes)
	}
	// No per-column statistics at all: same fallback.
	bare := &Path{Kind: PathPrebuilt, Rel: &RelOptInfo{Relids: 1, AvgVarBytes: 74}}
	if got := buildAvgVarBytes(bare, schemaOf("o_orderkey")); got != 74 {
		t.Fatalf("no ColVarBytes: got %v, want 74", got)
	}
	// No rel at all is "unknown", the zero every non-search Join carries.
	if got := buildAvgVarBytes(&Path{Kind: PathPrebuilt}, schemaOf("x")); got != 0 {
		t.Fatalf("relless path: got %v, want 0", got)
	}
}

func TestTableColVarBytesAlignsWithStats(t *testing.T) {
	tbl := &catalog.Table{
		Columns: []catalog.Column{{Name: "A_Key"}, {Name: "a_text"}, {Name: "a_unanalyzed"}},
		Stats: &catalog.TableStats{Columns: []catalog.ColumnStats{
			{AvgWidth: 0}, {AvgWidth: 42},
		}},
	}
	m := tableColVarBytes(tbl)
	if len(m) != 2 {
		t.Fatalf("got %d entries, want 2 (the third column has no statistics row)", len(m))
	}
	if m["a_key"] != 0 || m["a_text"] != 42 {
		t.Fatalf("got %v, want a_key=0 a_text=42 (names lower-cased)", m)
	}
	if _, ok := m["a_unanalyzed"]; ok {
		t.Fatal("a column with no statistics row must be ABSENT, not zero — absence declines, zero discounts")
	}
	if tableColVarBytes(nil) != nil {
		t.Fatal("nil table must produce no map")
	}
}

func TestUnionColVarBytesKeepsTheWiderWidth(t *testing.T) {
	a := map[string]float64{"l_comment": 27, "l_orderkey": 0}
	b := map[string]float64{"l_comment": 33, "n_name": 7}
	m := unionColVarBytes(a, b)
	if m["l_comment"] != 33 {
		t.Fatalf("collision: got %v, want the wider 33", m["l_comment"])
	}
	if m["l_orderkey"] != 0 || m["n_name"] != 7 {
		t.Fatalf("union lost an input: %v", m)
	}
	if got := unionColVarBytes(nil, b); len(got) != len(b) {
		t.Fatalf("nil-left union: got %v", got)
	}
	if got := unionColVarBytes(a, nil); len(got) != len(a) {
		t.Fatalf("nil-right union: got %v", got)
	}
}
