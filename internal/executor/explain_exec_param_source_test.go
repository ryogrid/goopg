package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

type unknownExecParamPlan struct{}

func (*unknownExecParamPlan) Pos() int                 { return 0 }
func (*unknownExecParamPlan) Output() optimizer.Schema { return nil }

func TestSubPlanExecParamSourcesSupportedOwners(t *testing.T) {
	source := &optimizer.ColumnRef{Name: "outer_key", SourceTableIdx: 3}
	cases := []struct {
		name  string
		owner optimizer.Expr
	}{
		{"scalar", &optimizer.SubqueryExpr{ParParam: []int{7}, Args: []optimizer.Expr{source}}},
		{"exists", &optimizer.ExistsExpr{ParParam: []int{7}, Args: []optimizer.Expr{source}}},
		{"in", &optimizer.InExpr{Operand: &optimizer.ColumnRef{Name: "probe"}, ParParam: []int{7}, Args: []optimizer.Expr{source}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := subPlanExecParamSources(tc.owner)
			if len(got) != 1 || got[7] != source {
				t.Fatalf("sources = %#v, want slot 7 -> source", got)
			}
		})
	}
}

func TestSubPlanExecParamSourcesDeclinesWholeMalformedOwner(t *testing.T) {
	col := func(name string) optimizer.Expr { return &optimizer.ColumnRef{Name: name} }
	cases := []struct {
		name  string
		owner optimizer.Expr
	}{
		{"empty", &optimizer.ExistsExpr{}},
		{"length mismatch", &optimizer.ExistsExpr{ParParam: []int{1}, Args: nil}},
		{"negative slot", &optimizer.ExistsExpr{ParParam: []int{-1}, Args: []optimizer.Expr{col("a")}}},
		{"duplicate slot", &optimizer.ExistsExpr{ParParam: []int{1, 1}, Args: []optimizer.Expr{col("a"), col("b")}}},
		{"nil arg", &optimizer.ExistsExpr{ParParam: []int{1}, Args: []optimizer.Expr{(*optimizer.ColumnRef)(nil)}}},
		{"non column arg", &optimizer.ExistsExpr{ParParam: []int{1}, Args: []optimizer.Expr{&optimizer.IntegerConst{Value: 1}}}},
		{"forwarded exec param", &optimizer.ExistsExpr{ParParam: []int{1}, Args: []optimizer.Expr{&optimizer.ExecParamRef{ID: 4}}}},
		{"mixed owner", &optimizer.ExistsExpr{ParParam: []int{1, 2}, Args: []optimizer.Expr{col("a"), &optimizer.ExecParamRef{ID: 4}}}},
		{"row in", &optimizer.InExpr{Operand: &optimizer.RowExpr{}, ParParam: []int{1}, Args: []optimizer.Expr{col("a")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subPlanExecParamSources(tc.owner); got != nil {
				t.Fatalf("sources = %#v, want whole-owner decline", got)
			}
		})
	}
}

func TestFormatExecParamRefUsesSlotWithoutProvenOwner(t *testing.T) {
	reg := &subPlanReg{
		rel: &explainNames{},
		execParamSources: map[int]*optimizer.ColumnRef{
			7: {Name: "outer_key", SourceTableIdx: 3},
			8: {Name: "unknown", SourceTableIdx: 0},
		},
	}

	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 7}, reg); got != "$7" {
		t.Errorf("source without owning ancestor = %q, want $7", got)
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 8}, reg); got != "$8" {
		t.Errorf("unresolvable source = %q, want $8", got)
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 9}, reg); got != "$9" {
		t.Errorf("unmapped source = %q, want $9", got)
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 7}, nil); got != "$7" {
		t.Errorf("nil registry = %q, want $7", got)
	}
}

func TestEmitSubPlanSubtreesShadowsAndRestoresParamSources(t *testing.T) {
	outerPlan := &optimizer.SeqScan{}
	innerPlan := &optimizer.SeqScan{}
	outerOwner := &optimizer.ExistsExpr{
		Plan: outerPlan, ParParam: []int{1},
		Args: []optimizer.Expr{&optimizer.ColumnRef{Name: "outer_key", SourceTableIdx: 1}},
	}
	innerOwner := &optimizer.SubqueryExpr{
		Plan: innerPlan, ParParam: []int{2},
		Args: []optimizer.Expr{&optimizer.ColumnRef{Name: "inner_key", SourceTableIdx: 2}},
	}
	reg := &subPlanReg{pending: []subPlanEntry{{n: 1, expr: outerOwner, plan: outerPlan}}}
	var rows []Row
	emitSubPlanSubtrees(&rows, "  ", parser.ExplainOptions{}, reg, nil, func(plan optimizer.Node, _ int) {
		if plan != outerPlan {
			t.Fatalf("outer callback got %T, want outer plan", plan)
		}
		if got := reg.execParamSources[1]; got == nil || got.Name != "outer_key" {
			t.Fatalf("outer mapping = %#v", reg.execParamSources)
		}
		reg.pending = append(reg.pending, subPlanEntry{n: 2, expr: innerOwner, plan: innerPlan})
		emitSubPlanSubtrees(&rows, "    ", parser.ExplainOptions{}, reg, nil, func(plan optimizer.Node, _ int) {
			if plan != innerPlan {
				t.Fatalf("inner callback got %T, want inner plan", plan)
			}
			if got := reg.execParamSources[2]; got == nil || got.Name != "inner_key" {
				t.Fatalf("inner mapping = %#v", reg.execParamSources)
			}
			if got := reg.execParamSources[1]; got != nil {
				t.Fatalf("inner body borrowed outer mapping: %#v", got)
			}
		})
		if got := reg.execParamSources[1]; got == nil || got.Name != "outer_key" {
			t.Fatalf("outer mapping was not restored: %#v", reg.execParamSources)
		}
	})
	if reg.execParamSources != nil {
		t.Fatalf("mapping leaked after outer body: %#v", reg.execParamSources)
	}
}

func planForExecParamTest(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

func firstSeqScanForExecParamTest(n optimizer.Node) *optimizer.SeqScan {
	if scan, ok := n.(*optimizer.SeqScan); ok {
		return scan
	}
	for _, child := range planChildren(n) {
		if scan := firstSeqScanForExecParamTest(child); scan != nil {
			return scan
		}
	}
	return nil
}

func TestResolveExecParamSourceStopsAtScopeBoundaries(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE hidden_scope (a int)"); err != nil {
		t.Fatal(err)
	}
	plan := planForExecParamTest(t, ctx, "SELECT a FROM hidden_scope")
	scan := firstSeqScanForExecParamTest(plan)
	if scan == nil || len(scan.Output()) == 0 {
		t.Fatalf("planned fixture has no SeqScan schema: %T", plan)
	}
	col := scan.Output()[0]
	source := &optimizer.ColumnRef{Name: col.Name, SourceTableIdx: col.SourceTableIdx}
	names := newExplainNames(scan)
	if got := resolveExecParamSourceInOwner(names, scan, source); got != "hidden_scope.a" {
		t.Fatalf("direct owner = %q, want hidden_scope.a", got)
	}
	zeroSource := &optimizer.ColumnRef{Name: col.Name, SourceTableIdx: 0}
	reg := &subPlanReg{
		rel:              names,
		ancestor:         scan,
		execParamSources: map[int]*optimizer.ColumnRef{7: zeroSource},
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 7}, reg); got != "hidden_scope.a" {
		t.Fatalf("unique zero-ID source = %q, want hidden_scope.a", got)
	}

	isolated := &optimizer.Project{Child: scan, IsolatedScope: true}
	if got := resolveExecParamSourceInOwner(names, isolated, source); got != "" {
		t.Fatalf("IsolatedScope leaked hidden same-ID scan: %q", got)
	}
	cte := &optimizer.CTEScan{Name: "c", Alias: "c", Child: scan}
	if got := resolveExecParamSourceInOwner(names, cte, source); got != "" {
		t.Fatalf("CTEScan.Child leaked hidden same-ID scan: %q", got)
	}
}

func TestResolveExecParamSourceRequiresRegisteredRTID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE unregistered_scope (a int)"); err != nil {
		t.Fatal(err)
	}
	plan := planForExecParamTest(t, ctx, "SELECT a FROM unregistered_scope")
	scan := firstSeqScanForExecParamTest(plan)
	if scan == nil || scan.RTID == 0 || len(scan.Output()) == 0 {
		t.Fatalf("planned fixture lacks registered SeqScan identity: %#v", scan)
	}
	names := newExplainNames(scan)
	delete(names.bySource, scan.RTID)
	reg := &subPlanReg{
		rel:      names,
		ancestor: scan,
		execParamSources: map[int]*optimizer.ColumnRef{
			7: {Name: scan.Output()[0].Name, SourceTableIdx: scan.Output()[0].SourceTableIdx},
		},
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 7}, reg); got != "$7" {
		t.Fatalf("unregistered RTID rendered as %q, want $7", got)
	}
}

func TestResolveExecParamSourceDeduplicatesRTID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE duplicate_rtid_scope (a int)"); err != nil {
		t.Fatal(err)
	}
	plan := planForExecParamTest(t, ctx, "SELECT a FROM duplicate_rtid_scope")
	scan := firstSeqScanForExecParamTest(plan)
	if scan == nil || scan.RTID == 0 || len(scan.Output()) == 0 {
		t.Fatalf("planned fixture lacks registered SeqScan identity: %#v", scan)
	}
	duplicate := *scan
	owner := &optimizer.Join{Left: scan, Right: &duplicate}
	got := resolveExecParamSourceInOwner(newExplainNames(owner), owner,
		&optimizer.ColumnRef{Name: scan.Output()[0].Name, SourceTableIdx: 0})
	if got != "duplicate_rtid_scope.a" {
		t.Fatalf("duplicate RTID resolved as %q, want duplicate_rtid_scope.a", got)
	}
}

func TestFormatExecParamRefDeclinesNonmatchingAndUnknownOwner(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE known_owner_scope (a int)"); err != nil {
		t.Fatal(err)
	}
	plan := planForExecParamTest(t, ctx, "SELECT a FROM known_owner_scope")
	scan := firstSeqScanForExecParamTest(plan)
	if scan == nil {
		t.Fatalf("planned fixture has no SeqScan: %T", plan)
	}
	reg := &subPlanReg{
		rel:      newExplainNames(scan),
		ancestor: scan,
		execParamSources: map[int]*optimizer.ColumnRef{
			8: {Name: "missing", SourceTableIdx: 0},
		},
	}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 8}, reg); got != "$8" {
		t.Fatalf("nonmatching source rendered as %q, want $8", got)
	}

	reg.ancestor = &unknownExecParamPlan{}
	if got := formatExecParamRef(&optimizer.ExecParamRef{ID: 8}, reg); got != "$8" {
		t.Fatalf("unknown owner rendered as %q, want $8", got)
	}
}

func TestResolveExecParamSourceDeclinesAmbiguousOwner(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE ambiguous_scope (a int)"); err != nil {
		t.Fatal(err)
	}
	plan := planForExecParamTest(t, ctx,
		"SELECT x.a FROM ambiguous_scope x, ambiguous_scope y WHERE x.a = y.a")
	if got := resolveExecParamSourceInOwner(newExplainNames(plan), plan,
		&optimizer.ColumnRef{Name: "a", SourceTableIdx: 0}); got != "" {
		t.Fatalf("ambiguous zero-ID owner resolved as %q, want decline", got)
	}
}

func TestExplainNestedExecParamUsesImmediateOwnerLevel(t *testing.T) {
	pinCorrelatedSubPlanPath(t)
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t3 (a int, b int)"); err != nil {
		t.Fatal(err)
	}

	plan, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS ("+
			"SELECT 1 FROM t2 WHERE t2.a = t1.a AND (t2.b = -999 OR EXISTS ("+
			"SELECT 1 FROM t3 WHERE t3.a = t2.a)))")
	if !strings.Contains(plan, "SubPlan 1") || !strings.Contains(plan, "SubPlan 2") {
		t.Fatalf("fixture did not retain two nested SubPlans:\n%s", plan)
	}
	if strings.Count(plan, "a = t1.a") != 1 {
		t.Fatalf("outer source should appear exactly once:\n%s", plan)
	}
	if strings.Count(plan, "a = t2.a") != 1 {
		t.Fatalf("nested source did not resolve to its immediate owner:\n%s", plan)
	}
}

func TestExplainExecParamUsesOwningOuterColumn(t *testing.T) {
	pinCorrelatedSubPlanPath(t)
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	for _, prefix := range []string{"EXPLAIN", "EXPLAIN (ANALYZE)"} {
		t.Run(prefix, func(t *testing.T) {
			plan, _ := joinedPlan(t, ctx,
				prefix+" SELECT * FROM t1 WHERE t1.a = 1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")
			if !strings.Contains(plan, "Filter: (a = t1.a)") {
				t.Fatalf("PARAM_EXEC source was not rendered as the qualified owning column:\n%s", plan)
			}
			if strings.Contains(plan, " = $0") {
				t.Fatalf("mapped PARAM_EXEC leaked its slot token:\n%s", plan)
			}
		})
	}
}
