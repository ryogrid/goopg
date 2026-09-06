package optimizer

// M0127-P5.8 — the collapse pass (collapse.go, 03 §6).
//
// `resolveContext.joinlist` IS read in production since M0127-P5.9
// (2026-08-06) — `tryPGShapedJoinSearch` consults it — so only the narrower
// `GOOPG_PGSHAPED_COLLAPSE` arm (explicit INNER JOIN flattening) is still off
// by default. These tests are therefore no longer the whole specification,
// though they remain the whole specification of the flattening arm. They are
// written against the PG behaviours the file claims to
// port, not against the implementation's shape: each names the upstream line it
// pins.
//
// The acceptance test is `TestFlatCommaListIsOneProblemAtAnyWidth` — the
// property whose violation would re-introduce the greedy pre-reorder for wide
// comma joins (03 §6's documented Q2 failure mode), and the one a "cap the
// search size" reading of the GUCs would break first.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/parser"
)

// fmtJoinlist renders a joinlist as nested bracket lists so a structural
// mismatch reads as one string rather than as a walk of two trees:
// `[0 1 [2 3]]` is "0 and 1 and a subproblem of 2 and 3, in one problem".
func fmtJoinlist(jl joinlist) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range jl {
		if i > 0 {
			b.WriteByte(' ')
		}
		if it.isLeaf() {
			fmt.Fprintf(&b, "%d", it.rel)
			continue
		}
		b.WriteString(fmtJoinlist(it.sub))
	}
	b.WriteByte(']')
	return b.String()
}

// parseFrom returns the FROM clause of `SELECT * FROM <from>` as the parser
// builds it. Going through the real parser rather than hand-building
// `parser.FromExpr` values is deliberate: the leaf numbering this file asserts
// on is only meaningful if it matches the order the parser (and therefore
// `planFromItem`) walks range variables in.
func parseFrom(t *testing.T, from string) []parser.FromExpr {
	t.Helper()
	stmts, err := parser.Parse("SELECT * FROM " + from)
	if err != nil {
		t.Fatalf("parse %q: %v", from, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse %q: got %d statements, want 1", from, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parse %q: got %T, want *parser.SelectStmt", from, stmts[0])
	}
	if len(sel.FromExprs) == 0 {
		t.Fatalf("parse %q: no FromExprs (the JOIN-free spelling is deconstructRangeVars' case)", from)
	}
	return sel.FromExprs
}

func checkJoinlist(t *testing.T, from string, lim collapseLimits, collapseJoins bool, want string) {
	t.Helper()
	got := fmtJoinlist(deconstructJointree(parseFrom(t, from), lim, collapseJoins))
	if got != want {
		t.Errorf("FROM %s (from=%d join=%d collapse=%v):\n  got  %s\n  want %s",
			from, lim.fromCollapseLimit, lim.joinCollapseLimit, collapseJoins, got, want)
	}
}

// TestFlatCommaListIsOneProblemAtAnyWidth pins initsplan.c:1233-1238's
// `sub_members <= 1` arm: a single-baserel FROM item merges into the parent
// joinlist UNCONDITIONALLY, so a comma list is one search problem however wide
// it is and whatever the GUCs say.
//
// This is the acceptance property. Reading either limit as "cap the number of
// relations the DP enumerates over" would split a 15-way comma join into
// subproblems at width 9, which is the greedy-pre-reorder regime 03 §6 forbids.
func TestFlatCommaListIsOneProblemAtAnyWidth(t *testing.T) {
	for _, n := range []int{2, 8, 9, 15, 40} {
		names := make([]string, n)
		want := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("t%d", i)
			want[i] = fmt.Sprintf("%d", i)
		}
		from := strings.Join(names, ", ")
		wantStr := "[" + strings.Join(want, " ") + "]"
		// Both regimes, and the tightest limits the GUCs admit (MinVal 1):
		// none of the four may split a flat comma list.
		for _, lim := range []collapseLimits{
			defaultCollapseLimits(),
			{fromCollapseLimit: 1, joinCollapseLimit: 1},
		} {
			for _, collapse := range []bool{false, true} {
				checkJoinlist(t, from, lim, collapse, wantStr)
			}
		}
		// The JOIN-free parse shape takes the other entry point and must
		// agree.
		if got := fmtJoinlist(deconstructRangeVars(n)); got != wantStr {
			t.Errorf("deconstructRangeVars(%d):\n  got  %s\n  want %s", n, got, wantStr)
		}
	}
}

// TestJoinCollapseLimitGovernsExplicitJoins pins the `JoinExpr` tail
// (initsplan.c:1410-1441) with collapse ON: an inner JOIN chain folds into one
// problem while the two sides fit `join_collapse_limit`, and splits into two
// subproblems when they do not.
func TestJoinCollapseLimitGovernsExplicitJoins(t *testing.T) {
	const chain4 = "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x JOIN d ON c.x = d.x"
	tests := []struct {
		name  string
		from  string
		limit int
		want  string
	}{
		// Room for all four: one 4-way problem.
		{"chain of 4 under limit 8", chain4, 8, "[0 1 2 3]"},
		// Exactly four: still combines (the test is `<=`).
		{"chain of 4 at limit 4", chain4, 4, "[0 1 2 3]"},
		// One short. The outermost join is the one that overflows
		// (left = [0 1 2], right = [3] → 4 > 3), so the fold stops
		// there and leaves the inner 3-way as a subproblem.
		{"chain of 4 at limit 3", chain4, 3, "[[0 1 2] 3]"},
		// Two joins deep before the overflow: a⋈b combines (2 <= 2),
		// then (a⋈b)⋈c is 3 > 2 → [[0 1] 2], then ⋈d is 2+1 = 3 > 2 →
		// the whole left side becomes one subproblem beside d.
		{"chain of 4 at limit 2", chain4, 2, "[[[0 1] 2] 3]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lim := collapseLimits{fromCollapseLimit: defaultFromCollapseLimit, joinCollapseLimit: tc.limit}
			checkJoinlist(t, tc.from, lim, true, tc.want)
		})
	}
}

// TestJoinCollapseLimitOnePinsSyntacticOrder pins the escape hatch goopg has
// never had — and the surprise in it: at =1 the pin does NOT bite until the
// third relation, because a one-element side is unwrapped rather than wrapped
// (initsplan.c:1428-1436), making the "cannot combine" answer for a two-way
// join identical to the combined one.
func TestJoinCollapseLimitOnePinsSyntacticOrder(t *testing.T) {
	lim := collapseLimits{fromCollapseLimit: defaultFromCollapseLimit, joinCollapseLimit: 1}
	// Two-way: both members stay at top level, so =1 constrains nothing.
	checkJoinlist(t, "a JOIN b ON a.x = b.x", lim, true, "[0 1]")
	// Three-way: a⋈b must be built before c joins it.
	checkJoinlist(t, "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x", lim, true, "[[0 1] 2]")
	// Four-way: fully left-deep in syntactic order.
	checkJoinlist(t, "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x JOIN d ON c.x = d.x",
		lim, true, "[[[0 1] 2] 3]")
}

// TestCollapseFlagOffPinsExplicitJoins pins the sub-flag's OFF semantics
// (08 §2): today's behaviour, in which an explicit JOIN is never reordered, so
// the chain enters the enclosing problem as one opaque item — the same shape
// upstream gives a FULL JOIN (initsplan.c:1414-1418).
//
// Note the flat-comma sibling is unaffected: `d` stays a first-class member.
func TestCollapseFlagOffPinsExplicitJoins(t *testing.T) {
	lim := defaultCollapseLimits()
	checkJoinlist(t, "a JOIN b ON a.x = b.x", lim, false, "[[[0] [1]]]")
	checkJoinlist(t, "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x", lim, false, "[[[[[0] [1]]] [2]]]")
	checkJoinlist(t, "a JOIN b ON a.x = b.x, d", lim, false, "[[[0] [1]] 2]")
	// …and ON, the same two queries open up.
	checkJoinlist(t, "a JOIN b ON a.x = b.x", lim, true, "[0 1]")
	checkJoinlist(t, "a JOIN b ON a.x = b.x, d", lim, true, "[0 1 2]")
}

// TestOuterJoinsStayPinned pins which outer joins still take upstream's FULL
// treatment — order forced at the node — regardless of the collapse flag.
//
// C-04a re-pinned this. 03 §4.4's "goopg has no join_is_legal constraint
// inference" no longer holds: C-01 infers the SpecialJoinInfos and C-03b/c
// build LEFT paths and plans from them, so LEFT is now collapse-dependent
// exactly as INNER is. RIGHT (C-04b) and FULL (ledgered, DESIGN §3.6) still
// pin unconditionally, and FULL's tree-side safety RESTS on that.
func TestOuterJoinsStayPinned(t *testing.T) {
	lim := defaultCollapseLimits()
	for _, spelling := range []string{"RIGHT JOIN", "FULL JOIN"} {
		from := "a " + spelling + " b ON a.x = b.x"
		for _, collapse := range []bool{false, true} {
			checkJoinlist(t, from, lim, collapse, "[[[0] [1]]]")
		}
	}
	// LEFT: pinned with the flag off, flattened with it on — the INNER rule.
	checkJoinlist(t, "a LEFT JOIN b ON a.x = b.x", lim, false, "[[[0] [1]]]")
	checkJoinlist(t, "a LEFT JOIN b ON a.x = b.x", lim, true, "[0 1]")
	// An INNER chain topped by a LEFT link is now ONE three-member problem —
	// the C-04a payload, and the shape Q72 has.
	checkJoinlist(t, "a JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x",
		lim, true, "[0 1 2]")
	// A RIGHT link still pins from that node up.
	checkJoinlist(t, "a JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x",
		lim, true, "[[[0 1] [2]]]")
	// CROSS JOIN is upstream's JOIN_INNER with no quals, so it collapses
	// with the inner arm rather than pinning.
	checkJoinlist(t, "a CROSS JOIN b", lim, true, "[0 1]")
}

// TestFromCollapseLimitGovernsSubJoinlists pins initsplan.c:1233-1238's second
// arm: `from_collapse_limit` decides whether a MULTI-relation subproblem is
// merged into the comma list, and the decision uses the joinlist's FINAL width
// (`+ remaining`), not its width so far — so the answer does not depend on
// which item happens to be processed first.
func TestFromCollapseLimitGovernsSubJoinlists(t *testing.T) {
	// Three comma items, each a 2-way inner JOIN that collapse folds to two
	// members: the whole FROM is 6 relations.
	const from = "a JOIN b ON a.x = b.x, c JOIN d ON c.x = d.x, e JOIN f ON e.x = f.x"
	tests := []struct {
		limit int
		want  string
	}{
		// Room for all six: one 6-way problem.
		{8, "[0 1 2 3 4 5]"},
		{6, "[0 1 2 3 4 5]"},
		// Five: item 1 sees 0 + 2 + 2 = 4 <= 5 and merges; item 2 sees
		// 2 + 2 + 1 = 5 <= 5 and merges; item 3 sees 4 + 2 + 0 = 6 > 5
		// and becomes a subproblem. Note the `remaining` term is what
		// makes item 1's test 4 rather than 2 — the check is against the
		// width the joinlist will END at.
		{5, "[0 1 2 3 [4 5]]"},
		// Three: item 1 already fails (0 + 2 + 2 = 4 > 3), and so does
		// every later one, so no 2-member subproblem merges.
		{3, "[[0 1] [2 3] [4 5]]"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("from_collapse_limit=%d", tc.limit), func(t *testing.T) {
			lim := collapseLimits{fromCollapseLimit: tc.limit, joinCollapseLimit: defaultJoinCollapseLimit}
			checkJoinlist(t, from, lim, true, tc.want)
		})
	}
}

// TestJoinlistCountsAndLeaves pins the two accessors a consumer reaches for:
// `nrels` is the problem size 03 §7's `maxSearchRels` ceiling is compared
// against, and `leaves` is the depth-first relation set.
func TestJoinlistCountsAndLeaves(t *testing.T) {
	jl := deconstructJointree(
		parseFrom(t, "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x, d"),
		collapseLimits{fromCollapseLimit: 8, joinCollapseLimit: 2},
		true,
	)
	if got, want := fmtJoinlist(jl), "[[0 1] 2 3]"; got != want {
		t.Fatalf("joinlist:\n  got  %s\n  want %s", got, want)
	}
	if got := jl.nrels(); got != 4 {
		t.Errorf("nrels() = %d, want 4", got)
	}
	if got := fmt.Sprint(jl.leaves(nil)); got != "[0 1 2 3]" {
		t.Errorf("leaves() = %s, want [0 1 2 3]", got)
	}
	// A pinned outer join is ONE joinlist member but still two relations —
	// the distinction `nrels` exists to make. Spelled with FULL since C-04a:
	// LEFT no longer pins (see TestOuterJoinsStayPinned).
	pinned := deconstructJointree(parseFrom(t, "a FULL JOIN b ON a.x = b.x"), defaultCollapseLimits(), true)
	if len(pinned) != 1 {
		t.Errorf("pinned outer join: %d members, want 1", len(pinned))
	}
	if got := pinned.nrels(); got != 2 {
		t.Errorf("pinned outer join: nrels() = %d, want 2", got)
	}
}

// TestJoinlistLeavesMatchBindings pins the correspondence the joinlist is
// worth anything for: a leaf's `rel` is a subscript into
// `resolveContext.bindings`, so a consumer hands `bindings[rel]` /
// `scans[rel]` / `relInfos[rel]` straight to `buildInitialRels` with no name
// matching anywhere.
//
// It goes through the production `planFromClause` rather than through
// `deconstructJointree` alone, because the claim under test is about two walks
// agreeing — the one that numbers leaves and the one that appends bindings —
// and only the real entry point runs both.
func TestJoinlistLeavesMatchBindings(t *testing.T) {
	cat := catalog.NewInMemory()
	for _, name := range []string{"a", "b", "c", "d"} {
		cols := []catalog.Column{{Name: name + "x", Type: catalog.Type{Name: "int8"}}}
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	for _, q := range []string{
		"SELECT * FROM a, b, c, d",
		"SELECT * FROM a JOIN b ON a.ax = b.bx, c, d",
		"SELECT * FROM a JOIN b ON a.ax = b.bx JOIN c ON b.bx = c.cx JOIN d ON c.cx = d.dx",
		"SELECT * FROM a LEFT JOIN b ON a.ax = b.bx, c JOIN d ON c.cx = d.dx",
	} {
		t.Run(q, func(t *testing.T) {
			stmts, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), cat, DefaultPlannerSettings(), nil)
			if err != nil {
				t.Fatalf("planFromClause: %v", err)
			}
			if rctx.joinlist == nil {
				t.Fatal("planFromClause left resolveContext.joinlist nil")
			}
			leaves := rctx.joinlist.leaves(nil)
			if len(leaves) != len(rctx.bindings) {
				t.Fatalf("joinlist has %d leaves but the FROM clause bound %d relations",
					len(leaves), len(rctx.bindings))
			}
			// Depth-first leaf order IS binding order: no collapse
			// decision may permute or drop a relation, only group it.
			for i, rel := range leaves {
				if rel != i {
					t.Fatalf("leaf %d is rel %d; leaves = %v, want 0..%d in order",
						i, rel, leaves, len(leaves)-1)
				}
			}
			if got := rctx.joinlist.nrels(); got != len(rctx.bindings) {
				t.Errorf("nrels() = %d, want %d", got, len(rctx.bindings))
			}
		})
	}
}

// TestCollapseLimitsMatchConfigDefaults is the drift guard `defaultCostParams`
// has for the cost GUCs: the numbers the planner plans against must be the
// numbers `SHOW from_collapse_limit` reports, or the plan disagrees with the
// session in a way nobody can observe from either side.
func TestCollapseLimitsMatchConfigDefaults(t *testing.T) {
	reg := misc.BuildDefaultRegistry()
	for _, tc := range []struct {
		name string
		want int
	}{
		{"from_collapse_limit", defaultFromCollapseLimit},
		{"join_collapse_limit", defaultJoinCollapseLimit},
	} {
		v, ok := reg.Get(tc.name)
		if !ok {
			t.Fatalf("GUC %s is not registered", tc.name)
		}
		if v.BootVal != fmt.Sprint(tc.want) {
			t.Errorf("%s BootVal = %q, planner default = %d", tc.name, v.BootVal, tc.want)
		}
	}
}
