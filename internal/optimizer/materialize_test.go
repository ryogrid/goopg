package optimizer

import (
	"math"
	"testing"
)

// M0146-0010 — pins for the Materialize surface: `cost_material`
// (costsize.c:2485), `cost_rescan`'s T_Material arm (costsize.c:4703), and
// the match_unsorted_outer admission gate (joinpath.c:1890-1901) with its
// ExecMaterializesOutput exclusion (execAmi.c:640).

// TestCostMaterialExpression pins PG's arithmetic exactly:
//
//	startup passes through unchanged;
//	run += 2 * cpu_operator_cost * tuples   (build charge — the extra vs
//	cost_rescan "ensures we'll prefer materializing the smaller rel");
//	run += seq_page_cost * ceil(nbytes / BLCKSZ) when the buffer exceeds
//	the in-memory budget.
func TestCostMaterialExpression(t *testing.T) {
	cp := defaultCostParams()
	in := Cost{Startup: 3.5, Total: 53.5} // run 50

	// In-memory fit: no spill term.
	tuples, avgVar, ncols := 1000.0, 64.0, 4
	got := costMaterial(cp, in, tuples, avgVar, ncols)
	wantTotal := in.Total + 2*cp.cpuOperatorCost*tuples
	if got.Startup != in.Startup {
		t.Errorf("startup: got %v, want %v (pass-through)", got.Startup, in.Startup)
	}
	if math.Abs(got.Total-wantTotal) > 1e-9 {
		t.Errorf("total: got %v, want %v", got.Total, wantTotal)
	}

	// Spill arm: nbytes > budget adds seq_page_cost per page.
	tuples = float64(cp.workMem)/64 + 4096 // comfortably over budget at 64 B/row
	got = costMaterial(cp, in, tuples, avgVar, ncols)
	nbytes := relationByteSize(tuples, avgVar, ncols)
	if nbytes <= float64(cp.workMem) {
		t.Fatalf("fixture must spill: nbytes %v <= budget %v", nbytes, cp.workMem)
	}
	wantTotal = in.Total + 2*cp.cpuOperatorCost*tuples + cp.seqPageCost*math.Ceil(nbytes/blockSizeBytes)
	if math.Abs(got.Total-wantTotal) > 1e-9 {
		t.Errorf("spill total: got %v, want %v", got.Total, wantTotal)
	}
}

// TestMaterialRescanCostExpression pins cost_rescan's T_Material/T_Sort arm:
// cpu_operator_cost per tuple, zero startup, plus the page re-read when the
// buffer spilled. The exact-boundary case must NOT spill (PG's test is
// `nbytes > work_mem`).
func TestMaterialRescanCostExpression(t *testing.T) {
	cp := defaultCostParams()

	rows, avgVar, ncols := 500.0, 40.0, 2
	got := materialRescanCost(cp, rows, avgVar, ncols)
	if want := cp.cpuOperatorCost * rows; math.Abs(got-want) > 1e-12 {
		t.Errorf("in-memory rescan: got %v, want %v", got, want)
	}

	rows = float64(cp.workMem)/32 + 1024
	nbytes := relationByteSize(rows, 32, 1)
	got = materialRescanCost(cp, rows, 32, 1)
	want := cp.cpuOperatorCost*rows + cp.seqPageCost*math.Ceil(nbytes/blockSizeBytes)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("spilled rescan: got %v, want %v", got, want)
	}
}

// TestMaterialInnerPathForAdmission pins joinpath.c:1890-1901's gate:
// enable_material AND an unparameterised inner AND a pathtype
// ExecMaterializesOutput doesn't already cover. Every negative must return
// nil — "PG would file no matpath for this inner".
func TestMaterialInnerPathForAdmission(t *testing.T) {
	cp := defaultCostParams()
	rel := &RelOptInfo{Relids: 2}
	inner := &Path{Kind: PathSeqScan, Rel: rel, Rows: 10, Cost: Cost{Total: 42}}

	if got := materialInnerPathFor(nil, rel, cp); got != nil {
		t.Errorf("nil inner admitted: %v", got)
	}
	off := cp
	off.enableMaterial = false
	if got := materialInnerPathFor(inner, rel, off); got != nil {
		t.Errorf("enable_material=off admitted: %v", got)
	}
	paramed := &Path{Kind: PathIndexScan, Rel: rel, RequiredOuter: 1, Cost: Cost{Total: 1}}
	if got := materialInnerPathFor(paramed, rel, cp); got != nil {
		t.Errorf("parameterised inner admitted: %v", got)
	}
	for _, kind := range []PathKind{PathMaterial, PathSort, PathMemoize} {
		p := &Path{Kind: kind, Rel: rel, Rows: 5}
		if got := materialInnerPathFor(p, rel, cp); got != nil {
			t.Errorf("already-materialising %v admitted", kind)
		}
	}
	// The positive case: a plain unparameterised inner yields a PathMaterial
	// over it, carrying rows/pathkeys through and the build cost on top.
	got := materialInnerPathFor(inner, rel, cp)
	if got == nil || got.Kind != PathMaterial || len(got.Children) != 1 || got.Children[0] != inner {
		t.Fatalf("admission produced %v", got)
	}
	if got.Rows != inner.Rows {
		t.Errorf("rows: got %v, want pass-through %v", got.Rows, inner.Rows)
	}
	if got.Cost.Total <= inner.Cost.Total {
		t.Errorf("cost: got %v, want strictly above the child's %v (build charge)", got.Cost.Total, inner.Cost.Total)
	}
	if got.Cost.Startup != inner.Cost.Startup {
		t.Errorf("startup: got %v, want pass-through %v", got.Cost.Startup, inner.Cost.Startup)
	}
}

// TestExecMaterializesOutput pins execAmi.c:640-647's exclusion set on both
// path kinds and the PathPrebuilt node classes goopg maps them onto.
func TestExecMaterializesOutput(t *testing.T) {
	for _, p := range []*Path{
		{Kind: PathMaterial},
		{Kind: PathSort},
		{Kind: PathMemoize},
		{Kind: PathPrebuilt, node: &Materialize{}},
		{Kind: PathPrebuilt, node: &Sort{}},
		{Kind: PathPrebuilt, node: &CTEScan{}},
		{Kind: PathPrebuilt, node: &MaterializedCTEScan{}},
		{Kind: PathPrebuilt, node: &WorkTableScan{}},
		{Kind: PathPrebuilt, node: &ScalarFuncScan{}},
		{Kind: PathPrebuilt, node: &RowsFrom{}},
		{Kind: PathPrebuilt, node: &GenerateSeries{}},
		// Leaf paths hide their node in Rel.baseLeaf — the Q31 case: a
		// PathSeqScan over a *CTEScan leaf is T_CteScan in PG's space.
		{Kind: PathSeqScan, Rel: &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: &CTEScan{}}}},
		{Kind: PathSeqScan, Rel: &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: &Filter{Child: &CTEScan{}}}}},
	} {
		if !execMaterializesOutput(p) {
			t.Errorf("%+v should report materialising", p.Kind)
		}
	}
	for _, p := range []*Path{
		nil,
		{Kind: PathSeqScan},
		{Kind: PathIndexScan},
		{Kind: PathSeqScan, Rel: &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: &SeqScan{}}}},
		{Kind: PathPrebuilt, node: &SeqScan{}},
		{Kind: PathPrebuilt, node: &IndexScan{}},
		{Kind: PathPrebuilt, node: &Project{}},
	} {
		if execMaterializesOutput(p) {
			t.Errorf("%+v reported materialising; PG wraps these", p.Kind)
		}
	}
}

// TestCreateMaterialPlanShape pins the plan half: PathMaterial →
// *Materialize over the child's node, with the child's layout (columns are
// position-identical through the buffer).
func TestCreateMaterialPlanShape(t *testing.T) {
	rel := &RelOptInfo{Relids: 2}
	child := newPrebuiltPath(rel, &Project{})
	p := materialInnerPath(rel, child, defaultCostParams())
	if p == nil {
		t.Fatal("no path")
	}
	n, lay := createMaterialPlan(p)
	m, ok := n.(*Materialize)
	if !ok {
		t.Fatalf("createMaterialPlan returned %T, want *Materialize", n)
	}
	if _, isProject := m.Child.(*Project); !isProject {
		t.Fatalf("child is %T, want the prebuilt *Project", m.Child)
	}
	if len(lay) != len(m.Output()) {
		t.Fatalf("layout %d cols vs output %d", len(lay), len(m.Output()))
	}
}

// TestPathRescanCostTuplestoreArms pins cost_rescan's T_CteScan /
// T_WorkTableScan arm (cpu_tuple_cost per tuple, zero startup) and its
// T_FunctionScan arm (startup dropped, run cost kept). Without them a bare
// CTE inner was charged a full re-execution per outer row once the fused
// cache-replay pricing retired — TPC-DS Q31's join order moved off PG's.
// An interior path over a CTE base rel (a Unique here) keeps the default
// arm: its pathtype is its own, not the leaf's.
func TestPathRescanCostTuplestoreArms(t *testing.T) {
	cp := defaultCostParams()
	cteRel := &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: &Filter{Child: &CTEScan{}}}}
	cte := &Path{Kind: PathSeqScan, Rel: cteRel, Rows: 200, NCols: 2, Cost: Cost{Startup: 0, Total: 900}}
	st, tot := pathRescanCost(cte, cp)
	if want := tuplestoreRescanCost(cp, cte.Rows, pathAvgVarBytes(cte), pathNCols(cte)); st != 0 || math.Abs(tot-want) > 1e-12 {
		t.Errorf("CTE rescan: got (%v,%v), want (0,%v)", st, tot, want)
	}
	if want := cp.cpuTupleCost * 200; math.Abs(tot-want) > 1e-12 {
		t.Errorf("CTE rescan in-memory: got %v, want cpu_tuple_cost*rows %v", tot, want)
	}

	fn := &Path{Kind: PathPrebuilt, node: &GenerateSeries{}, Rows: 10, Cost: Cost{Startup: 7, Total: 12}}
	if st, tot := pathRescanCost(fn, cp); st != 0 || tot != 5 {
		t.Errorf("FunctionScan rescan: got (%v,%v), want (0,5)", st, tot)
	}

	uniq := &Path{Kind: PathUnique, Rel: cteRel, Rows: 50, Cost: Cost{Startup: 30, Total: 40}}
	if st, tot := pathRescanCost(uniq, cp); st != 30 || tot != 40 {
		t.Errorf("Unique over CTE rel: got (%v,%v), want default arm (30,40)", st, tot)
	}
	if execMaterializesOutput(uniq) {
		t.Errorf("Unique over CTE rel must not report materialising (T_Unique is not in ExecMaterializesOutput)")
	}
}
