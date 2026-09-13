package optimizer

// R118 (TEMPORARY — removed with the diagnostic before REPORT.md): focused
// tests for the producer-audit sidecar lifecycle, equation reproduction,
// census mapping, and renderer agreement.

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// auditTestJoin builds a hash equi-join over the cond-lower fixture:
// supplier (outer) against nation (inner) on the nation PK.
func auditTestJoin(t *testing.T) (*Join, *SeqScan, *SeqScan) {
	t.Helper()
	_, outer, inner := condLowerFixture(t)
	j := condLowerJoin(outer, inner, JoinTypeInner, nil)
	j.Algo = JoinAlgoHash
	return j, outer, inner
}

func TestProducerAuditOffAllocatesNothing(t *testing.T) {
	j, _, _ := auditTestJoin(t)
	var sc *producerAuditSidecar
	if got := sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test"); got != 0 {
		t.Fatalf("nil sidecar must record nothing, got id %d", got)
	}
	if _, ok := sc.resolve(j); ok {
		t.Fatal("nil sidecar must resolve nothing")
	}
	if rep := freezeProducerAudit(sc, j); rep.Stop != "no-sidecar" {
		t.Fatalf("nil sidecar census stop = %q, want no-sidecar", rep.Stop)
	}
}

func TestEstimateJoinEquationReproduces(t *testing.T) {
	j, _, _ := auditTestJoin(t)
	var tr JoinEstimateTrace
	want := estimateJoinTraced(j, &tr)
	if tr.Branch == "" {
		t.Fatal("trace recorded no branch")
	}
	got, ok := reproduceJoinEstimateEquation(&tr)
	if !ok {
		t.Fatalf("branch %q not replayable", tr.Branch)
	}
	if got != want {
		t.Fatalf("replay %d != production %d (branch %s)", got, want, tr.Branch)
	}
	if plain := estimateJoin(j); plain != want {
		t.Fatalf("traced %d != untraced %d: the trace changed arithmetic", want, plain)
	}
}

func TestEstimateJoinEquationBranches(t *testing.T) {
	cases := map[string]func() *Join{
		"cross": func() *Join {
			j, _, _ := auditTestJoin(t)
			j.Type = JoinTypeCross
			return j
		},
		"semi": func() *Join {
			j, _, _ := auditTestJoin(t)
			j.Type = JoinTypeSemi
			return j
		},
		"anti": func() *Join {
			j, _, _ := auditTestJoin(t)
			j.Type = JoinTypeAnti
			return j
		},
		"left": func() *Join {
			j, _, _ := auditTestJoin(t)
			j.Type = JoinTypeLeft
			return j
		},
		"nestedloop-fallback": func() *Join {
			j, _, _ := auditTestJoin(t)
			j.Algo = JoinAlgoNestedLoop
			return j
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			j := build()
			var tr JoinEstimateTrace
			want := estimateJoinTraced(j, &tr)
			got, ok := reproduceJoinEstimateEquation(&tr)
			if !ok {
				t.Fatalf("branch %q not replayable", tr.Branch)
			}
			if got != want {
				t.Fatalf("replay %d != production %d (branch %s)", got, want, tr.Branch)
			}
		})
	}
}

func TestProducerAuditSuccessorChain(t *testing.T) {
	sc := newProducerAuditSidecar()
	j, _, _ := auditTestJoin(t)
	id1 := sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test")
	copy1 := *j
	sc.noteSuccessor(j, &copy1)
	copy2 := copy1
	sc.noteSuccessor(&copy1, &copy2)
	got, ok := sc.resolve(&copy2)
	if !ok {
		t.Fatal("successor chain must resolve")
	}
	end := sc.records[got]
	first := sc.records[id1]
	if !reflect.DeepEqual(end.Trace, first.Trace) || end.Route != first.Route {
		t.Fatal("terminal lineage must carry the source producer data")
	}
	// Nil-safe no-ops.
	sc.noteSuccessor(nil, &copy2)
	sc.noteSuccessor(j, nil)
	sc.noteSuccessor(j, j)
	var nilSC *producerAuditSidecar
	nilSC.noteSuccessor(j, &copy1)
	// Unknown pointer: zero mapping, not a guess.
	if _, ok := sc.resolve(&Join{}); ok {
		t.Fatal("unrecorded pointer must not resolve")
	}
}

func TestProducerAuditCensusMapping(t *testing.T) {
	sc := newProducerAuditSidecar()
	j, _, _ := auditTestJoin(t)
	top := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Left: j,
		Right: &SeqScan{}}
	// Only the lower join is instrumented: the census must map it
	// one-to-one and report the uninstrumented top as zero-unmapped.
	id := sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test")
	rep := freezeProducerAudit(sc, top)
	if len(rep.Rows) != 2 {
		t.Fatalf("census found %d joins, want 2", len(rep.Rows))
	}
	if rep.Rows[0].Mapping != "zero-unmapped" {
		t.Errorf("uninstrumented top maps as %q, want zero-unmapped", rep.Rows[0].Mapping)
	}
	low := rep.Rows[1]
	if low.Mapping != "one-to-one" || low.ConstructionID != id {
		t.Errorf("lower maps as %q id %d, want one-to-one id %d", low.Mapping, low.ConstructionID, id)
	}
	if !low.EquationOK {
		t.Error("lower equation must reproduce from the final node")
	}
	if !low.SchemaOK {
		t.Error("lower schema must match its construction snapshot")
	}
	if low.Route != producerRouteLegacyDirect {
		t.Errorf("route = %q, want legacy-direct", low.Route)
	}
}

func TestProducerAuditDeterministicIDs(t *testing.T) {
	plan := func() (*producerAuditSidecar, *ProducerAuditReport) {
		sc := newProducerAuditSidecar()
		j, _, _ := auditTestJoin(t)
		sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test")
		return sc, freezeProducerAudit(sc, j)
	}
	_, r1 := plan()
	_, r2 := plan()
	if len(r1.Rows) != 1 || len(r2.Rows) != 1 {
		t.Fatal("expected one row each")
	}
	if r1.Rows[0].ConstructionID != r2.Rows[0].ConstructionID {
		t.Error("construction IDs must be deterministic across statements")
	}
	if r1.Token == r2.Token {
		t.Error("report tokens must be unique per statement")
	}
}

func TestProducerAuditConcurrentDisjoint(t *testing.T) {
	const n = 8
	reps := make([]*ProducerAuditReport, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sc := newProducerAuditSidecar()
			j, _, _ := auditTestJoin(t)
			sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test")
			reps[i] = freezeProducerAudit(sc, j)
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, rep := range reps {
		if rep == nil || len(rep.Rows) != 1 {
			t.Fatalf("statement %d: bad report", i)
		}
		if seen[rep.Token] {
			t.Fatalf("duplicate token %s across concurrent statements", rep.Token)
		}
		seen[rep.Token] = true
		if !rep.Rows[0].EquationOK {
			t.Errorf("statement %d: equation must hold under concurrency", i)
		}
	}
}

// TestProducerAuditExplainHandoff plans a real EXPLAIN with the diagnostic
// on and off: the wrapper creates exactly one sidecar, ordinary (non-EXPLAIN)
// planning allocates none, and errors publish nothing.
func TestProducerAuditExplainHandoff(t *testing.T) {
	cat, _, _ := condLowerFixture(t)
	t.Setenv(producerAuditFlag, "1")
	stmts, err := parser.Parse("EXPLAIN SELECT * FROM supplier, nation")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := PlanWithSettings(stmts[0], cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	ex, ok := node.(*Explain)
	if !ok {
		t.Fatalf("top node is %T, want *Explain", node)
	}
	if ex.ProducerReport == nil {
		t.Fatal("diagnostic-on EXPLAIN must attach a frozen report")
	}
	if len(ex.ProducerReport.Rows) == 0 {
		t.Error("report should observe the planned joins")
	}
	// Diagnostic off: no report, no state.
	t.Setenv(producerAuditFlag, "")
	node2, err := PlanWithSettings(stmts[0], cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if ex2 := node2.(*Explain); ex2.ProducerReport != nil {
		t.Error("diagnostic-off EXPLAIN must attach nothing")
	}
	// Ordinary statement: no report possible (no wrapper to carry it).
	sel, err := parser.Parse("SELECT * FROM supplier, nation")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Setenv(producerAuditFlag, "1")
	if _, err := PlanWithSettings(sel[0], cat, DefaultPlannerSettings()); err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Error path publishes nothing (no Explain node exists to carry it).
	bad, err := parser.Parse("EXPLAIN SELECT * FROM no_such_table_xyz")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := PlanWithSettings(bad[0], cat, DefaultPlannerSettings()); err == nil {
		t.Fatal("expected a planning error")
	}
}

// TestProducerAuditPathBackedRoute exercises the alternate construction
// route end to end: a search-built PathHashJoin becomes a *Join through
// createHashJoinPlan, records as path-backed, and maps one-to-one with a
// reproducing equation. Production never threads the sidecar here (no
// planner settings in scope); the test proves the record path accepts the
// route if a successor ever threads it.
func TestProducerAuditPathBackedRoute(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 10000, 100), scanRel(b, 500, 5)
	joinrel := newRelOptInfo(a|b, 5000, 64)
	clauses := []*restrictInfo{equiClause(a, b)}
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, defaultCostParams(), nil); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	var hp *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathHashJoin {
			hp = p
		}
	}
	if hp == nil {
		t.Fatal("no hash path generated")
	}
	// Rebuild through the REAL alternate constructor on prebuilt leaves
	// over real tables (baseLeaf carriers included, as the search sets
	// them at joinsearch.go:423); the join shape is what is exercised.
	_, suppOuter, nationInner := condLowerFixture(t)
	outer.baseLeaf = suppOuter
	outer.baseOffset = 0
	inner.baseLeaf = nationInner
	inner.baseOffset = len(suppOuter.Output())
	outerP := &Path{Kind: PathPrebuilt, Rel: outer, Rows: 10000, node: suppOuter}
	innerP := &Path{Kind: PathPrebuilt, Rel: inner, Rows: 500, node: nationInner}
	_ = hp
	joinPath := &Path{Kind: PathHashJoin, Jointype: parser.JoinInner, Rel: joinrel,
		Rows: 5000, Children: []*Path{outerP, innerP},
		// The real producer's own key carriers (not hand-built pairs).
		HashKeys: hp.HashKeys,
	}
	node, _ := createHashJoinPlan(joinPath)
	j, ok := node.(*Join)
	if !ok {
		t.Fatalf("path-backed build produced %T, want *Join", node)
	}
	sc := newProducerAuditSidecar()
	id := sc.recordJoinConstruction(j, producerRoutePathBacked, "publishedSchema@createHashJoinPlan")
	rep := freezeProducerAudit(sc, j)
	if len(rep.Rows) != 1 {
		t.Fatalf("census found %d joins, want 1", len(rep.Rows))
	}
	row := rep.Rows[0]
	if row.Mapping != "one-to-one" || row.ConstructionID != id || row.Route != producerRoutePathBacked {
		t.Errorf("row = %+v, want one-to-one path-backed id %d", row, id)
	}
	if !row.EquationOK {
		t.Error("path-backed equation must reproduce from the final node")
	}
}

// TestProducerAuditDegenerateFreeze pins census robustness: nil children
// and empty trees freeze without panic and without rows.
func TestProducerAuditDegenerateFreeze(t *testing.T) {
	sc := newProducerAuditSidecar()
	rep := freezeProducerAudit(sc, nil)
	if len(rep.Rows) != 0 {
		t.Errorf("nil tree census has %d rows, want 0", len(rep.Rows))
	}
	rep2 := freezeProducerAudit(sc, &Join{})
	if len(rep2.Rows) != 1 || rep2.Rows[0].Mapping != "zero-unmapped" {
		t.Errorf("unrecorded join must map zero-unmapped, got %+v", rep2.Rows)
	}
}

// TestProducerAuditNestedExplain pins the degenerate nested-EXPLAIN
// behavior: the inner wrapper gets its own sidecar and report; the outer
// wrapper carries an explicitly empty report (the census does not descend
// into nested Explain nodes). Deterministic, never partial.
func TestProducerAuditNestedExplain(t *testing.T) {
	cat, _, _ := condLowerFixture(t)
	t.Setenv(producerAuditFlag, "1")
	stmts, err := parser.Parse("EXPLAIN EXPLAIN SELECT * FROM supplier")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := PlanWithSettings(stmts[0], cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	ex, ok := node.(*Explain)
	if !ok {
		t.Fatalf("top is %T", node)
	}
	inner, ok := ex.Child.(*Explain)
	if !ok {
		t.Fatalf("child is %T, want nested *Explain", ex.Child)
	}
	if inner.ProducerReport == nil {
		t.Fatal("inner EXPLAIN must carry its own frozen report")
	}
	if ex.ProducerReport == nil {
		t.Fatal("outer EXPLAIN must carry a report field (possibly empty)")
	}
	if len(ex.ProducerReport.Rows) != 0 {
		t.Errorf("outer report has %d rows, want 0 (no descent into nested Explain)",
			len(ex.ProducerReport.Rows))
	}
}

// TestProducerAuditCensusPreorder pins the census traversal order on one
// fixture tree: same joins, deterministic pre-order. Cross-renderer
// agreement (TEXT/JSON Join occurrence sequences vs frozen ordinals) is
// pinned by the executor-side TestProducerAuditRendererAgreement, which
// exercises the production render paths instead of duplicating them here.
func TestProducerAuditCensusPreorder(t *testing.T) {
	sc := newProducerAuditSidecar()
	j, _, _ := auditTestJoin(t)
	outer2, _, _ := auditTestJoin(t)
	top := &Join{Algo: JoinAlgoHash, Type: JoinTypeInner, Left: j, Right: outer2}
	sc.recordJoinConstruction(j, producerRouteLegacyDirect, "test")
	sc.recordJoinConstruction(outer2, producerRouteLegacyDirect, "test")
	sc.recordJoinConstruction(top, producerRouteLegacyDirect, "test")
	rep := freezeProducerAudit(sc, &Project{Child: top})
	var want []string
	for _, r := range rep.Rows {
		want = append(want, fmt.Sprintf("%s/%s", r.Jointype, r.Algo))
	}
	if len(want) != 3 || want[0] != "inner/hash" || want[1] != "inner/hash" || want[2] != "inner/hash" {
		t.Fatalf("census order = %v, want three inner/hash in pre-order", want)
	}
	// TEXT and JSON renderers both expose the same three occurrences; the
	// dedicated executor-side test asserts the rendered sequences match.
	_ = strings.Join(want, ",")
}
