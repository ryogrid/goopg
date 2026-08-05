package planner

// M0127-P5.9-m — what a COLLAPSE arm of the 09 §3 acceptance bar can and cannot
// see.
//
// 08 §2 gates the S5 row on running the bar "once with collapse OFF, then with
// collapse ON", and the collapse-ON pass gates the `GOOPG_PGSHAPED_COLLAPSE`
// flip. Running it produced 24/24 value MATCH on TPC-H SF1 at a fixed binary
// (09 §3.18) — and that green says nothing about the flag, because
// `GOOPG_PGSHAPED_COLLAPSE` only acts on an explicit INNER/CROSS JOIN and the
// TPC-H corpus contains none: its one explicit join is Q13's LEFT OUTER JOIN,
// which `joinPinned` pins in BOTH regimes.
//
// That is the failure mode this milestone keeps re-discovering one instrument
// at a time — §3.15's headline number measured ON→ON, §3.16's provenance label
// named the opposite of the regime it ran under. Both were gates reporting a
// number about a variable they did not vary. So the corpus's eligibility is
// pinned here rather than asserted in prose: a COLLAPSE arm over a corpus with
// zero eligible statements is a CONTROL, and the day someone re-spells a
// benchmark query with `JOIN … ON` this test fails and says the arm has become
// a real test.
//
// The measurement runs the production function (`deconstructJointree`) over the
// production parse of each corpus query, at both flag values, and asks whether
// any query level's joinlist differs. Nothing here re-implements the rule.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// tpcdsCorpusDir is the dsqgen output the SF0.5 gate sweeps. It lives under
// bench/ rather than in the module, so the TPC-DS half of the measurement skips
// when the bench tree is absent; the TPC-H half never does.
const tpcdsCorpusDir = "../../bench/tpcds/runtime_goopg/tpcds-data/queries"

// collapseEligible reports whether `GOOPG_PGSHAPED_COLLAPSE` changes the search
// population of any query level in sql, and returns the levels' rendered
// joinlists for the OFF regime so a failure can name the shape it saw.
//
// "Query level" is one `*parser.SelectStmt` with a FROM clause the JOIN-aware
// entry point handles — the same condition planner.go:2008 uses to call
// `deconstructJointree`. A level whose FROM is the JOIN-free spelling goes to
// `deconstructRangeVars`, which takes no limits and no flag, so it can never be
// eligible (collapse.go's own note: a flat comma list is one problem either
// way).
func collapseEligible(sql string) (eligible bool, levels []string, err error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, nil, err
	}
	for _, st := range stmts {
		for _, sel := range selectLevels(st) {
			if len(sel.FromExprs) == 0 {
				continue
			}
			lim := defaultCollapseLimits()
			off := fmtJoinlist(canonJoinlist(deconstructJointree(sel.FromExprs, lim, false)))
			on := fmtJoinlist(canonJoinlist(deconstructJointree(sel.FromExprs, lim, true)))
			levels = append(levels, off)
			if off != on {
				eligible = true
			}
		}
	}
	return eligible, levels, nil
}

// canonJoinlist strips the nesting levels that carry no search decision, so two
// joinlists compare equal iff they pose the same SET of search problems.
//
// The pinned arm wraps a JOIN as `list_make1(list_make2(left, right))`
// (initsplan.c:1417) and upstream does not unwrap it, because
// `make_rel_from_joinlist` recurses on a one-element sub-list and returns that
// rel unchanged (allpaths.c:3391 — collapse.go:322 says so in as many words).
// Rendered, that makes `a JOIN b` read `[[[0] [1]]]` with the flag off and
// `[0 1]` with it on — a textual difference over a problem that has exactly one
// possible pairing either way. Counting it would report every two-way explicit
// join in a corpus as collapse-eligible and inflate the arm's blast radius,
// which is the opposite of the error this file was written to catch but the
// same defect: a number that does not measure what it names.
func canonJoinlist(jl joinlist) joinlist {
	// An inert wrapper is a one-item list whose item is a sub-list: peel it
	// until the outermost list carries a real decision (≥ 2 items) or a leaf.
	for len(jl) == 1 && !jl[0].isLeaf() {
		jl = jl[0].sub
	}
	out := make(joinlist, len(jl))
	for i, it := range jl {
		if it.isLeaf() {
			out[i] = it
			continue
		}
		sub := canonJoinlist(it.sub)
		if len(sub) == 1 {
			out[i] = sub[0] // "avoid creating useless 1-element sublists"
			continue
		}
		out[i] = subItem(sub)
	}
	return out
}

// selectLevels collects every `*parser.SelectStmt` reachable from a parsed
// statement — the top level plus every sub-select, CTE body and set-op branch.
//
// It walks by reflection over EXPORTED fields rather than by a hand-written
// type switch, deliberately: a switch would silently stop finding levels the
// day the AST grows a node kind, and a corpus-eligibility number that quietly
// under-counts is exactly the kind of green this file exists to prevent.
// `TestCollapseInstrumentFindsNestedLevels` is the guard that the walk reaches
// a sub-select at all.
func selectLevels(root any) []*parser.SelectStmt {
	var out []*parser.SelectStmt
	seen := map[uintptr]bool{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			if v.Kind() == reflect.Pointer {
				if p := v.Pointer(); seen[p] {
					return
				} else {
					seen[p] = true
				}
				if sel, ok := v.Interface().(*parser.SelectStmt); ok {
					out = append(out, sel)
				}
			}
			walk(v.Elem())
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(v.MapIndex(k))
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if t.Field(i).PkgPath != "" { // unexported: not reachable
					continue
				}
				walk(v.Field(i))
			}
		}
	}
	// Hand the root to `walk` rather than special-casing it: pre-marking the
	// root pointer as seen made the walk return at its first step and report
	// only the top level — the under-count this function's doc comment warns
	// about, produced by the guard against it (M0127-P5.9-m).
	walk(reflect.ValueOf(root))
	return out
}

// TestCollapseInstrumentFindsNestedLevels is the instrument's positive control.
// A measurement that reports "0 eligible" is only evidence if the same code
// reports non-zero when an eligible statement IS present — including one where
// the eligible level is a SUB-select, since a walk that only looked at the top
// level would under-count every corpus query that hides its joins in a CTE.
func TestCollapseInstrumentFindsNestedLevels(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "top-level inner join chain",
			sql:  "SELECT * FROM a JOIN b ON a.x = b.x JOIN c ON b.x = c.x",
			want: true,
		},
		{
			name: "inner join chain inside a sub-select",
			sql:  "SELECT * FROM (SELECT x FROM a JOIN b ON a.x = b.x JOIN c ON b.x = c.x) s",
			want: true,
		},
		{
			name: "inner join chain inside a CTE body",
			sql:  "WITH w AS (SELECT a.x FROM a JOIN b ON a.x = b.x JOIN c ON b.x = c.x) SELECT * FROM w",
			want: true,
		},
		{
			name: "left outer join is pinned in both regimes",
			sql:  "SELECT * FROM a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x",
			want: false,
		},
		{
			name: "flat comma list is one problem either way",
			sql:  "SELECT * FROM a, b, c, d WHERE a.x = b.x",
			want: false,
		},
		{
			name: "two-way inner join is unaffected (one item either way)",
			sql:  "SELECT * FROM a JOIN b ON a.x = b.x",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, levels, err := collapseEligible(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Errorf("collapseEligible(%q) = %v, want %v (levels %v)", tc.sql, got, tc.want, levels)
			}
		})
	}
}

// TestCollapseIsAControlOnTheTPCHCorpus pins the fact that makes 09 §3.18's
// green a control: not one of the 22 TPC-H queries contains a join
// `GOOPG_PGSHAPED_COLLAPSE` can act on, so both arms of the collapse-ON
// acceptance pass plan byte-identical search problems and any timing difference
// between them is host noise by construction.
//
// If this test fails, the corpus changed and the bar's collapse arm now MEASURES
// something. That is a promotion, not a break: update the count here and re-read
// the arm's result as evidence about the flag rather than about the host.
func TestCollapseIsAControlOnTheTPCHCorpus(t *testing.T) {
	queries := tpch.Queries()
	var eligible []int
	unparsed := 0
	for qn, sql := range queries {
		got, _, err := collapseEligible(sql)
		if err != nil {
			// A query this planner cannot parse cannot be an arm of any
			// bar either; count it so the denominator stays honest.
			unparsed++
			t.Logf("Q%d: parse failed (%v)", qn, err)
			continue
		}
		if got {
			eligible = append(eligible, qn)
		}
	}
	sort.Ints(eligible)
	if len(eligible) != 0 {
		t.Errorf("TPC-H corpus: %d collapse-eligible queries %v, want 0 — the bar's "+
			"collapse arm is no longer a control; re-read 09 §3.18 with this in mind",
			len(eligible), eligible)
	}
	if unparsed != 0 {
		t.Errorf("TPC-H corpus: %d of %d queries did not parse", unparsed, len(queries))
	}
	t.Logf("TPC-H corpus: %d queries, %d collapse-eligible (Q13's LEFT OUTER JOIN is pinned in both regimes)",
		len(queries), len(eligible))
}

// TestCollapseEligibilityOfTheTPCDSCorpus is the other half of the same
// question, on the corpus the DS05 clause of the bar sweeps. Here the answer is
// NOT zero — three queries are written as inner-JOIN chains — which is why the
// DS05 arm, not the TPC-H arm, is the collapse pass's decisive channel.
//
// Skips when the bench tree is absent (the queries are dsqgen output under
// bench/, not module data), so this never turns a clean checkout red.
func TestCollapseEligibilityOfTheTPCDSCorpus(t *testing.T) {
	entries, err := os.ReadDir(tpcdsCorpusDir)
	if err != nil {
		t.Skipf("TPC-DS corpus not present (%v)", err)
	}
	// queryN.sql only: query_0.sql is dsqgen's concatenation of the whole
	// stream and would double-count every level in the directory.
	nameRE := regexp.MustCompile(`^query([0-9]+)\.sql$`)
	var eligible, unparsed []int
	total := 0
	for _, e := range entries {
		m := nameRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		qn, _ := strconv.Atoi(m[1])
		sql, err := os.ReadFile(filepath.Join(tpcdsCorpusDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		total++
		got, _, perr := collapseEligible(string(sql))
		if perr != nil {
			unparsed = append(unparsed, qn)
			continue
		}
		if got {
			eligible = append(eligible, qn)
		}
	}
	sort.Ints(eligible)
	sort.Ints(unparsed)
	got := sprintInts(eligible)
	// Two, not the three a `grep -c ' join '` finds. Q78 spells three inner
	// JOINs and is still ineligible: each of its chains is
	// `A LEFT JOIN B … JOIN date_dim …`, and the pinned outer join has already
	// folded its two sides into ONE joinlist item, so the inner join that
	// follows offers a two-member problem in both regimes. The lexical count
	// and the planner's count differ, and the planner's is the one the arm
	// runs — which is the whole reason this measurement goes through
	// `deconstructJointree` instead of through a regex.
	const want = "72,75"
	if got != want {
		t.Errorf("TPC-DS collapse-eligible set = {%s}, want {%s} (of %d parsed; %d unparseable %v).\n"+
			"The DS05 clause of the collapse-ON pass is the arm that measures the flag; if this set "+
			"changed, that arm's blast radius changed with it.", got, want, total, len(unparsed), unparsed)
	}
	t.Logf("TPC-DS corpus: %d queries, %d unparseable %v, collapse-eligible {%s}",
		total, len(unparsed), unparsed, got)
}

// TestCollapseDoesNotReachTheSearch is the measured no-go of M0127-P5.9-m,
// pinned so the flip cannot be attempted again on the strength of a green bar.
//
// The flag's whole purpose is to feed explicit-JOIN chains into the PG-shaped
// search: it flattens `a JOIN b ON … JOIN c ON …` into one flat joinlist
// instead of one opaque pinned item. But `ctx.joinlist` is only ever read AFTER
// `tryPGShapedJoinSearch`'s preconditions pass, and one of those preconditions
// is that the pre-search node's leaves enumerate to the binding count —
// `extractScans` (bushy.go:261) descends `JoinTypeCross` and nothing else, so an
// explicit JOIN arrives as ONE node for N bindings and the seam declines before
// the joinlist is consulted (`TestPGShapedSeamDeclines/leaf count disagrees
// with binding count` is the same gate from the other side).
//
// So collapse ON and collapse OFF plan the identical tree for every statement
// the flag acts on, which is what the DS05 plan A/B measured across the whole
// TPC-DS corpus at a fixed binary (`same=99 changed=0`) and what the
// enumeration trace explains: on Q72's eleven-way explicit-JOIN level the
// search emitted no trace at all, in either regime.
//
// Flipping the default on that evidence would advance 08 §2's S5 row with a
// change that cannot move a plan. The unblock is the seam, not the collapse
// pass — see the deferral-ledger row for the resume point.
func TestCollapseDoesNotReachTheSearch(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	for _, collapse := range []bool{false, true} {
		prev := pgShapedCollapse
		pgShapedCollapse = collapse
		node, ctx := seamFixture(names, []int64{100, 100, 100})
		// What `planFromItem` builds for `a JOIN b ON … JOIN c ON …`: the
		// chain is INNER joins carrying their own ON quals, not the CROSS
		// chain a comma FROM list produces.
		chain := node.(*Join)
		chain.Left.(*Join).Type = JoinTypeInner
		fused := &Join{Type: JoinTypeInner, Left: chain.Left, Right: chain.Right,
			schema: append(Schema(nil), chain.Output()...)}
		// The joinlist the flag actually produces for that FROM clause,
		// so the only thing varying between the two arms is the regime.
		ctx.joinlist = deconstructJointree(
			parseFrom(t, "a JOIN b ON a.a0 = b.b0 JOIN c ON b.b0 = c.c0"),
			defaultCollapseLimits(), collapse)
		_, _, used := tryPGShapedJoinSearch(fused, rfjEq(names, 0, 1), ctx, nil)
		pgShapedCollapse = prev
		if used {
			t.Fatalf("collapse=%v: the seam searched an explicit-JOIN chain — the "+
				"precondition this milestone recorded as the collapse flip's blocker "+
				"has been lifted, so M0127-P5.9-m's no-go should be re-measured",
				collapse)
		}
	}
}

// sprintInts renders an int slice as a bare comma list, which is what the
// eligible-set pin above is written against.
func sprintInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}
