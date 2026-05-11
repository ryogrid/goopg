package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestEstimateJoinCostInferredEdgePenalty pins that the
// M0076-0004 cost penalty multiplier is applied when
// `joinEdge.isInferred = true`. Same operands → cost
// should be larger (by inferredEdgePenalty) for inferred
// edges.
func TestEstimateJoinCostInferredEdgePenalty(t *testing.T) {
	tbl := &catalog.Table{
		Name: "t",
		Stats: &catalog.TableStats{
			RowCount: 1000,
			Columns: []catalog.ColumnStats{
				{NDistinct: 100},
			},
		},
	}
	g := &joinGraph{
		nodes:  2,
		tables: []*catalog.Table{tbl, tbl},
	}
	explicitEdge := &joinEdge{leftTable: 0, rightTable: 1, isInferred: false}
	inferredEdge := &joinEdge{leftTable: 0, rightTable: 1, isInferred: true}

	_, costExplicit := estimateJoinCost(1000, 1000, explicitEdge, g, nil)
	_, costInferred := estimateJoinCost(1000, 1000, inferredEdge, g, nil)

	if costInferred <= costExplicit {
		t.Errorf("inferred-edge cost should be > explicit; got %d vs %d", costInferred, costExplicit)
	}
	// Sanity: inferred ≈ explicit × inferredEdgePenalty.
	want := int64(float64(costExplicit) * inferredEdgePenalty)
	if costInferred != want {
		t.Errorf("inferred cost = %d, want %d (explicit×penalty)", costInferred, want)
	}
}

// TestEstimateJoinCostExplicitUnaffected pins the explicit-
// edge contract for the M0077-0003 3-part formula:
//
//	outputRows = (L*R)/maxNDV    = (100*100)/10 = 1000
//	buildRows  = min(L,R)        = 100
//	probeRows  = max(L,R)        = 100
//	cost = 1000*1 + 100*4 + 100*1 = 1500
func TestEstimateJoinCostExplicitUnaffected(t *testing.T) {
	tbl := &catalog.Table{
		Name: "t",
		Stats: &catalog.TableStats{
			RowCount: 100,
			Columns: []catalog.ColumnStats{
				{NDistinct: 10},
			},
		},
	}
	g := &joinGraph{
		nodes:  2,
		tables: []*catalog.Table{tbl, tbl},
	}
	edge := &joinEdge{leftTable: 0, rightTable: 1}
	out, cost := estimateJoinCost(100, 100, edge, g, nil)
	if out != 1000 {
		t.Errorf("outputRows = %d, want 1000 (10000/10)", out)
	}
	wantCost := int64(1000*outputRowWeight + 100*hashBuildWeight + 100*hashProbeWeight)
	if cost != wantCost {
		t.Errorf("explicit edge cost = %d, want %d (3-part: out + build*4 + probe)", cost, wantCost)
	}
}

// TestEquivClassClassesDeterministic pins the M0076-0004
// determinism fix: the synthesised-conjunct sequence
// from `inferTransitiveEqualities` is reproducible across
// runs (Go map iteration randomisation defused).
func TestEquivClassClassesDeterministic(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int4")
	d := makeColRef("d", 4, "int4")
	conjuncts := []Expr{
		makeEqExpr(a, b),
		makeEqExpr(b, c),
		makeEqExpr(c, d),
	}
	// 100 invocations should all produce identical output
	// (same length + same per-position pair identity).
	first := inferTransitiveEqualities(conjuncts)
	for i := 0; i < 99; i++ {
		got := inferTransitiveEqualities(conjuncts)
		if len(got) != len(first) {
			t.Fatalf("invocation %d: len = %d, want %d", i, len(got), len(first))
		}
		for j := range got {
			fl, fr, _ := isColumnRefEquality(first[j])
			gl, gr, _ := isColumnRefEquality(got[j])
			fla, flb := identOf(fl), identOf(fr)
			gla, glb := identOf(gl), identOf(gr)
			if fla != gla || flb != glb {
				t.Errorf("invocation %d position %d: differs from first run", i, j)
			}
		}
	}
}

// TestBuildJoinGraphInferredCount pins that the trailing
// `inferredCount` conjuncts are tagged isInferred=true on
// their resulting edges. (M0076-0004.)
func TestBuildJoinGraphInferredCount(t *testing.T) {
	tbl1 := &catalog.Table{
		Name: "t1",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	tbl2 := &catalog.Table{
		Name: "t2",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	tbl3 := &catalog.Table{
		Name: "t3",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	tables := []*catalog.Table{tbl1, tbl2, tbl3}

	scans := []Node{
		&SeqScan{Table: tbl1, schema: Schema{{Name: "id", Type: catalog.Type{Name: "int4"}}}},
		&SeqScan{Table: tbl2, schema: Schema{{Name: "id", Type: catalog.Type{Name: "int4"}}}},
		&SeqScan{Table: tbl3, schema: Schema{{Name: "id", Type: catalog.Type{Name: "int4"}}}},
	}
	scanWidth := []int{1, 1, 1}

	// Three explicit equalities: t1.id = t2.id, t2.id = t3.id, t1.id = t3.id (last is inferred).
	t1id := &ColumnRef{Name: "id", Index: 0, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1}
	t2id := &ColumnRef{Name: "id", Index: 1, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2}
	t3id := &ColumnRef{Name: "id", Index: 2, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 3}

	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: t1id, Right: t2id},
		&BinaryOp{Op: parser.OpEq, Left: t2id, Right: t3id},
		&BinaryOp{Op: parser.OpEq, Left: t1id, Right: t3id}, // synthesised
	}

	// inferredCount=1 → only the LAST conjunct is tagged inferred.
	g := buildJoinGraph(tables, scans, scanWidth, conjuncts, 1, nil)
	if g == nil {
		t.Fatal("buildJoinGraph returned nil")
	}
	if len(g.edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(g.edges))
	}
	// Edge 0,1 should be explicit; edge 2 should be inferred.
	if g.edges[0].isInferred {
		t.Errorf("edge 0 incorrectly marked isInferred")
	}
	if g.edges[1].isInferred {
		t.Errorf("edge 1 incorrectly marked isInferred")
	}
	if !g.edges[2].isInferred {
		t.Errorf("edge 2 should be marked isInferred")
	}
}

// TestBuildJoinGraphInferredCountZeroIsHistorical pins that
// passing inferredCount=0 (the historical pre-M0076 call
// pattern) marks ALL edges as explicit. Backward-compat.
func TestBuildJoinGraphInferredCountZeroIsHistorical(t *testing.T) {
	tbl1 := &catalog.Table{
		Name:    "t1",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	tbl2 := &catalog.Table{
		Name:    "t2",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	tables := []*catalog.Table{tbl1, tbl2}
	scans := []Node{
		&SeqScan{Table: tbl1, schema: Schema{{Name: "id", Type: catalog.Type{Name: "int4"}}}},
		&SeqScan{Table: tbl2, schema: Schema{{Name: "id", Type: catalog.Type{Name: "int4"}}}},
	}
	scanWidth := []int{1, 1}

	t1id := &ColumnRef{Name: "id", Index: 0, Type: catalog.Type{Name: "int4"}}
	t2id := &ColumnRef{Name: "id", Index: 1, Type: catalog.Type{Name: "int4"}}

	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: t1id, Right: t2id},
	}

	g := buildJoinGraph(tables, scans, scanWidth, conjuncts, 0, nil)
	if g == nil || len(g.edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.edges))
	}
	if g.edges[0].isInferred {
		t.Errorf("inferredCount=0 should leave ALL edges as explicit")
	}
}
