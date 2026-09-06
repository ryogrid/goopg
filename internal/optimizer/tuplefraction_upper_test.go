package optimizer

// C-17 (P4-08) — `tuple_fraction` end-to-end: every upper rel, not only the
// join search.
//
// The item is a PLUMBING item, and its verification is a census rather than a
// behaviour sweep, so the pins here are of two kinds:
//
//  1. a STATIC census over the planner source (`TestEveryUpperRelProducerIsThreadedATupleFraction`)
//     — no upper-rel producer may be called with a literal 0 fraction. Three
//     sites were, and a fourth (the set-op statement's ORDER BY) had no
//     ORDERED rel at all;
//  2. BEHAVIOURAL pins for the gap the census was hiding: the fraction used to
//     be stamped inside the `WHERE` arm, so a WHERE-less statement reached
//     every upper rel claiming all rows were wanted.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// upperRelProducers is the closed list of Phase-4 upper-rel producers. A new
// one added without a row here is caught by
// TestUpperRelProducerCensusIsComplete below, so the census cannot silently
// stop covering the planner.
var upperRelProducers = []string{
	"createGroupingPaths",
	"createOrderedPaths",
	"createDistinctPaths",
	"createWindowPaths",
	"createSetOpPaths",
}

// TestUpperRelProducerCensusIsComplete keeps `upperRelProducers` honest: every
// `func create*Paths` in the package that fetches an upper rel must be listed.
// Without this, C-17's census silently narrows the day C-19+ adds a producer.
func TestUpperRelProducerCensusIsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range upperRelProducers {
		listed[p] = true
	}
	decl := regexp.MustCompile(`(?m)^func (create[A-Za-z]*Paths)\(`)
	for _, f := range []string{"groupingpaths.go", "upperordered.go", "distinctpaths.go", "windowsetoppaths.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			if !listed[m[1]] {
				t.Fatalf("%s declares upper-rel producer %s, which the C-17 census does not list; add it to upperRelProducers", f, m[1])
			}
		}
	}
}

// TestEveryUpperRelProducerIsThreadedATupleFraction is C-17's census: no
// producer call site in the planner may pass a literal 0 as its
// `tupleFraction`. A literal 0 is not a neutral default — it is
// `ConsiderStartup = false` on the rel and `getCheapestFractionalPath`
// degenerating to cheapest-total, i.e. an upper rel that has been told the
// whole result will be fetched when it may not be.
//
// The check is textual on purpose. The alternative — asserting a nonzero
// fraction arrives at each rel — needs a statement shape per producer and
// silently passes when a shape stops reaching its producer, which is exactly
// how the WHERE-arm gap survived. Text cannot go stale that way.
func TestEveryUpperRelProducerIsThreadedATupleFraction(t *testing.T) {
	src, err := os.ReadFile("planner.go")
	if err != nil {
		t.Fatalf("read planner.go: %v", err)
	}
	// Producer calls are written on one line in this file; a call whose
	// argument list ends in `, 0)` passes the literal.
	for _, name := range upperRelProducers {
		re := regexp.MustCompile(regexp.QuoteMeta(name) + `\([^\n]*?, (0|0\.0)\)`)
		if loc := re.FindIndex(src); loc != nil {
			line := strings.Count(string(src[:loc[0]]), "\n") + 1
			t.Errorf("planner.go:%d calls %s with a literal 0 tuple fraction: %s",
				line, name, strings.TrimSpace(string(src[loc[0]:loc[1]])))
		}
	}
}

// TestTupleFractionIsStampedOnceAtTheConvergentPoint pins the hoist itself:
// `searchTupleFraction` is assigned to `ctx.tupleFraction` exactly ONCE in the
// planner, at the block every FROM arm reaches. Two assignments inside
// individual arms is the shape that left a WHERE-less statement at zero.
func TestTupleFractionIsStampedOnceAtTheConvergentPoint(t *testing.T) {
	src, err := os.ReadFile("planner.go")
	if err != nil {
		t.Fatalf("read planner.go: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*ctx\.tupleFraction = searchTupleFraction\(`)
	if n := len(re.FindAll(src, -1)); n != 1 {
		t.Fatalf("planner.go assigns ctx.tupleFraction from searchTupleFraction %d times, want exactly 1 (the convergent stamping block); per-arm assignments leave the arms without one at zero", n)
	}
}

// tupleFractionTestCatalog is one table wide enough to carry an ORDER BY, a
// GROUP BY and a UNION branch.
func tupleFractionTestCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int8"}},
		{Name: "b", Type: catalog.Type{Name: "int8"}},
	}
	for _, name := range []string{"t", "u"} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	return cat
}

func planOne(t *testing.T, cat catalog.Catalog, sql string) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	n, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return n
}

// findNode walks the plan for the first node satisfying `pred`.
func findNode(n Node, pred func(Node) bool) Node {
	if n == nil {
		return nil
	}
	if pred(n) {
		return n
	}
	for _, c := range planChildrenForTest(n) {
		if got := findNode(c, pred); got != nil {
			return got
		}
	}
	return nil
}

func planChildrenForTest(n Node) []Node {
	switch p := n.(type) {
	case *Sort:
		return []Node{p.Child}
	case *Limit:
		return []Node{p.Child}
	case *Project:
		return []Node{p.Child}
	case *Filter:
		return []Node{p.Child}
	case *Aggregate:
		return []Node{p.Child}
	case *Distinct:
		return []Node{p.Child}
	case *DistinctOn:
		return []Node{p.Child}
	case *WindowAgg:
		return []Node{p.Child}
	case *SetOp:
		return []Node{p.Left, p.Right}
	}
	return nil
}

// TestSetOpTrailingSortIsAnOrderedUpperRel pins the last producer C-17 wired:
// a set-op statement's trailing ORDER BY used to be a bare `&Sort{}` with no
// upper rel and therefore no price at all (the pre-C-12 state — a top-level
// sort costed zero). It must now carry the `PlanCost` `createOrderedPaths`
// stamps.
func TestSetOpTrailingSortIsAnOrderedUpperRel(t *testing.T) {
	cat := tupleFractionTestCatalog(t)
	for _, sql := range []string{
		"SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY 1",
		"SELECT a FROM t UNION SELECT a FROM u ORDER BY 1 LIMIT 5",
		"SELECT a FROM t EXCEPT SELECT a FROM u ORDER BY 1",
	} {
		t.Run(sql, func(t *testing.T) {
			sortNode := findNode(planOne(t, cat, sql), func(n Node) bool { _, ok := n.(*Sort); return ok })
			if sortNode == nil {
				t.Fatal("no Sort in the set-op plan")
			}
			carrier, ok := sortNode.(PlanCostCarrier)
			if !ok {
				t.Fatal("*Sort does not carry a PlanCost")
			}
			pc, set := carrier.PlanCostInfo()
			if !set {
				t.Fatal("the set-op trailing Sort carries no PlanCost; it was not built through createOrderedPaths")
			}
			if pc.TotalCost <= 0 {
				t.Fatalf("the set-op trailing Sort is priced at %v; a top-level sort priced at zero is the pre-C-12 state", pc.TotalCost)
			}
		})
	}
}

// TestFractionChangesTheWinnerOnATwoCandidateRel pins the MECHANISM the hoist
// feeds: on a rel holding a cheap-total/slow-start path and a cheap-start/
// slow-total one, `getCheapestFractionalPath` returns different winners at
// fraction 0 and at a small fraction. That is what an upper rel handed a
// literal 0 can never do — and, before C-17, what every upper rel of a
// WHERE-less statement could never do either, because the fraction was
// stamped inside the WHERE arm.
func TestFractionChangesTheWinnerOnATwoCandidateRel(t *testing.T) {
	rel := &RelOptInfo{Rows: 1000, ConsiderStartup: true}
	cheapTotal := &Path{Kind: PathPrebuilt, Rel: rel, Rows: 1000, Cost: Cost{Startup: 500, Total: 600}}
	cheapStartup := &Path{Kind: PathPrebuilt, Rel: rel, Rows: 1000, Cost: Cost{Startup: 1, Total: 5000}}
	rel.Pathlist = []*Path{cheapTotal, cheapStartup}
	rel.CheapestTotal = cheapTotal
	rel.CheapestStartup = cheapStartup

	if got := getCheapestFractionalPath(rel, 0); got != cheapTotal {
		t.Fatal("fraction 0 must return the cheapest-total path")
	}
	if got := getCheapestFractionalPath(rel, 1); got != cheapStartup {
		t.Fatal("an absolute bound of 1 row must return the cheapest-startup path; the fraction is not load-bearing")
	}
}

// TestConsiderStartupFollowsTheFraction re-pins the one thing the fraction
// does to a rel the moment it is fetched (`fetch_upper_rel`, relnode.c:1484):
// `consider_startup = (root->tuple_fraction > 0)`. A zero fraction is an
// active decision to PRUNE fast-start paths, which is why threading a literal
// 0 is a defect rather than a default.
func TestConsiderStartupFollowsTheFraction(t *testing.T) {
	for _, c := range []struct {
		frac float64
		want bool
	}{{0, false}, {0.1, true}, {10, true}} {
		u := newUpperRels()
		if got := fetchUpperRel(u, UpperOrdered, 0, c.frac).ConsiderStartup; got != c.want {
			t.Fatalf("fetchUpperRel(frac=%v).ConsiderStartup = %v, want %v", c.frac, got, c.want)
		}
		if got := newUpperRelForNode(u, UpperSetOp, c.frac).ConsiderStartup; got != c.want {
			t.Fatalf("newUpperRelForNode(frac=%v).ConsiderStartup = %v, want %v", c.frac, got, c.want)
		}
	}
}
