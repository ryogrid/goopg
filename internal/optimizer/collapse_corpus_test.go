package optimizer

// M0127-P5.9-m, re-based by take3 C-06 — which corpus queries have an explicit
// JOIN whose ORDER the search chooses.
//
// The file was written to instrument the `GOOPG_PGSHAPED_COLLAPSE` arm of the
// 09 §3 acceptance bar: it measured, per corpus query, whether the flag's two
// values posed a DIFFERENT set of search problems, so that a green arm over a
// corpus with zero eligible statements could be read as the CONTROL it was.
// That was the failure mode the milestone kept re-discovering one instrument at
// a time — §3.15's headline number measured ON→ON, §3.16's provenance label
// named the opposite of the regime it ran under: gates reporting a number about
// a variable they did not vary.
//
// Take3 C-06 retired the flag (2026-09-07): explicit INNER/LEFT/RIGHT/CROSS
// links flatten unconditionally, and the two-valued question has no second
// value left to ask. The measurement is kept and re-based on the question that
// survives the flag, which is the one the sets below were always USED for — the
// DS05 plan channel's blast radius: for which queries does an explicit JOIN
// chain enter the enclosing search problem, so the search picks its order,
// rather than arriving as one opaque pinned item?
//
// The sets are therefore re-measured against the new predicate rather than
// carried over; the old ones were `{}` (TPC-H) and `{40,49,72,75,78,80,93}`
// (TPC-DS) under "the flag changes this level's problem set".
//
// The measurement runs the production functions (`deconstructJointree`,
// `deconstructFromItem`) over the production parse of each corpus query.
// Nothing here re-implements the rule.

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

// tpcdsFlatteningSet is the pinned answer of
// `TestExplicitJoinFlatteningOnTheTPCDSCorpus` — see that test for what the set
// means and how it has moved.
const tpcdsFlatteningSet = "5,40,49,72,75,77,78,80,93"

// explicitJoinFlattens reports whether any query level of sql has a comma FROM
// item written with an explicit `JOIN` whose chain FLATTENS — i.e. the item
// contributes more than one member to the enclosing joinlist, so the search
// chooses the order of its links instead of inheriting the written one. It
// returns the levels' rendered joinlists so a failure can name the shape it saw.
//
// This is the post-C-06 spelling of what `collapseEligible` measured. It asks
// the production deconstruction directly (`deconstructFromItem`, then
// `canonJoinlist` over the whole level for the diagnostic string) rather than
// diffing two flag arms, because there is only one arm now.
//
// "Query level" is one `*parser.SelectStmt` with a FROM clause the JOIN-aware
// entry point handles — the same condition planner.go uses to call
// `deconstructJointree`. A level whose FROM is the JOIN-free spelling goes to
// `deconstructRangeVars`, which has no explicit JOIN to flatten, so it can
// never qualify (collapse.go's own note: a flat comma list is one problem
// however it is spelled).
func explicitJoinFlattens(sql string) (flattens bool, levels []string, err error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, nil, err
	}
	lim := defaultCollapseLimits()
	for _, st := range stmts {
		for _, sel := range selectLevels(st) {
			if len(sel.FromExprs) == 0 {
				continue
			}
			levels = append(levels, fmtJoinlist(canonJoinlist(deconstructJointree(sel.FromExprs, lim))))
			rel := 0
			for _, item := range sel.FromExprs {
				sub := deconstructFromItem(item, rel, lim)
				rel += fromItemRels(item)
				if len(item.Joins) > 0 && len(sub) > 1 {
					flattens = true
				}
			}
		}
	}
	return flattens, levels, nil
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
// A measurement that reports a small count is only evidence if the same code
// reports non-zero when a qualifying statement IS present — including one where
// that level is a SUB-select, since a walk that only looked at the top level
// would under-count every corpus query that hides its joins in a CTE.
func TestJoinFlatteningInstrumentFindsNestedLevels(t *testing.T) {
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
			// C-04a: LEFT no longer pins, so a LEFT chain flattens into one
			// problem exactly as an INNER one does. This case read `false` up
			// to C-04a and is the instrument's own witness that the pin
			// relaxed.
			name: "left outer join chain flattens (C-04a)",
			sql:  "SELECT * FROM a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x",
			want: true,
		},
		{
			// FULL still pins — C-04 leaves it alone deliberately
			// (DESIGN §3.6), and its safety rests on that. It is the only
			// join type left that does, since C-06.
			name: "full outer join is pinned",
			sql:  "SELECT * FROM a FULL JOIN b ON a.x = b.x FULL JOIN c ON b.x = c.x",
			want: false,
		},
		{
			// C-04b: RIGHT flattens too. The joinlist flattens both links;
			// whether the SEAM then admits the plan-side shape is a separate
			// question (a RIGHT under a RIGHT's nullable side declines there
			// — joinsearchspine_test.go).
			name: "right outer join chain flattens (C-04b)",
			sql:  "SELECT * FROM a RIGHT JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x",
			want: true,
		},
		{
			name: "flat comma list has no explicit JOIN to flatten",
			sql:  "SELECT * FROM a, b, c, d WHERE a.x = b.x",
			want: false,
		},
		{
			// Read `false` under the retired flag — the two arms posed the
			// SAME problem set for a two-way join, because upstream unwraps a
			// one-element side (initsplan.c:1428-1436). It reads `true` under
			// the predicate that replaced it, and correctly so: the item DOES
			// contribute two members, and the search DOES pick which side
			// builds. The two questions differ exactly here.
			name: "two-way inner join flattens into two members",
			sql:  "SELECT * FROM a JOIN b ON a.x = b.x",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, levels, err := explicitJoinFlattens(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Errorf("explicitJoinFlattens(%q) = %v, want %v (levels %v)", tc.sql, got, tc.want, levels)
			}
		})
	}
}

// TestExplicitJoinFlatteningOnTheTPCHCorpus pins which TPC-H queries have an
// explicit JOIN whose order the search picks.
//
// It used to assert ZERO, and to mean something else: under
// `GOOPG_PGSHAPED_COLLAPSE` the count was the number of queries the flag could
// act on, and zero was what made 09 §3.18's collapse arm a CONTROL rather than
// a measurement. Q13's `LEFT OUTER JOIN` did not count, because `joinPinned`
// pinned it in both regimes.
//
// C-04a relaxed the LEFT pin and C-06 retired the flag, so Q13's join now
// flattens and the honest answer is {13} — which is exactly why C-06's flip was
// not plan-neutral: Q13 is the ONE TPC-H query the retirement could move, and
// it did (see the C-06 evidence note in collapse.go's header).
//
// If this set changes, the corpus changed and the TPC-H channel's blast radius
// changed with it.
func TestExplicitJoinFlatteningOnTheTPCHCorpus(t *testing.T) {
	queries := tpch.Queries()
	var eligible []int
	unparsed := 0
	for qn, sql := range queries {
		got, _, err := explicitJoinFlattens(sql)
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
	if got, want := sprintInts(eligible), "13"; got != want {
		t.Errorf("TPC-H queries with a flattening explicit JOIN = {%s}, want {%s} — "+
			"the TPC-H channel's blast radius changed", got, want)
	}
	if unparsed != 0 {
		t.Errorf("TPC-H corpus: %d of %d queries did not parse", unparsed, len(queries))
	}
	t.Logf("TPC-H corpus: %d queries, flattening explicit JOIN in {%s}",
		len(queries), sprintInts(eligible))
}

// TestExplicitJoinFlatteningOnTheTPCDSCorpus is the other half of the same
// question, on the corpus the DS05 clause of the bar sweeps. It is the larger
// channel, which is why DS05 rather than TPC-H was the collapse pass's decisive
// one.
//
// Skips when the bench tree is absent (the queries are dsqgen output under
// bench/, not module data), so this never turns a clean checkout red.
func TestExplicitJoinFlatteningOnTheTPCDSCorpus(t *testing.T) {
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
		got, _, perr := explicitJoinFlattens(string(sql))
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
	// History of this pin: {72,75} while LEFT pinned; {40,49,72,75,78,80,93}
	// after C-04a/b relaxed the LEFT and RIGHT pins, under the retired flag's
	// "does the flag change this level's problem set" predicate; and the set
	// below under C-06's replacement predicate, which additionally counts the
	// two-way joins the old one could not see (upstream unwraps a one-element
	// side, so the old two arms agreed there — see the instrument case above).
	//
	// This IS the DS05 plan channel's blast radius: these are the queries
	// whose join ORDER the search chooses. The measurement goes through
	// `deconstructJointree` rather than a regex for P5.9-m's reason — the
	// lexical count and the planner's differ, and the planner's is the one
	// that runs.
	const want = tpcdsFlatteningSet
	if got != want {
		t.Errorf("TPC-DS queries with a flattening explicit JOIN = {%s}, want {%s} (of %d parsed; "+
			"%d unparseable %v).\nThis is the DS05 plan channel's blast radius; if it changed, "+
			"re-run 09 §3.18's protocol rather than re-quoting its numbers.",
			got, want, total, len(unparsed), unparsed)
	}
	t.Logf("TPC-DS corpus: %d queries, %d unparseable %v, flattening explicit JOIN in {%s}",
		total, len(unparsed), unparsed, got)
}

// levelHasSearchableInnerPrefix reports whether any query level of sql presents
// the shape M0127-P5.9-s peels: a joinlist topped by pinned LEFT links with at
// least two relations below them.
//
// It is the production predicate, not a paraphrase — `deconstructJointree` and
// `innerPrefixBelowOuterSpine` are the same calls the seam makes — because
// P5.9-m's whole lesson was that a corpus measurement
// re-derived from the SQL text answers a different question than the planner
// does (the `72,75` note above: `grep -c ' join '` finds three eligible queries
// where `deconstructJointree` finds two).
//
// The plan-tree half of the seam's check (`splitOuterSpine`) is not modelled: it
// can only DECLINE what this admits, so the count below is an upper bound on the
// corpus population, and it is labelled as one.
func levelHasSearchableInnerPrefix(sql string) (bool, error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, err
	}
	for _, st := range stmts {
		for _, sel := range selectLevels(st) {
			if len(sel.FromExprs) == 0 {
				continue
			}
			jl := deconstructJointree(sel.FromExprs, defaultCollapseLimits())
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
		ok, perr := levelHasSearchableInnerPrefix(string(sql))
		if perr != nil {
			continue
		}
		if ok {
			peelable = append(peelable, qn)
		}
	}
	sort.Ints(peelable)
	// C-04a re-pin: 2 -> 0, and EMPTY is the correct answer rather than a
	// regression. The peel existed because a LEFT link could not enter the
	// search; now it can, so `pinnedOuter` answers false for LEFT and no LEFT
	// link ever starts a spine again (DESIGN §3.1/§3.3). The peel survives for
	// RIGHT and FULL only — C-04b's and the ledger's scope — and the queries
	// that used to be peeled are now in the collapse-eligible set above, where
	// their whole chain is ONE problem instead of a prefix under a stack.
	//
	// Kept pinned rather than deleted: an empty set is the assertion that LEFT
	// has LEFT the spine, and a non-empty one would mean a LEFT link found its
	// way back onto it.
	const want = ""
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
// M0127-P5.9-r changed no plan. It was also the fact the collapse flip had to
// start from; take3 C-06 has since decided that flip (see this file's header),
// and the measurement is kept as the standing statement about the corpus.
//
// P5.9-r lifted the precondition P5.9-m recorded: the seam now flattens an
// explicit INNER chain and routes its `ON` quals into the search's clause list,
// which `TestExplicitJoinChainReachesTheSearch` demonstrates on a
// three-relation chain.
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
		if _, _, perr := explicitJoinFlattens(string(sql)); perr == nil {
			withJoin = append(withJoin, qn)
		}
	}
	sort.Ints(innerOnly)
	if len(innerOnly) != 0 {
		t.Errorf("TPC-DS queries with an INNER-only explicit-JOIN chain = {%s}, want none (of %d).\n"+
			"A corpus query the seam can now flatten means the DS05 plan A/B can move on an "+
			"INNER-only chain, which nothing in this corpus has ever exercised.",
			sprintInts(innerOnly), total)
	}
	t.Logf("TPC-DS corpus: %d queries, %d inner-only explicit-JOIN chains", total, len(innerOnly))
}

// TestExplicitJoinChainReachesTheSearch is the INVERSION of M0127-P5.9-m's
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
// joinsearchseam.go). This test pins the consequence: an explicit-JOIN chain
// reaches the search, so the flattening decides a join ORDER rather than
// deciding nothing, and M0127-P5.9-m's no-go — which was a statement about a
// flag that could not move a plan — did not stand on its own evidence.
//
// It ran both flag arms until take3 C-06 retired `GOOPG_PGSHAPED_COLLAPSE`.
// Nothing is lost by dropping the second arm: the test deliberately never
// asserted that the arms DIFFER on this fixture (three relations under
// `join_collapse_limit` is a shape where a pinned order and a searched order
// can legitimately coincide). The claim under test is reachability, which is
// what was false before P5.9-r, and it is the surviving arm that carries it.
//
// Reachability in the PLANNER is not reachability in the CORPUS, and the two
// must not be confused — confusing them is the exact defect P5.9-m recorded.
// `TestNoCorpusQueryHasAnInnerOnlyJoinChain` above measures the second.
func TestExplicitJoinChainReachesTheSearch(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	// The chain `planFromItem` builds for that FROM clause, and the joinlist
	// the deconstruction actually produces for it.
	node, ctx := seamInnerChain(t, names, []int64{100, 100, 100})
	out, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined an explicit-JOIN chain — the P5.9-r walk has regressed")
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v)", want, got)
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
