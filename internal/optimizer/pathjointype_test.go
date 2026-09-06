package optimizer

// C-03a (docs/design/planner-c03-jointype-search/DESIGN.md §4) — `Path.Jointype`,
// landed INERT. These are the slice's own gates:
//
//   - R1: `parser.JoinInner` is the zero value, so the new field is invisible to
//     every path that does not set it. The design VERIFIES this by inspection
//     (internal/parser/ast.go:727) and asks for it to be PINNED, because the
//     inertness argument of all four C-03 cuts rests on it: reorder that enum
//     and thousands of unstamped paths silently become LEFT joins.
//   - the compare-ignore rule: jointype is a correctness attribute set by
//     legality, not a cost dimension, so dominance must not see it.
//   - the DPPATH label, which is what C-03b/C-03d adjudicate on.
//
// What is deliberately NOT here: any suite-level or plan-level assertion. The
// slice cannot move a plan — nothing reads `Path.Jointype` yet — and a gate that
// could only ever pass is not evidence. The evidence is the mechanism pins
// below plus the unchanged suites.

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPathJointypeDefaultsToInner is R1. Two halves, because the risk has two
// halves: the enum's zero value, and the struct's zero value reading as it.
func TestPathJointypeDefaultsToInner(t *testing.T) {
	if parser.JoinInner != 0 {
		t.Fatalf("parser.JoinInner = %d, want 0 — the whole C-03 inertness "+
			"argument is that an unstamped Path reads as an inner join "+
			"(DESIGN.md §4 R1)", int(parser.JoinInner))
	}
	var p Path
	if p.Jointype != parser.JoinInner {
		t.Fatalf("zero Path.Jointype = %v, want JoinInner", p.Jointype)
	}
	// A scan path goes through a real producer and must come out inner too:
	// the scan producers never touch the field, which is the point.
	rel := scanRel(relsetOf(0), 1000, 20)
	for _, sp := range rel.Pathlist {
		if sp.Jointype != parser.JoinInner {
			t.Errorf("scan path kind=%d Jointype = %v, want JoinInner", sp.Kind, sp.Jointype)
		}
	}
}

// TestJoinProducersStampInner: every path `addPathsToJoinrel` offers carries
// JoinInner explicitly. This is the C-03b hook — when a producer starts
// stamping something else, it does so because the sjinfo said to, and this test
// is the record of what the pre-C-03b world emitted.
func TestJoinProducersStampInner(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 10000, 100), scanRel(b, 500, 5)
	joinrel := newRelOptInfo(a|b, 5000, 64)
	clauses := []*restrictInfo{equiClause(a, b), plainClause(a | b)}
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	if len(joinrel.Pathlist) == 0 {
		t.Fatal("no paths generated — the producers under test never ran")
	}
	for _, p := range joinrel.Pathlist {
		if p.Jointype != parser.JoinInner {
			t.Errorf("path kind=%d Jointype = %v, want JoinInner", p.Kind, p.Jointype)
		}
	}
}

// TestComparePathsIgnoresJointype: two paths identical in every costed
// dimension but differing in jointype must compare EQUAL, so `addToPathlist`
// rejects the newcomer exactly as it would have before the field existed.
//
// The must-avoid direction matters more than the equality: if jointype ever
// became a dominance dimension, a SEMI and an INNER path over the same relset
// would both survive and every list above them would double.
func TestComparePathsIgnoresJointype(t *testing.T) {
	rel := newRelOptInfo(relsetOf(0)|relsetOf(1), 100, 32)
	mk := func(jt parser.JoinType) *Path {
		return &Path{Kind: PathHashJoin, Jointype: jt, Rel: rel, Rows: 100,
			Cost: Cost{Startup: 10, Total: 100}}
	}
	for _, jt := range []parser.JoinType{parser.JoinLeft, parser.JoinSemi, parser.JoinAnti, parser.JoinFull} {
		if got := comparePaths(mk(parser.JoinInner), mk(jt)); got != relEqual {
			t.Errorf("comparePaths(inner, %v) = %v, want relEqual", jt, got)
		}
		if got := comparePathCosts(mk(parser.JoinInner), mk(jt), totalCost); got != 0 {
			t.Errorf("comparePathCosts(inner, %v) = %d, want 0", jt, got)
		}
		list := addToPathlist([]*Path{mk(parser.JoinInner)}, mk(jt))
		if len(list) != 1 {
			t.Errorf("addToPathlist kept %d paths for %v, want 1 — jointype must "+
				"not create a second surviving path", len(list), jt)
		}
	}
}

// TestTracePathRendersJointype: the DPPATH label C-03b and C-03d read.
//
// The record is captured off a real `os.Stderr` pipe rather than by
// re-implementing the format string, so a change to the format is caught here
// instead of silently diverging from what an enum-trace reader parses.
func TestTracePathRendersJointype(t *testing.T) {
	rel := newRelOptInfo(relsetOf(0)|relsetOf(2), 100, 32)
	for _, tc := range []struct {
		jt   parser.JoinType
		want string
	}{
		{parser.JoinInner, "jointype=inner"},
		{parser.JoinLeft, "jointype=left"},
		{parser.JoinSemi, "jointype=semi"},
		{parser.JoinAnti, "jointype=anti"},
	} {
		p := &Path{Kind: PathHashJoin, Jointype: tc.jt, Rel: rel, Rows: 100,
			Cost: Cost{Startup: 10, Total: 100}}
		line := captureTracePath(t, rel, p, "test.producer", false, verdictAccepted)
		if !strings.Contains(line, tc.want) {
			t.Errorf("DPPATH line %q does not contain %q", line, tc.want)
		}
		// The label is appended AFTER verdict, so an existing reader that
		// stops at `verdict=` is unaffected. Pin the ordering, not just the
		// presence.
		if i, j := strings.Index(line, "verdict="), strings.Index(line, "jointype="); i < 0 || j < i {
			t.Errorf("DPPATH line %q: jointype= must follow verdict=", line)
		}
	}
}

// captureTracePath runs tracePath with the trace forced on and returns the one
// line it wrote to stderr.
func captureTracePath(t *testing.T, rel *RelOptInfo, p *Path, producer string, partial bool, v pathVerdict) string {
	t.Helper()
	oldEnabled, oldErr := pathTraceEnabled, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	pathTraceEnabled, os.Stderr = true, w
	tracePath(rel, p, producer, partial, v)
	pathTraceEnabled, os.Stderr = oldEnabled, oldErr
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("read trace: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}
	return strings.TrimSpace(line)
}
