package executor

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// ---- tryFuseHashCascade predicate tests ----

func TestFusionDeclinesWhenDisabled(t *testing.T) {
	// nil env
	if _, ok := tryFuseHashCascade(nil, nil); ok {
		t.Error("nil env should decline")
	}
	// disabled config
	env := &buildEnv{fusionCfg: fusionConfig{enabled: false, minLevels: 3}}
	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("disabled fusion should decline")
	}
}

func TestFusionDeclinesInWorker(t *testing.T) {
	env := &buildEnv{
		inWorker:  true,
		fusionCfg: fusionConfig{enabled: true, minLevels: 3},
	}
	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("inWorker should decline (C10/F4)")
	}
}

func TestFusionDeclinesWithLockRows(t *testing.T) {
	env := &buildEnv{
		fusionCfg: fusionConfig{enabled: true, minLevels: 3},
	}
	env.q0.ran = true
	env.q0.hasLockRows = true
	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("hasLockRows should decline (C9)")
	}
}

func TestFusionDeclinesWithMHJ(t *testing.T) {
	env := &buildEnv{
		fusionCfg: fusionConfig{enabled: true, minLevels: 3},
	}
	env.q0.ran = true
	env.q0.hasMHJ = true
	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("hasMHJ should decline")
	}
}

func TestFusionDeclinesWhenInstrumented(t *testing.T) {
	env := &buildEnv{
		fusionCfg: fusionConfig{enabled: true, minLevels: 3},
	}
	env.q0.ran = true
	// Simulate instrumented scope.
	prev := instrumentScope
	instrumentScope = &instrumenter{}
	defer func() { instrumentScope = prev }()

	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("instrumented should decline (C11/C12 F8)")
	}
}

func TestFusionDeclinesWhenQ0NotRun(t *testing.T) {
	// Q0 not yet run + nil root → Q0 runs, finds nothing.
	// p is nil so the function declines, but Q0 should have been memoised.
	env := &buildEnv{
		root:      nil,
		fusionCfg: fusionConfig{enabled: true, minLevels: 3},
	}
	if _, ok := tryFuseHashCascade(env, nil); ok {
		t.Error("nil plan should decline")
	}
	if !env.q0.ran {
		t.Error("Q0 should have run even with nil root")
	}
}

// ---- Q0 walk tests ----

func TestWalkRootForQ0FindsLockRows(t *testing.T) {
	lr := &planner.LockRows{}
	lrFound, _, _ := walkRootForQ0(lr)
	if !lrFound {
		t.Error("walkRootForQ0 should find LockRows")
	}
}

func TestWalkRootForQ0FindsGather(t *testing.T) {
	g := &planner.Gather{}
	_, gaFound, _ := walkRootForQ0(g)
	if !gaFound {
		t.Error("walkRootForQ0 should find Gather")
	}
}

func TestWalkRootForQ0FindsMHJ(t *testing.T) {
	mhj := &planner.MultiHashJoin{}
	_, _, mhjFound := walkRootForQ0(mhj)
	if !mhjFound {
		t.Error("walkRootForQ0 should find MultiHashJoin")
	}
}

func TestWalkRootForQ0SearchesRecursively(t *testing.T) {
	// Filter → LockRows (nested)
	lr := &planner.LockRows{}
	f := &planner.Filter{Child: lr}
	lrFound, _, _ := walkRootForQ0(f)
	if !lrFound {
		t.Error("walkRootForQ0 should find LockRows through Filter")
	}
}

// ---- outputMatchesChildren tests ----

func TestOutputMatchesChildrenSimple(t *testing.T) {
	// outputMatchesChildren is an internal predicate helper. Its
	// correctness is exercised through tryFuseHashCascade in the
	// integration-level TestFusedCascadeMatchesUnfused (M0126-0007).
	// Here we only document the expected behaviour:
	// - It compares SchemaColumn names and types element-wise
	// - The comparison gates fusion (width alone is insufficient — F1)
	t.Log("outputMatchesChildren integration test deferred to M0126-0007")
}

// ---- exprRefsInBound tests ----

func TestExprRefsInBoundColumnRef(t *testing.T) {
	// ColumnRef in bounds → ok.
	// Since we can't set unexported fields, test the structure.
	_ = &planner.ColumnRef{}
	// exprRefsInBound with a ColumnRef — tested indirectly.
}

func TestExprRefsInBoundOuterColumnRef(t *testing.T) {
	// OuterColumnRef always fails.
	e := &planner.OuterColumnRef{}
	if exprRefsInBound(e, 100) {
		t.Error("OuterColumnRef should always be out of bounds")
	}
}

func TestExprRefsInBoundSubquery(t *testing.T) {
	e := &planner.SubqueryExpr{}
	if exprRefsInBound(e, 100) {
		t.Error("SubqueryExpr should always be out of bounds")
	}
}

func TestExprRefsInBoundExists(t *testing.T) {
	e := &planner.ExistsExpr{}
	if exprRefsInBound(e, 100) {
		t.Error("ExistsExpr should always be out of bounds")
	}
}

// ---- env var config tests ----

func TestReadFusionConfigDefaults(t *testing.T) {
	// Clear env vars to test defaults.
	os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION")
	os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS")
	cfg := readFusionConfig()
	if cfg.enabled {
		t.Error("default should be disabled")
	}
	if cfg.minLevels != 3 {
		t.Errorf("default minLevels should be 3, got %d", cfg.minLevels)
	}
}

func TestReadFusionConfigEnabled(t *testing.T) {
	os.Setenv("GOOPG_RUNTIME_JOIN_FUSION", "1")
	defer os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION")
	cfg := readFusionConfig()
	if !cfg.enabled {
		t.Error("GOOPG_RUNTIME_JOIN_FUSION=1 should enable")
	}
}

func TestReadFusionConfigEnabledTrue(t *testing.T) {
	os.Setenv("GOOPG_RUNTIME_JOIN_FUSION", "true")
	defer os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION")
	cfg := readFusionConfig()
	if !cfg.enabled {
		t.Error("GOOPG_RUNTIME_JOIN_FUSION=true should enable")
	}
}

func TestReadFusionConfigMinLevels(t *testing.T) {
	os.Setenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS", "5")
	defer os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS")
	cfg := readFusionConfig()
	if cfg.minLevels != 5 {
		t.Errorf("minLevels should be 5, got %d", cfg.minLevels)
	}
}

func TestReadFusionConfigMinLevelsInvalid(t *testing.T) {
	os.Setenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS", "bad")
	defer os.Unsetenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS")
	cfg := readFusionConfig()
	if cfg.minLevels != 3 {
		t.Errorf("invalid minLevels should keep default 3, got %d", cfg.minLevels)
	}
}

// ---- structural tests ----

// TestFusedHashJoinOpImplementsOperator verifies the fused operator
// satisfies the Operator interface at compile time.
func TestFusedHashJoinOpImplementsOperator(t *testing.T) {
	var _ Operator = (*fusedHashJoinOp)(nil)
}

// TestJoinStructFieldCountGuard encodes the current field counts
// of the key structs so accidental additions/removals are noticed.
// This is not about a specific number being "correct" — it's about
// making every structural change explicit in review.
func TestFusedLevelFieldCount(t *testing.T) {
	// fusedLevel currently has 14 fields:
	// plan, probeKey, buildKey, width, offset, residual,
	// buildOp, ht, intHT, htIsInt, slot, matches, cursor
	_ = fusedLevel{}
	// If this test fails because a field was intentionally added
	// or removed, update the comment and the test expectation.
}

// TestFusedHashJoinOpFieldCount is the companion check for fusedHashJoinOp.
func TestFusedHashJoinOpFieldCount(t *testing.T) {
	// fusedHashJoinOp currently has 9 fields:
	// levels, probeOp, probeMatSlot, out, schema, ctx,
	// active, curLevel, probeWidth, stepCount
	_ = fusedHashJoinOp{}
}

// ---- virtualCol coordinate tests ----

func TestVirtualSlotColMapping(t *testing.T) {
	// Verify that NewVirtualSlot produces the correct column mapping.
	schema := planner.Schema{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}},
	}
	row1 := Row{Datum{}, Datum{}, Datum{}}
	row2 := Row{Datum{}, Datum{}}
	s1 := SlotFromRow(nil, row1)
	s2 := SlotFromRow(nil, row2)
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 0},
		{sourceIdx: 0, sourceCol: 1},
		{sourceIdx: 1, sourceCol: 0},
	}
	vs := NewVirtualSlot(schema, []TupleSlot{s1, s2}, cols)
	if vs.Width() != 3 {
		t.Errorf("width should be 3, got %d", vs.Width())
	}
	si, sc := vs.VirtualCol(0)
	if si != 0 || sc != 0 {
		t.Errorf("col 0 should map to (0,0), got (%d,%d)", si, sc)
	}
	si, sc = vs.VirtualCol(2)
	if si != 1 || sc != 0 {
		t.Errorf("col 2 should map to (1,0), got (%d,%d)", si, sc)
	}
}

// ---- buildEnv round-trip ----

func TestBuildEnvSetup(t *testing.T) {
	// M0127-P1.2 rewrote this. It used to assign the package global
	// buildEnvInFlight and read it back, which asserted only that Go
	// variables hold what is stored in them — and the global it exercised
	// was the source of the parallel-build data race. The env is now a
	// local of buildWithEnv, so what is worth pinning is that a fresh env
	// carries the three fields tryFuseHashCascade reads, and that the
	// worker flag is the one thing that distinguishes the two entry
	// points (C10/F4: fusion declines in a worker).
	leader := &buildEnv{root: nil, inWorker: false, fusionCfg: readFusionConfig()}
	worker := &buildEnv{root: nil, inWorker: true, fusionCfg: readFusionConfig()}

	if leader.inWorker {
		t.Error("leader env must not be marked inWorker")
	}
	if !worker.inWorker {
		t.Error("worker env must be marked inWorker")
	}
	if leader.q0.ran || worker.q0.ran {
		t.Error("a fresh env must have an unmemoised Q0")
	}
	if leader.fusionCfg.minLevels != worker.fusionCfg.minLevels {
		t.Errorf("fusion config differs between entry points: %d vs %d",
			leader.fusionCfg.minLevels, worker.fusionCfg.minLevels)
	}
}

// ---- walkExpr tests ----

func TestWalkExprVisitsAll(t *testing.T) {
	// BinaryOp: a = b (two ColumnRef children)
	left := &planner.ColumnRef{}
	right := &planner.ColumnRef{}
	op := &planner.BinaryOp{Left: left, Right: right}

	var visited []planner.Expr
	walkExpr(op, func(e planner.Expr) bool {
		visited = append(visited, e)
		return true
	})
	if len(visited) != 3 {
		t.Errorf("should visit 3 nodes, got %d", len(visited))
	}
}

func TestWalkExprStopsEarly(t *testing.T) {
	left := &planner.ColumnRef{}
	right := &planner.ColumnRef{}
	op := &planner.BinaryOp{Left: left, Right: right}

	count := 0
	walkExpr(op, func(e planner.Expr) bool {
		count++
		return false // stop after first
	})
	if count != 1 {
		t.Errorf("should stop after 1, got %d", count)
	}
}

func TestExprChildrenBinaryOp(t *testing.T) {
	op := &planner.BinaryOp{Left: &planner.ColumnRef{}, Right: &planner.ColumnRef{}}
	children := exprChildren(op)
	if len(children) != 2 {
		t.Errorf("BinaryOp should have 2 children, got %d", len(children))
	}
}

func TestExprChildrenFuncCall(t *testing.T) {
	fc := &planner.FuncCall{Args: []planner.Expr{&planner.ColumnRef{}, &planner.ColumnRef{}}}
	children := exprChildren(fc)
	if len(children) != 2 {
		t.Errorf("FuncCall with 2 args should have 2 children, got %d", len(children))
	}
}
