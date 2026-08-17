package optimizer

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

// levelHasSearchableInnerPrefix reports whether any query level of sql presents
// the shape M0127-P5.9-s peels: a joinlist topped by pinned LEFT links with at
// least two relations below them.
//
// It is the production predicate, not a paraphrase — `deconstructJointree` and
// `innerPrefixBelowOuterSpine` are the same calls the seam makes, at the same
// flag value — because P5.9-m's whole lesson was that a corpus measurement
// re-derived from the SQL text answers a different question than the planner
// does (the `72,75` note above: `grep -c ' join '` finds three eligible queries
// where `deconstructJointree` finds two).
//
// The plan-tree half of the seam's check (`splitOuterSpine`) is not modelled: it
// can only DECLINE what this admits, so the count below is an upper bound on the
// corpus population, and it is labelled as one.
func levelHasSearchableInnerPrefix(sql string, collapseJoins bool) (bool, error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, err
	}
	for _, st := range stmts {
		for _, sel := range selectLevels(st) {
			if len(sel.FromExprs) == 0 {
				continue
			}
			jl := deconstructJointree(sel.FromExprs, defaultCollapseLimits(), collapseJoins)
			prefix, spine := jl.innerPrefixBelowOuterSpine()
			if len(spine) == 0 || prefix.nrels() < 2 {
				continue
			}
			allLeft := true
			for _, t := range spine {
				if t != parser.JoinLeft {
					allLeft = false
					break
				}
			}
			if allLeft {
				return true, nil
			}
		}
	}
	return false, nil
}

// TestCorpusQueriesWithASearchableInnerPrefix is P5.9-s's counterpart to
// `TestNoCorpusQueryHasAnInnerOnlyJoinChain` above, and it is the fact that
// distinguishes this task from P5.9-r: the INNER walk reached zero corpus
// queries, and the peel does not.
//
// The set is PINNED rather than merely logged, for the reason the collapse-
// eligible set above is: it is the blast radius of the DS05 plan channel. If a
// query enters or leaves it, the number of plans the arm can move changed, and
// the acceptance bar's "same=99" needs re-reading rather than re-quoting.
//
// Measured at collapse ON, because that is the regime in which an inner prefix
// flattens into one searchable problem. With collapse OFF each inner link is
// itself pinned, so the prefix is a two-member subproblem whose order is forced
// and only its PATHS are searched — reachable, but not reorderable.
func TestCorpusQueriesWithASearchableInnerPrefix(t *testing.T) {
	entries, err := os.ReadDir(tpcdsCorpusDir)
	if err != nil {
		t.Skipf("TPC-DS corpus not present (%v)", err)
	}
	nameRE := regexp.MustCompile(`^query([0-9]+)\.sql$`)
	var peelable []int
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
		ok, perr := levelHasSearchableInnerPrefix(string(sql), true)
		if perr != nil {
			continue
		}
		if ok {
			peelable = append(peelable, qn)
		}
	}
	sort.Ints(peelable)
	// The same two queries the COLLAPSE arm is eligible on, and for the same
	// reason: `deconstructJointree` is what decides both, so a chain it pins
	// into one opaque item (Q78's `A LEFT JOIN B … JOIN date_dim …`, where the
	// outer link sits BELOW an inner one) is out of reach of the peel as well —
	// lifting it needs 03 §4.4's `SpecialJoinInfo` inference, not a spine walk.
	// Zero before this task, so the corpus population went 0 -> 2.
	const want = "72,75"
	if got := sprintInts(peelable); got != want {
		t.Errorf("TPC-DS queries with a searchable INNER prefix below a LEFT spine = {%s}, "+
			"want {%s} (of %d).\nThis is the DS05 plan channel's blast radius for the peel; "+
			"if it changed, re-run 09 §3.19's protocol rather than re-quoting its numbers.",
			got, want, total)
	}
	t.Logf("TPC-DS corpus: %d queries, %d with a peelable LEFT spine over a >=2-relation "+
		"inner prefix (upper bound: the plan-tree half of the check can still decline)",
		total, len(peelable))
}

// chainIsInnerOnly reports whether EVERY explicit `JOIN` in this statement's
// FROM clauses is INNER or CROSS — the property `extractSearchLeaves`
// (joinsearchseam.go) needs to flatten a chain, since the walk stops at an outer
// link and hands the whole chain back as one leaf.
//
// A statement with no explicit JOIN at all answers false: it has no chain for
// the walk to flatten, so it is not a member of the population this measures.
func chainIsInnerOnly(sql string) (innerOnly bool, err error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, err
	}
	sawJoin := false
	for _, st := range stmts {
		for _, sel := range selectLevels(st) {
			for _, item := range sel.FromExprs {
				for _, j := range item.Joins {
					sawJoin = true
					if j.Type != parser.JoinInner && j.Type != parser.JoinCross {
						return false, nil
					}
				}
			}
		}
	}
	return sawJoin, nil
}

// TestNoCorpusQueryHasAnInnerOnlyJoinChain is the measurement that explains why
// M0127-P5.9-r changed no plan, and it is the fact the next attempt on the
// collapse flip has to start from.
//
// P5.9-r lifted the precondition P5.9-m recorded: the seam now flattens an
// explicit INNER chain and routes its `ON` quals into the search's clause list,
// which `TestCollapseReachesTheSearch` demonstrates on a three-relation chain.
// The DS05 plan A/B nevertheless reported `queries=99 same=99 changed=0` under
// collapse OFF *and* ON, and this is why: of the 99 TPC-DS queries, **twelve**
// spell an explicit JOIN and **every one of the twelve** contains an outer join.
// The walk stops at the outer link, the leaf count disagrees with the binding
// count (`DPTRACE seam-decline reason=leaf-count nrels=11 nleaves=1` on Q72),
// and the statement falls back to the syntactic shape exactly as before. TPC-H
// is the same story with one query: Q13's only explicit join is a LEFT OUTER.
//
// So the corpus contains NO statement this walk can act on, and the collapse
// flip stays a no-go for a NEW reason — one level deeper than P5.9-m's. The
// remaining blocker was that `joinlistItem` (collapse.go) carried no join TYPE:
// `joinPinned` correctly wraps an outer join into its own two-member
// subproblem, but nothing downstream could rebuild it AS an outer join, so
// admitting one would silently plan a LEFT JOIN as an INNER JOIN. The leaf-count
// decline was what stood between that latent shape and a wrong answer, which is
// why P5.9-r kept it rather than widening the walk further.
//
// M0127-P5.9-s closed that: the item carries its type, `makeRelFromJoinlist`
// REFUSES a pinned outer subproblem outright, and the seam peels a LEFT spine off
// the top and searches the inner prefix below it. This test's own claim is
// unchanged and still true — no corpus query is INNER-only — but it is no longer
// the reason the corpus cannot move: `TestCorpusQueriesWithASearchableInnerPrefix`
// measures the population that CAN, and it is {72,75} rather than empty.
//
// This test fails the day a corpus query is written INNER-only — which is the
// day a corpus measurement can move, and therefore the day 09 §3.18's protocol
// is worth re-running.
func TestNoCorpusQueryHasAnInnerOnlyJoinChain(t *testing.T) {
	entries, err := os.ReadDir(tpcdsCorpusDir)
	if err != nil {
		t.Skipf("TPC-DS corpus not present (%v)", err)
	}
	nameRE := regexp.MustCompile(`^query([0-9]+)\.sql$`)
	var innerOnly, withJoin []int
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
		ok, perr := chainIsInnerOnly(string(sql))
		if perr != nil {
			continue
		}
		if ok {
			innerOnly = append(innerOnly, qn)
		}
		if _, _, perr := collapseEligible(string(sql)); perr == nil {
			withJoin = append(withJoin, qn)
		}
	}
	sort.Ints(innerOnly)
	if len(innerOnly) != 0 {
		t.Errorf("TPC-DS queries with an INNER-only explicit-JOIN chain = {%s}, want none (of %d).\n"+
			"A corpus query the seam can now flatten means the DS05 plan A/B can finally move, so "+
			"09 §3.18's collapse protocol should be re-run and M0127-P5.9-m's no-go re-decided.",
			sprintInts(innerOnly), total)
	}
	t.Logf("TPC-DS corpus: %d queries, %d inner-only explicit-JOIN chains", total, len(innerOnly))
}

// TestCollapseReachesTheSearch is the INVERSION of M0127-P5.9-m's
// `TestCollapseDoesNotReachTheSearch`, and it is the fact that makes 03 §6's
// collapse pass a decidable question again.
//
// The flag's whole purpose is to feed explicit-JOIN chains into the PG-shaped
// search: it flattens `a JOIN b ON … JOIN c ON …` into one flat joinlist instead
// of one opaque pinned item. Under P5.9-m that was unobservable, because
// `ctx.joinlist` is only read AFTER `tryPGShapedJoinSearch`'s preconditions
// pass, and `extractScans` descended `JoinTypeCross` and nothing else — so an
// explicit JOIN arrived as ONE node for N bindings and the seam declined before
// the joinlist was consulted, with the flag on OR off. Both arms planned the
// identical tree, which is what the DS05 plan A/B measured across the whole
// TPC-DS corpus (`same=99 changed=0`) and what the empty enumeration trace on
// Q72's eleven-way level explained.
//
// M0127-P5.9-r lifted that precondition (`extractSearchLeaves`,
// joinsearchseam.go). This test pins the consequence from the collapse side:
// BOTH regimes now reach the search, so the flag decides a join ORDER rather
// than deciding nothing, and M0127-P5.9-m's no-go — which was a statement about
// a flag that could not move a plan — no longer stands on its own evidence and
// has to be re-measured by 09 §3.18's protocol.
//
// What it deliberately does NOT assert is that the two regimes differ on THIS
// fixture: three relations under `join_collapse_limit` is a shape where the
// pinned order and a searched order can legitimately coincide. The claim under
// test is reachability, which is what was false before.
//
// Reachability in the PLANNER is not reachability in the CORPUS, and the two
// must not be confused — confusing them is the exact defect P5.9-m recorded.
// `TestNoCorpusQueryHasAnInnerOnlyJoinChain` above measures the second: no
// TPC-DS or TPC-H query is written INNER-only, so the re-run of 09 §3.18's
// protocol still reports `same=99 changed=0` and the flip is still a no-go.
// This test says the door is open; that one says nobody walks through it yet.
func TestCollapseReachesTheSearch(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	for _, collapse := range []bool{false, true} {
		prev := pgShapedCollapse
		pgShapedCollapse = collapse
		// The chain `planFromItem` builds for that FROM clause, and the
		// joinlist the flag actually produces for it — so the only thing
		// varying between the two arms is the regime.
		node, ctx := seamInnerChain(t, names, []int64{100, 100, 100})
		out, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
		pgShapedCollapse = prev
		if !used {
			t.Fatalf("collapse=%v: the seam declined an explicit-JOIN chain — the "+
				"P5.9-r walk has regressed and the collapse flag is unobservable again",
				collapse)
		}
		got := seamEqualities(out)
		for _, want := range []string{"a0=b0", "b0=c0"} {
			if !got[want] {
				t.Fatalf("collapse=%v: the searched tree does not enforce %s (enforces %v)",
					collapse, want, got)
			}
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
